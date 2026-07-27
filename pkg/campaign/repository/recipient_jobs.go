package campaign_repository

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxRecipientClaimBatch = 100

var safeCampaignErrorCode = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

func (r *campaignRepository) ClaimReady(ctx context.Context, limit int, leaseDuration time.Duration) ([]campaign_model.Recipient, error) {
	return r.claimReady(ctx, "", limit, leaseDuration)
}

func (r *campaignRepository) ClaimReadyForInstance(ctx context.Context, instanceID string, limit int, leaseDuration time.Duration) ([]campaign_model.Recipient, error) {
	if uuid.Validate(instanceID) != nil {
		return nil, errors.New("campaign claim instance identity is invalid")
	}
	return r.claimReady(ctx, instanceID, limit, leaseDuration)
}

func (r *campaignRepository) claimReady(ctx context.Context, instanceID string, limit int, leaseDuration time.Duration) ([]campaign_model.Recipient, error) {
	if r == nil || r.db == nil || r.now == nil || ctx == nil {
		return nil, errors.New("campaign recipient repository is required")
	}
	if limit < 1 || limit > maxRecipientClaimBatch {
		return nil, fmt.Errorf("recipient claim limit must be between 1 and %d", maxRecipientClaimBatch)
	}
	if leaseDuration <= 0 {
		return nil, errors.New("recipient claim lease must be positive")
	}
	if err := r.groupSafety.validate(); err != nil {
		return nil, err
	}
	direct, err := r.claimDirectReady(ctx, instanceID, limit, leaseDuration)
	if err != nil || len(direct) == limit || !r.groupSafety.Enabled {
		return direct, err
	}
	groups, err := r.claimGroupReady(ctx, instanceID, limit-len(direct), leaseDuration)
	return append(direct, groups...), err
}

func (r *campaignRepository) claimDirectReady(ctx context.Context, instanceID string, limit int, leaseDuration time.Duration) ([]campaign_model.Recipient, error) {
	now := r.now().UTC()
	claimToken := uuid.NewString()
	leaseUntil := now.Add(leaseDuration)
	var instanceFilter any
	if instanceID != "" {
		instanceFilter = instanceID
	}
	var recipients []campaign_model.Recipient
	err := r.db.WithContext(ctx).Raw(`WITH candidates AS (
    SELECT recipients.id
    FROM campaign_recipients AS recipients
    JOIN campaigns ON campaigns.id = recipients.campaign_id
        AND campaigns.instance_id = recipients.instance_id
    WHERE campaigns.status = 'running'
	  AND recipients.target_type = 'direct'
      AND (CAST(? AS uuid) IS NULL OR recipients.instance_id = CAST(? AS uuid))
      AND ((recipients.status = 'pending' AND recipients.next_attempt_at <= ?)
        OR (recipients.status = 'processing' AND recipients.lease_until <= ?))
    ORDER BY recipients.next_attempt_at ASC, recipients.campaign_id ASC, recipients.id ASC
    FOR KEY SHARE OF campaigns
    FOR UPDATE OF recipients SKIP LOCKED
    LIMIT ?
)
UPDATE campaign_recipients AS recipients
SET status = 'processing', claim_token = ?, lease_until = ?, last_error_code = NULL, updated_at = ?
FROM candidates
WHERE recipients.id = candidates.id
RETURNING recipients.*`, instanceFilter, instanceFilter, now, now, limit, claimToken, leaseUntil, now).Scan(&recipients).Error
	return recipients, err
}

