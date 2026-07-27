package campaign_repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *campaignRepository) ActivateGroupCampaign(ctx context.Context, instanceID, campaignID, instanceJID string, actor Actor) (*campaign_model.Campaign, error) {
	if r == nil || r.db == nil || r.now == nil || r.groupEligibility == nil || ctx == nil || uuid.Validate(instanceID) != nil || uuid.Validate(campaignID) != nil || strings.TrimSpace(instanceJID) == "" {
		return nil, errors.New("group campaign activation dependencies and identities are required")
	}
	actor.Type = strings.TrimSpace(actor.Type)
	actorHash, err := validateActor(actor, campaignID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCampaignInput, err)
	}
	now := r.now().UTC()
	var campaign campaign_model.Campaign
	noEligible := false
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id = ? AND id = ?", instanceID, campaignID).First(&campaign).Error; err != nil {
			return err
		}
		if campaign.TargetType != campaign_model.CampaignTargetGroupList ||
			(campaign.Status != campaign_model.CampaignStatusScheduled && campaign.Status != campaign_model.CampaignStatusPaused) || campaign.NeedsAttention {
			return ErrInvalidCampaignTransition
		}
		var recipients []campaign_model.Recipient
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("instance_id = ? AND campaign_id = ? AND target_type = ? AND status = ?", instanceID, campaignID, campaign_model.RecipientTargetGroup, campaign_model.RecipientStatusPending).
			Order("recipient_jid ASC").Find(&recipients).Error; err != nil {
			return err
		}
		if len(recipients) == 0 {
			return ErrNoEligibleTargets
		}
		groupJIDs := make([]string, len(recipients))
		for index := range recipients {
			groupJIDs[index] = recipients[index].RecipientJID
		}
		results, err := r.groupEligibility(ctx, tx, instanceID, instanceJID, groupJIDs)
		if err != nil {
			return err
		}
		if len(results) != len(recipients) {
			return errors.New("group activation eligibility does not match target snapshot")
		}
		eligible := 0
		for index, result := range results {
			jid, jidErr := canonicalGroupTargetJID(result.GroupJID)
			if jidErr != nil || jid != recipients[index].RecipientJID {
				return errors.New("group activation eligibility identity mismatch")
			}
			switch result.Eligibility {
			case "eligible":
				eligible++
			case "unknown":
				return ErrGroupProjectionNotReady
			case "unavailable":
				reason := strings.TrimSpace(result.Reason)
				if !safeCampaignErrorCode.MatchString(reason) {
					return errors.New("group activation eligibility reason is invalid")
				}
				update := tx.Model(&campaign_model.Recipient{}).
					Where("id = ? AND campaign_id = ? AND instance_id = ? AND status = ?", recipients[index].ID, campaignID, instanceID, campaign_model.RecipientStatusPending).
					Updates(map[string]any{"status": campaign_model.RecipientStatusSkipped, "last_error_code": reason, "next_attempt_at": now, "updated_at": now})
				if update.Error != nil {
					return update.Error
				}
				if update.RowsAffected != 1 {
					return ErrCampaignConflict
				}
				from, to := string(campaign_model.RecipientStatusPending), string(campaign_model.RecipientStatusSkipped)
				audit := newAuditEvent(&campaign, &recipients[index].ID, "recipient_skipped", "system", nil, from, to, now)
				audit.Metadata = safeErrorMetadata(reason)
				if err := tx.Create(audit).Error; err != nil {
					return err
				}
			default:
				return errors.New("group activation eligibility state is invalid")
			}
		}
		from := string(campaign.Status)
		updates := map[string]any{"version": gorm.Expr("version + 1"), "updated_at": now, "retry_at": nil, "pause_reason": nil, "status_reason": nil}
		to := string(campaign_model.CampaignStatusRunning)
		if eligible == 0 {
			noEligible = true
			to = string(campaign_model.CampaignStatusFailed)
			updates["status"] = campaign_model.CampaignStatusFailed
			updates["status_reason"] = "campaign_no_eligible_targets"
			updates["finished_at"] = now
		} else {
			updates["status"] = campaign_model.CampaignStatusRunning
			updates["finished_at"] = nil
		}
		result := tx.Model(&campaign_model.Campaign{}).
			Where("instance_id = ? AND id = ? AND version = ?", instanceID, campaignID, campaign.Version).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrCampaignConflict
		}
		if err := tx.Create(newAuditEvent(&campaign, nil, "status_changed", actor.Type, actorHash, from, to, now)).Error; err != nil {
			return err
		}
		return tx.Where("instance_id = ? AND id = ?", instanceID, campaignID).First(&campaign).Error
	})
	if err != nil {
		return &campaign, err
	}
	if noEligible {
		return &campaign, ErrNoEligibleTargets
	}
	return &campaign, nil
}

func safeErrorMetadata(code string) []byte {
	return []byte(`{"errorCode":"` + code + `"}`)
}
