package projection_repository

import (
	"context"
	"errors"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"gorm.io/gorm"
)

type DurableEventRepository interface {
	Append(context.Context, *projection_model.DurableEvent) error
	DeleteExpired(context.Context, time.Time, int) (int64, error)
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