func (r *campaignRepository) claimGroupReady(ctx context.Context, instanceID string, limit int, leaseDuration time.Duration) ([]campaign_model.Recipient, error) {
	now := r.now().UTC()
	claimToken := uuid.NewString()
	leaseUntil := now.Add(leaseDuration)
	cooldownCutoff := now.Add(-r.groupSafety.Cooldown)
	var instanceFilter any
	if instanceID != "" {
		instanceFilter = instanceID
	}
	var recipients []campaign_model.Recipient
	err := r.db.WithContext(ctx).Raw(`WITH candidates AS (
    SELECT recipients.id, recipients.campaign_id, recipients.instance_id, recipients.recipient_jid
    FROM campaign_recipients AS recipients
    JOIN campaigns ON campaigns.id = recipients.campaign_id
        AND campaigns.instance_id = recipients.instance_id
    JOIN campaign_group_delivery_guards AS guards ON guards.instance_id = recipients.instance_id
        AND guards.group_jid = recipients.recipient_jid
    LEFT JOIN campaign_instance_circuits AS circuits ON circuits.instance_id = recipients.instance_id
    WHERE campaigns.status = 'running'
      AND recipients.target_type = 'group'
      AND (CAST(? AS uuid) IS NULL OR recipients.instance_id = CAST(? AS uuid))
      AND (circuits.instance_id IS NULL OR circuits.open_until <= ?)
      AND (guards.lease_until IS NULL OR guards.lease_until <= ?)
      AND (guards.last_acknowledged_at IS NULL OR guards.last_acknowledged_at <= ?)
      AND ((recipients.status = 'pending' AND recipients.next_attempt_at <= ?)
        OR (recipients.status = 'processing' AND recipients.lease_until <= ?))
      AND recipients.id = (
        SELECT competing.id
        FROM campaign_recipients AS competing
        JOIN campaigns AS competing_campaign ON competing_campaign.id = competing.campaign_id
            AND competing_campaign.instance_id = competing.instance_id
        WHERE competing.instance_id = recipients.instance_id
          AND competing.recipient_jid = recipients.recipient_jid
          AND competing.target_type = 'group'
          AND competing_campaign.status = 'running'
          AND ((competing.status = 'pending' AND competing.next_attempt_at <= ?)
            OR (competing.status = 'processing' AND competing.lease_until <= ?))
        ORDER BY competing.next_attempt_at ASC, competing.campaign_id ASC, competing.id ASC
        LIMIT 1
      )
    ORDER BY recipients.next_attempt_at ASC, recipients.campaign_id ASC, recipients.id ASC
    FOR KEY SHARE OF campaigns
    FOR UPDATE OF recipients, guards SKIP LOCKED
    LIMIT ?
), claimed_guards AS (
    UPDATE campaign_group_delivery_guards AS guards
    SET owner_recipient_id = candidates.id,
        owner_campaign_id = candidates.campaign_id,
        claim_token = ?, lease_until = ?, updated_at = ?
    FROM candidates
    WHERE guards.instance_id = candidates.instance_id
      AND guards.group_jid = candidates.recipient_jid
    RETURNING guards.instance_id, guards.group_jid
), claimed_recipients AS (
    UPDATE campaign_recipients AS recipients
    SET status = 'processing', claim_token = ?, lease_until = ?, last_error_code = NULL, updated_at = ?
    FROM candidates
    JOIN claimed_guards ON claimed_guards.instance_id = candidates.instance_id
        AND claimed_guards.group_jid = candidates.recipient_jid
    WHERE recipients.id = candidates.id
    RETURNING recipients.*
)
SELECT * FROM claimed_recipients`, instanceFilter, instanceFilter, now, now, cooldownCutoff, now, now, now, now, limit,
		claimToken, leaseUntil, now, claimToken, leaseUntil, now).Scan(&recipients).Error
	return recipients, err
}

func (r *campaignRepository) MarkSent(ctx context.Context, recipient *campaign_model.Recipient, providerMessageID string) error {
	providerMessageID = boundedProviderMessageID(providerMessageID)
	if err := r.validateClaimMutation(ctx, recipient); err != nil {
		return err
	}
	if providerMessageID == "" {
		return errors.New("provider message identity is required")
	}
	now := r.now().UTC()
	return r.finishClaim(ctx, recipient, campaign_model.RecipientStatusSent, "recipient_sent", map[string]any{
		"provider_message_id": providerMessageID, "sent_at": now, "attempt_count": gorm.Expr("attempt_count + 1"),
		"last_error_code": nil, "next_attempt_at": now,
	}, now)
}

func (r *campaignRepository) MarkRetry(ctx context.Context, recipient *campaign_model.Recipient, errorCode string, retryAt time.Time) error {
	return r.reschedule(ctx, recipient, errorCode, retryAt, true, "recipient_retry_scheduled")
}

func (r *campaignRepository) MarkDeferred(ctx context.Context, recipient *campaign_model.Recipient, errorCode string, retryAt time.Time) error {
	return r.reschedule(ctx, recipient, errorCode, retryAt, false, "recipient_deferred")
}

func (r *campaignRepository) reschedule(ctx context.Context, recipient *campaign_model.Recipient, errorCode string, retryAt time.Time, countAttempt bool, eventType string) error {
	if err := r.validateClaimMutation(ctx, recipient); err != nil {
		return err
	}
	now := r.now().UTC()
	if !safeCampaignErrorCode.MatchString(errorCode) || retryAt.IsZero() || !retryAt.After(now) {
		return errors.New("safe campaign error code and retry time are required")
	}
	updates := map[string]any{"next_attempt_at": retryAt.UTC(), "last_error_code": errorCode}
	if countAttempt {
		updates["attempt_count"] = gorm.Expr("attempt_count + 1")
	}
	return r.finishClaim(ctx, recipient, campaign_model.RecipientStatusPending, eventType, updates, now)
}

func (r *campaignRepository) MarkFailed(ctx context.Context, recipient *campaign_model.Recipient, errorCode string) error {
	if err := r.validateClaimMutation(ctx, recipient); err != nil {
		return err
	}
	if !safeCampaignErrorCode.MatchString(errorCode) {
		return errors.New("safe campaign error code is required")
	}
	now := r.now().UTC()
	return r.finishClaim(ctx, recipient, campaign_model.RecipientStatusFailed, "recipient_failed", map[string]any{
		"attempt_count": gorm.Expr("attempt_count + 1"), "last_error_code": errorCode, "next_attempt_at": now,
	}, now)
}

