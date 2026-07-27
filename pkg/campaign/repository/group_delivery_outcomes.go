package campaign_repository

import (
	"context"
	"errors"
	"strings"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *campaignRepository) RevalidateGroupClaim(ctx context.Context, recipient *campaign_model.Recipient) (GroupEligibilityResult, error) {
	var empty GroupEligibilityResult
	if err := r.validateClaimMutation(ctx, recipient); err != nil {
		return empty, err
	}
	if recipient.TargetType != campaign_model.RecipientTargetGroup || r.groupEligibility == nil {
		return empty, errors.New("active group campaign claim and eligibility evaluator are required")
	}
	var campaign campaign_model.Campaign
	if err := r.db.WithContext(ctx).Select("status", "retry_at", "pause_reason").Where("instance_id = ? AND id = ?", recipient.InstanceID, recipient.CampaignID).First(&campaign).Error; err != nil {
		return empty, err
	}
	if campaign.Status != campaign_model.CampaignStatusRunning {
		retryAt := r.now().UTC().Add(time.Minute)
		if campaign.RetryAt != nil && campaign.RetryAt.After(retryAt) {
			retryAt = campaign.RetryAt.UTC()
		}
		return GroupEligibilityResult{GroupJID: recipient.RecipientJID, Eligibility: "deferred", Reason: "campaign_paused", RetryAt: &retryAt}, nil
	}
	var circuit instanceCircuit
	err := r.db.WithContext(ctx).Where("instance_id = ? AND open_until > ?", recipient.InstanceID, r.now().UTC()).First(&circuit).Error
	if err == nil {
		retryAt := circuit.OpenUntil.UTC()
		return GroupEligibilityResult{GroupJID: recipient.RecipientJID, Eligibility: "deferred", Reason: circuit.Reason, RetryAt: &retryAt}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return empty, err
	}
	results, err := r.groupEligibility(ctx, r.db.WithContext(ctx), recipient.InstanceID, "", []string{recipient.RecipientJID})
	if err != nil {
		return empty, err
	}
	if len(results) != 1 || results[0].GroupJID != recipient.RecipientJID {
		return empty, errors.New("group claim eligibility identity mismatch")
	}
	return results[0], nil
}

func (r *campaignRepository) MarkProjectionUnknown(ctx context.Context, recipient *campaign_model.Recipient, retryAt time.Time) error {
	return r.finishGroupSignal(ctx, recipient, "projection_not_ready", retryAt, groupSignalProjectionUnknown)
}

func (r *campaignRepository) MarkRateLimited(ctx context.Context, recipient *campaign_model.Recipient, errorCode string, retryAt time.Time) error {
	if !safeCampaignErrorCode.MatchString(errorCode) {
		return errors.New("safe campaign error code is required")
	}
	return r.finishGroupSignal(ctx, recipient, errorCode, retryAt, groupSignalRateLimited)
}

func (r *campaignRepository) MarkUnknownOutcome(ctx context.Context, recipient *campaign_model.Recipient) error {
	return r.finishGroupSignal(ctx, recipient, "unknown_send_outcome", time.Time{}, groupSignalUnknownOutcome)
}

type groupSignal int

const (
	groupSignalProjectionUnknown groupSignal = iota
	groupSignalRateLimited
	groupSignalUnknownOutcome
)

