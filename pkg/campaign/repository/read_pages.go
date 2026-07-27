package campaign_repository

import (
	"context"
	"errors"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	"github.com/google/uuid"
)

const maxCampaignPageSize = 100

type CampaignCursor struct {
	CreatedAt time.Time
	ID        string
}

type CampaignPage struct {
	Items      []campaign_model.Campaign
	NextCursor *CampaignCursor
}

type RecipientCursor struct {
	CreatedAt time.Time
	ID        string
}

type RecipientPage struct {
	Items      []campaign_model.Recipient
	NextCursor *RecipientCursor
}

type AuditCursor struct {
	OccurredAt time.Time
	ID         string
}

type AuditPage struct {
	Items      []campaign_model.AuditEvent
	NextCursor *AuditCursor
}

type RecipientProgress struct {
	Counts    map[campaign_model.RecipientStatus]int64
	UpdatedAt time.Time
	RetryAt   *time.Time
}

func (r *campaignRepository) ListCampaigns(ctx context.Context, instanceID string, status campaign_model.CampaignStatus, limit int, cursor *CampaignCursor) (*CampaignPage, error) {
	if r == nil || r.db == nil || ctx == nil || uuid.Validate(instanceID) != nil || limit < 1 || limit > maxCampaignPageSize ||
		(status != "" && !validCampaignStatus(status)) || (cursor != nil && (cursor.CreatedAt.IsZero() || uuid.Validate(cursor.ID) != nil)) {
		return nil, errors.New("valid campaign list parameters are required")
	}
	query := r.db.WithContext(ctx).Where("instance_id = ?", instanceID)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if cursor != nil {
		at := cursor.CreatedAt.UTC()
		query = query.Where("created_at < ? OR (created_at = ? AND id < ?)", at, at, cursor.ID)
	}
	var campaigns []campaign_model.Campaign
	if err := query.Order("created_at DESC, id DESC").Limit(limit + 1).Find(&campaigns).Error; err != nil {
		return nil, err
	}
	page := &CampaignPage{Items: campaigns}
	if len(campaigns) > limit {
		page.Items = campaigns[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = &CampaignCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return page, nil
}

func (r *campaignRepository) ListRecipients(ctx context.Context, instanceID, campaignID string, limit int, cursor *RecipientCursor) (*RecipientPage, error) {
	if r == nil || r.db == nil || ctx == nil || uuid.Validate(instanceID) != nil || uuid.Validate(campaignID) != nil ||
		limit < 1 || limit > maxCampaignPageSize || (cursor != nil && (cursor.CreatedAt.IsZero() || uuid.Validate(cursor.ID) != nil)) {
		return nil, errors.New("valid campaign recipient list parameters are required")
	}
	query := r.db.WithContext(ctx).Where("instance_id = ? AND campaign_id = ?", instanceID, campaignID)
	if cursor != nil {
		at := cursor.CreatedAt.UTC()
		query = query.Where("created_at > ? OR (created_at = ? AND id > ?)", at, at, cursor.ID)
	}
	var recipients []campaign_model.Recipient
	if err := query.Order("created_at ASC, id ASC").Limit(limit + 1).Find(&recipients).Error; err != nil {
		return nil, err
	}
	page := &RecipientPage{Items: recipients}
	if len(recipients) > limit {
		page.Items = recipients[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = &RecipientCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return page, nil
}

func (r *campaignRepository) ListAuditPage(ctx context.Context, instanceID, campaignID string, limit int, cursor *AuditCursor) (*AuditPage, error) {
	if r == nil || r.db == nil || ctx == nil || uuid.Validate(instanceID) != nil || uuid.Validate(campaignID) != nil ||
		limit < 1 || limit > maxCampaignPageSize || (cursor != nil && (cursor.OccurredAt.IsZero() || uuid.Validate(cursor.ID) != nil)) {
		return nil, errors.New("valid campaign audit list parameters are required")
	}
	query := r.db.WithContext(ctx).Where("instance_id = ? AND campaign_id = ?", instanceID, campaignID)
	if cursor != nil {
		at := cursor.OccurredAt.UTC()
		query = query.Where("occurred_at > ? OR (occurred_at = ? AND id > ?)", at, at, cursor.ID)
	}
	var events []campaign_model.AuditEvent
	if err := query.Order("occurred_at ASC, id ASC").Limit(limit + 1).Find(&events).Error; err != nil {
		return nil, err
	}
	page := &AuditPage{Items: events}
	if len(events) > limit {
		page.Items = events[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = &AuditCursor{OccurredAt: last.OccurredAt, ID: last.ID}
	}
	return page, nil
}

func (r *campaignRepository) RecipientCounts(ctx context.Context, instanceID, campaignID string) (map[campaign_model.RecipientStatus]int64, error) {
	if r == nil || r.db == nil || ctx == nil || uuid.Validate(instanceID) != nil || uuid.Validate(campaignID) != nil {
		return nil, errors.New("campaign repository and identities are required")
	}
	snapshots, err := r.ProgressSnapshots(ctx, instanceID, []string{campaignID})
	if err != nil {
		return nil, err
	}
	if snapshot, exists := snapshots[campaignID]; exists {
		return snapshot.Counts, nil
	}
	return map[campaign_model.RecipientStatus]int64{}, nil
}

func (r *campaignRepository) ProgressSnapshots(ctx context.Context, instanceID string, campaignIDs []string) (map[string]RecipientProgress, error) {
	if r == nil || r.db == nil || ctx == nil || uuid.Validate(instanceID) != nil || len(campaignIDs) == 0 || len(campaignIDs) > maxCampaignPageSize {
		return nil, errors.New("bounded campaign progress identities are required")
	}
	for _, campaignID := range campaignIDs {
		if uuid.Validate(campaignID) != nil {
			return nil, errors.New("valid campaign progress identities are required")
		}
	}
	type progressRow struct {
		CampaignID string
		Status     campaign_model.RecipientStatus
		Count      int64
		UpdatedAt  time.Time
		RetryAt    *time.Time
	}
	var rows []progressRow
	if err := r.db.WithContext(ctx).Raw(`SELECT campaign_id, status, COUNT(*) AS count,
MAX(updated_at) AS updated_at,
MIN(next_attempt_at) FILTER (WHERE status = 'pending' AND next_attempt_at > NOW()) AS retry_at
FROM campaign_recipients
WHERE instance_id = ? AND campaign_id IN ?
GROUP BY campaign_id, status`, instanceID, campaignIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]RecipientProgress, len(campaignIDs))
	for _, row := range rows {
		snapshot := result[row.CampaignID]
		if snapshot.Counts == nil {
			snapshot.Counts = make(map[campaign_model.RecipientStatus]int64)
		}
		snapshot.Counts[row.Status] = row.Count
		if row.UpdatedAt.After(snapshot.UpdatedAt) {
			snapshot.UpdatedAt = row.UpdatedAt.UTC()
		}
		if row.RetryAt != nil && (snapshot.RetryAt == nil || row.RetryAt.Before(*snapshot.RetryAt)) {
			retryAt := row.RetryAt.UTC()
			snapshot.RetryAt = &retryAt
		}
		result[row.CampaignID] = snapshot
	}
	var circuit struct{ OpenUntil time.Time }
	if err := r.db.WithContext(ctx).Raw(`SELECT open_until FROM campaign_instance_circuits
WHERE instance_id = ? AND open_until > NOW()`, instanceID).Scan(&circuit).Error; err != nil {
		return nil, err
	}
	if !circuit.OpenUntil.IsZero() {
		for _, campaignID := range campaignIDs {
			snapshot := result[campaignID]
			if snapshot.RetryAt == nil || circuit.OpenUntil.After(*snapshot.RetryAt) {
				retryAt := circuit.OpenUntil.UTC()
				snapshot.RetryAt = &retryAt
				result[campaignID] = snapshot
			}
		}
	}
	return result, nil
}

func validCampaignStatus(status campaign_model.CampaignStatus) bool {
	switch status {
	case campaign_model.CampaignStatusDraft, campaign_model.CampaignStatusScheduled, campaign_model.CampaignStatusRunning,
		campaign_model.CampaignStatusPaused, campaign_model.CampaignStatusCompleted, campaign_model.CampaignStatusAborted, campaign_model.CampaignStatusFailed:
		return true
	default:
		return false
	}
}