func (r *campaignRepository) MarkSkipped(ctx context.Context, recipient *campaign_model.Recipient, errorCode string) error {
	if err := r.validateClaimMutation(ctx, recipient); err != nil {
		return err
	}
	if !safeCampaignErrorCode.MatchString(errorCode) {
		return errors.New("safe campaign error code is required")
	}
	now := r.now().UTC()
	return r.finishClaim(ctx, recipient, campaign_model.RecipientStatusSkipped, "recipient_skipped", map[string]any{
		"last_error_code": errorCode, "next_attempt_at": now,
	}, now)
}

func (r *campaignRepository) finishClaim(ctx context.Context, recipient *campaign_model.Recipient, target campaign_model.RecipientStatus, eventType string, updates map[string]any, now time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var campaign campaign_model.Campaign
		trackGroupFailure := recipient.TargetType == campaign_model.RecipientTargetGroup && target == campaign_model.RecipientStatusFailed
		campaignAutoPaused := false
		groupFailureCode := ""
		if trackGroupFailure {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id = ? AND id = ?", recipient.InstanceID, recipient.CampaignID).First(&campaign).Error; err != nil {
				return err
			}
		}
		if recipient.TargetType == campaign_model.RecipientTargetGroup {
			guardUpdates := map[string]any{
				"owner_recipient_id": nil, "owner_campaign_id": nil, "claim_token": nil, "lease_until": nil, "updated_at": now,
			}
			if target == campaign_model.RecipientStatusSent {
				guardUpdates["last_acknowledged_at"] = now
			}
			guard := tx.Model(&groupDeliveryGuard{}).Where(
				"instance_id = ? AND group_jid = ? AND owner_recipient_id = ? AND owner_campaign_id = ? AND claim_token = ?",
				recipient.InstanceID, recipient.RecipientJID, recipient.ID, recipient.CampaignID, *recipient.ClaimToken,
			).Updates(guardUpdates)
			if guard.Error != nil {
				return guard.Error
			}
			if guard.RowsAffected != 1 {
				return ErrRecipientClaimLost
			}
		}
		updates["status"] = target
		updates["claim_token"] = nil
		updates["lease_until"] = nil
		updates["updated_at"] = now
		result := tx.Model(&campaign_model.Recipient{}).Where(
			"id = ? AND campaign_id = ? AND instance_id = ? AND status = ? AND claim_token = ?",
			recipient.ID, recipient.CampaignID, recipient.InstanceID, campaign_model.RecipientStatusProcessing, *recipient.ClaimToken,
		).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrRecipientClaimLost
		}
		if trackGroupFailure {
			campaignUpdates := map[string]any{
				"failure_signal_count": gorm.Expr("failure_signal_count + 1"), "updated_at": now, "version": gorm.Expr("version + 1"),
			}
			groupFailureCode, _ = updates["last_error_code"].(string)
			if campaign.FailureSignalCount+1 >= r.groupSafety.FailurePauseThreshold || groupFailureCode == "instance_not_authorized" {
				campaignAutoPaused = true
				campaignUpdates["status"] = campaign_model.CampaignStatusPaused
				campaignUpdates["pause_reason"] = "failure_threshold_exceeded"
				if groupFailureCode == "instance_not_authorized" {
					campaignUpdates["pause_reason"] = "instance_not_authorized"
					campaignUpdates["needs_attention"] = true
				}
			}
			campaignResult := tx.Model(&campaign_model.Campaign{}).
				Where("instance_id = ? AND id = ? AND version = ?", campaign.InstanceID, campaign.ID, campaign.Version).
				Updates(campaignUpdates)
			if campaignResult.Error != nil {
				return campaignResult.Error
			}
			if campaignResult.RowsAffected != 1 {
				return ErrCampaignConflict
			}
		}
		from, to := string(campaign_model.RecipientStatusProcessing), string(target)
		if err := tx.Create(newAuditEvent(&campaign_model.Campaign{ID: recipient.CampaignID, InstanceID: recipient.InstanceID}, &recipient.ID,
			eventType, "system", nil, from, to, now)).Error; err != nil {
			return err
		}
		if campaignAutoPaused {
			campaignFrom, campaignTo := string(campaign.Status), string(campaign_model.CampaignStatusPaused)
			audit := newAuditEvent(&campaign, nil, "campaign_auto_paused", "system", nil, campaignFrom, campaignTo, now)
			audit.Metadata = safeErrorMetadata(groupFailureCode)
			return tx.Create(audit).Error
		}
		return nil
	})
}

func (r *campaignRepository) validateClaimMutation(ctx context.Context, recipient *campaign_model.Recipient) error {
	if r == nil || r.db == nil || r.now == nil || ctx == nil || recipient == nil || uuid.Validate(recipient.ID) != nil ||
		uuid.Validate(recipient.CampaignID) != nil || uuid.Validate(recipient.InstanceID) != nil || recipient.ClaimToken == nil || uuid.Validate(*recipient.ClaimToken) != nil {
		return errors.New("active campaign recipient claim is required")
	}
	return nil
}

func boundedProviderMessageID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 255 {
		return ""
	}
	return value
}