func (r *campaignRepository) finishGroupSignal(ctx context.Context, recipient *campaign_model.Recipient, code string, retryAt time.Time, signal groupSignal) error {
	if err := r.validateClaimMutation(ctx, recipient); err != nil {
		return err
	}
	if recipient.TargetType != campaign_model.RecipientTargetGroup || !safeCampaignErrorCode.MatchString(code) {
		return errors.New("active group claim and safe error code are required")
	}
	now := r.now().UTC()
	if signal != groupSignalUnknownOutcome && (retryAt.IsZero() || !retryAt.After(now)) {
		return errors.New("future retry time is required")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var campaign campaign_model.Campaign
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id = ? AND id = ?", recipient.InstanceID, recipient.CampaignID).First(&campaign).Error; err != nil {
			return err
		}
		guard := tx.Model(&groupDeliveryGuard{}).Where(
			"instance_id = ? AND group_jid = ? AND owner_recipient_id = ? AND owner_campaign_id = ? AND claim_token = ?",
			recipient.InstanceID, recipient.RecipientJID, recipient.ID, recipient.CampaignID, *recipient.ClaimToken,
		).Updates(map[string]any{"owner_recipient_id": nil, "owner_campaign_id": nil, "claim_token": nil, "lease_until": nil, "updated_at": now})
		if guard.Error != nil {
			return guard.Error
		}
		if guard.RowsAffected != 1 {
			return ErrRecipientClaimLost
		}

		recipientStatus := campaign_model.RecipientStatusPending
		recipientUpdates := map[string]any{
			"status": recipientStatus, "claim_token": nil, "lease_until": nil, "last_error_code": code, "updated_at": now,
		}
		campaignUpdates := map[string]any{"updated_at": now, "version": gorm.Expr("version + 1")}
		eventType := "recipient_deferred"
		if signal == groupSignalProjectionUnknown {
			recipientUpdates["next_attempt_at"] = retryAt.UTC()
			campaignUpdates["status"] = campaign_model.CampaignStatusPaused
			campaignUpdates["pause_reason"] = "projection_not_ready"
			campaignUpdates["retry_at"] = retryAt.UTC()
			eventType = "campaign_paused_projection_not_ready"
		} else if signal == groupSignalRateLimited {
			recipientUpdates["next_attempt_at"] = retryAt.UTC()
			recipientUpdates["attempt_count"] = gorm.Expr("attempt_count + 1")
			openUntil := now.Add(r.groupSafety.CircuitDuration)
			if retryAt.After(openUntil) {
				openUntil = retryAt.UTC()
			}
			circuit := instanceCircuit{InstanceID: recipient.InstanceID, OpenUntil: openUntil, Reason: code, UpdatedAt: now}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "instance_id"}}, DoUpdates: clause.Assignments(map[string]any{
				"open_until": gorm.Expr("GREATEST(campaign_instance_circuits.open_until, EXCLUDED.open_until)"), "reason": code, "updated_at": now,
			})}).Create(&circuit).Error; err != nil {
				return err
			}
			campaignUpdates["rate_limit_signal_count"] = gorm.Expr("rate_limit_signal_count + 1")
			campaignUpdates["retry_at"] = openUntil
			if campaign.RateLimitSignalCount+1 >= r.groupSafety.RatePauseThreshold {
				campaignUpdates["status"] = campaign_model.CampaignStatusPaused
				campaignUpdates["pause_reason"] = "rate_limit_threshold_exceeded"
			}
			eventType = "instance_circuit_opened"
		} else {
			recipientStatus = campaign_model.RecipientStatusFailed
			recipientUpdates["status"] = recipientStatus
			recipientUpdates["next_attempt_at"] = now
			recipientUpdates["attempt_count"] = gorm.Expr("attempt_count + 1")
			campaignUpdates["status"] = campaign_model.CampaignStatusPaused
			campaignUpdates["pause_reason"] = "unknown_send_outcome"
			campaignUpdates["status_reason"] = "unknown_send_outcome"
			campaignUpdates["needs_attention"] = true
			campaignUpdates["retry_at"] = nil
			eventType = "unknown_send_outcome"
		}
		updatedRecipient := tx.Model(&campaign_model.Recipient{}).Where(
			"id = ? AND campaign_id = ? AND instance_id = ? AND status = ? AND claim_token = ?",
			recipient.ID, recipient.CampaignID, recipient.InstanceID, campaign_model.RecipientStatusProcessing, *recipient.ClaimToken,
		).Updates(recipientUpdates)
		if updatedRecipient.Error != nil {
			return updatedRecipient.Error
		}
		if updatedRecipient.RowsAffected != 1 {
			return ErrRecipientClaimLost
		}
		campaignResult := tx.Model(&campaign_model.Campaign{}).Where("instance_id = ? AND id = ? AND version = ?", campaign.InstanceID, campaign.ID, campaign.Version).Updates(campaignUpdates)
		if campaignResult.Error != nil {
			return campaignResult.Error
		}
		if campaignResult.RowsAffected != 1 {
			return ErrCampaignConflict
		}
		from, to := string(campaign_model.RecipientStatusProcessing), string(recipientStatus)
		audit := newAuditEvent(&campaign, &recipient.ID, eventType, "system", nil, from, to, now)
		audit.Metadata = safeErrorMetadata(strings.TrimSpace(code))
		if err := tx.Create(audit).Error; err != nil {
			return err
		}
		if signal == groupSignalProjectionUnknown || signal == groupSignalUnknownOutcome ||
			(signal == groupSignalRateLimited && campaign.RateLimitSignalCount+1 >= r.groupSafety.RatePauseThreshold) {
			campaignFrom, campaignTo := string(campaign.Status), string(campaign_model.CampaignStatusPaused)
			campaignAudit := newAuditEvent(&campaign, nil, "campaign_auto_paused", "system", nil, campaignFrom, campaignTo, now)
			campaignAudit.Metadata = safeErrorMetadata(strings.TrimSpace(code))
			return tx.Create(campaignAudit).Error
		}
		return nil
	})
}
