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

func (r *campaignRepository) finishClaim(ctx context.Context, recipient *campaign_model.Recipient, target campaign_model.RecipientStatus, eventType string, updates map[string]any, now time.Time) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		from, to := string(campaign_model.RecipientStatusProcessing), string(target)
		return tx.Create(newAuditEvent(&campaign_model.Campaign{ID: recipient.CampaignID, InstanceID: recipient.InstanceID}, &recipient.ID,
			eventType, "system", nil, from, to, now)).Error
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
