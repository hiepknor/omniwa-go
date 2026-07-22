package projection_repository

import (
	"context"
	"errors"
	"strings"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"gorm.io/gorm"
)

type DurableEventRepository interface {
	Append(context.Context, *projection_model.DurableEvent) error
	List(context.Context, string, string, int, *DurableEventCursor) (*DurableEventPage, error)
	DeleteExpired(context.Context, time.Time, int) (int64, error)
}

const maxDurableEventPageSize = 200

type DurableEventCursor struct {
	OccurredAt time.Time
	ID         string
}

type DurableEventPage struct {
	Items      []projection_model.DurableEvent
	NextCursor *DurableEventCursor
}

// DeleteExpired hard-deletes a bounded batch ordered by expiry and durable ID.
// SKIP LOCKED allows multiple application replicas to sweep without blocking
// one another while preserving a strict upper bound per transaction.
func (r *durableEventRepository) DeleteExpired(ctx context.Context, now time.Time, limit int) (int64, error) {
	if r == nil || r.db == nil || ctx == nil || now.IsZero() || limit < 1 {
		return 0, errors.New("durable event retention repository parameters are invalid")
	}
	result := r.db.WithContext(ctx).Exec(`WITH expired AS (
    SELECT id
    FROM durable_events
    WHERE expires_at <= ?
    ORDER BY expires_at ASC, id ASC
    LIMIT ?
    FOR UPDATE SKIP LOCKED
)
DELETE FROM durable_events AS events
USING expired
WHERE events.id = expired.id`, now.UTC(), limit)
	return result.RowsAffected, result.Error
}

type durableEventRepository struct{ db *gorm.DB }

func NewDurableEventRepository(db *gorm.DB) DurableEventRepository {
	return &durableEventRepository{db: db}
}

func (r *durableEventRepository) Append(ctx context.Context, event *projection_model.DurableEvent) error {
	if r == nil || r.db == nil || ctx == nil || event == nil || event.ID == "" || event.InstanceID == "" || event.Type == "" ||
		event.OccurredAt.IsZero() || event.IngestedAt.IsZero() || event.ExpiresAt.IsZero() || len(event.Summary) == 0 {
		return errors.New("durable event is incomplete")
	}
	return r.db.WithContext(ctx).Create(event).Error
}

func (r *durableEventRepository) List(ctx context.Context, instanceID, eventType string, limit int, cursor *DurableEventCursor) (*DurableEventPage, error) {
	eventType = strings.TrimSpace(eventType)
	if r == nil || r.db == nil || ctx == nil || instanceID == "" || limit < 1 || limit > maxDurableEventPageSize || len(eventType) > 64 ||
		(cursor != nil && (cursor.ID == "" || cursor.OccurredAt.IsZero())) {
		return nil, errors.New("valid durable event list parameters are required")
	}
	query := r.db.WithContext(ctx).Where("instance_id = ?", instanceID)
	if eventType != "" {
		query = query.Where("event_type = ?", eventType)
	}
	if cursor != nil {
		at := cursor.OccurredAt.UTC()
		query = query.Where("occurred_at < ? OR (occurred_at = ? AND id < ?)", at, at, cursor.ID)
	}
	var events []projection_model.DurableEvent
	if err := query.Order("occurred_at DESC, id DESC").Limit(limit + 1).Find(&events).Error; err != nil {
		return nil, err
	}
	page := &DurableEventPage{Items: events}
	if len(events) > limit {
		page.Items = events[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = &DurableEventCursor{OccurredAt: last.OccurredAt, ID: last.ID}
	}
	return page, nil
}
