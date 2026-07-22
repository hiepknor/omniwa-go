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
	var rows []struct {
		Status campaign_model.RecipientStatus
		Count  int64
	}
	if err := r.db.WithContext(ctx).Model(&campaign_model.Recipient{}).Select("status, count(*) AS count").
		Where("instance_id = ? AND campaign_id = ?", instanceID, campaignID).Group("status").Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[campaign_model.RecipientStatus]int64, len(rows))
	for _, row := range rows {
		counts[row.Status] = row.Count
	}
	return counts, nil
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
