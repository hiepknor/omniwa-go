package projection_repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

type MessageRetentionRepository interface {
	DeleteBefore(context.Context, time.Time, int) (int64, error)
}

type messageRetentionRepository struct{ db *gorm.DB }

func NewMessageRetentionRepository(db *gorm.DB) MessageRetentionRepository {
	return &messageRetentionRepository{db: db}
}

// DeleteBefore hard-deletes a bounded batch. PostgreSQL cascades each deletion
// to projected_message_receipts through the versioned foreign key.
func (r *messageRetentionRepository) DeleteBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	if r == nil || r.db == nil || ctx == nil || cutoff.IsZero() || limit < 1 {
		return 0, errors.New("message retention repository parameters are invalid")
	}
	var deleted int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Exec(`WITH expired AS (
    SELECT instance_id, message_id
    FROM projected_messages
    WHERE provider_timestamp <= ?
    ORDER BY provider_timestamp ASC, instance_id ASC, message_id ASC
    LIMIT ?
    FOR UPDATE SKIP LOCKED
)
DELETE FROM projected_messages AS messages
USING expired
WHERE messages.instance_id = expired.instance_id
  AND messages.message_id = expired.message_id`, cutoff.UTC(), limit)
		if result.Error != nil {
			return result.Error
		}
		deleted = result.RowsAffected
		return tx.Exec(`WITH expired_events AS (
    SELECT instance_id, resource, event_key
    FROM projection_event_inbox
    WHERE resource = 'messages'
      AND event_type IN ('message', 'history_message', 'receipt')
      AND occurred_at <= ?
    ORDER BY occurred_at ASC, ingested_at ASC, event_key ASC
    LIMIT ?
    FOR UPDATE SKIP LOCKED
)
DELETE FROM projection_event_inbox AS events
USING expired_events
WHERE events.instance_id = expired_events.instance_id
  AND events.resource = expired_events.resource
  AND events.event_key = expired_events.event_key`, cutoff.UTC(), limit).Error
	})
	return deleted, err
}
