package projection_repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxClaimBatch       = 1000
	maxEventPayloadSize = 1 << 20
)

var ErrEventClaimLost = errors.New("projection event claim is no longer active")

type EventRepository interface {
	Enqueue(ctx context.Context, event *projection_model.Event) (bool, error)
	ClaimPending(ctx context.Context, limit int, leaseDuration time.Duration) ([]projection_model.Event, error)
	ClaimPendingFor(ctx context.Context, resource string, eventTypes []string, limit int, leaseDuration time.Duration) ([]projection_model.Event, error)
	MarkProcessed(ctx context.Context, event *projection_model.Event) error
	MarkRetry(ctx context.Context, event *projection_model.Event, failureClass projection_model.EventFailureClass, errorCode string, attemptedAt, retryAt time.Time) error
	MarkDeadLetter(ctx context.Context, event *projection_model.Event, failureClass projection_model.EventFailureClass, errorCode string, attemptedAt time.Time) error
}

type eventRepository struct {
	db  *gorm.DB
	now func() time.Time
}

func NewEventRepository(db *gorm.DB) EventRepository {
	return &eventRepository{db: db, now: time.Now}
}

func (r *eventRepository) Enqueue(ctx context.Context, event *projection_model.Event) (bool, error) {
	if err := validateEvent(event); err != nil {
		return false, err
	}
	now := r.now().UTC()
	event.OccurredAt = event.OccurredAt.UTC()
	event.IngestedAt = now
	event.AvailableAt = now
	event.Status = projection_model.EventStatusPending
	event.ClaimToken = nil
	event.LeaseUntil = nil
	event.ProcessedAt = nil
	event.RetryCount = 0
	event.LastErrorCode = nil
	event.LastAttemptAt = nil
	event.FailureClass = nil
	event.RetryPolicyVersion = projection_model.EventRetryPolicyVersion
	event.MaxAttempts = projection_model.DefaultEventMaxAttempts
	event.DeadLetteredAt = nil
	result := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(event)
	return result.RowsAffected == 1, result.Error
}

func (r *eventRepository) ClaimPending(ctx context.Context, limit int, leaseDuration time.Duration) ([]projection_model.Event, error) {
	return r.claimPending(ctx, "", nil, limit, leaseDuration)
}

func (r *eventRepository) ClaimPendingFor(ctx context.Context, resource string, eventTypes []string, limit int, leaseDuration time.Duration) ([]projection_model.Event, error) {
	if resource == "" || len(eventTypes) == 0 {
		return nil, errors.New("claim resource and event types are required")
	}
	return r.claimPending(ctx, resource, eventTypes, limit, leaseDuration)
}

func (r *eventRepository) claimPending(ctx context.Context, resource string, eventTypes []string, limit int, leaseDuration time.Duration) ([]projection_model.Event, error) {
	if limit <= 0 || limit > maxClaimBatch {
		return nil, fmt.Errorf("claim limit must be between 1 and %d", maxClaimBatch)
	}
	if leaseDuration <= 0 {
		return nil, errors.New("claim lease duration must be positive")
	}
	now := r.now().UTC()
	claimToken := uuid.NewString()
	leaseUntil := now.Add(leaseDuration)
	var events []projection_model.Event
	err := r.db.WithContext(ctx).Raw(`WITH candidates AS (
    SELECT instance_id, resource, event_key
    FROM projection_event_inbox
    WHERE (? = '' OR (resource = ? AND event_type = ANY(?)))
      AND ((status IN ('pending', 'failed') AND available_at <= ?)
        OR (status = 'processing' AND lease_until <= ?))
    ORDER BY occurred_at ASC, ingested_at ASC, event_key ASC
    FOR UPDATE SKIP LOCKED
    LIMIT ?
)
UPDATE projection_event_inbox AS inbox
SET status = 'processing', claim_token = ?, lease_until = ?
FROM candidates
WHERE inbox.instance_id = candidates.instance_id
  AND inbox.resource = candidates.resource
  AND inbox.event_key = candidates.event_key
RETURNING inbox.*`, resource, resource, pq.Array(eventTypes), now, now, limit, claimToken, leaseUntil).Scan(&events).Error
	return events, err
}

func (r *eventRepository) MarkProcessed(ctx context.Context, event *projection_model.Event) error {
	if err := validateClaimedEvent(event); err != nil {
		return err
	}
	now := r.now().UTC()
	result := r.claimedEventQuery(ctx, event).Updates(map[string]any{
		"status": projection_model.EventStatusProcessed, "processed_at": now,
		"claim_token": nil, "lease_until": nil, "last_error_code": nil,
		"last_attempt_at": now, "failure_class": nil, "dead_lettered_at": nil,
	})
	return claimResult(result)
}

func (r *eventRepository) MarkRetry(ctx context.Context, event *projection_model.Event, failureClass projection_model.EventFailureClass, errorCode string, attemptedAt, retryAt time.Time) error {
	if err := validateClaimedEvent(event); err != nil {
		return err
	}
	if failureClass != projection_model.EventFailureRetryable || !validFailureCode(errorCode) || attemptedAt.IsZero() || retryAt.IsZero() || retryAt.Before(attemptedAt) {
		return errors.New("retryable failure class, safe error code, attempt time, and retry time are required")
	}
	result := r.claimedEventQuery(ctx, event).Updates(map[string]any{
		"status": projection_model.EventStatusFailed, "available_at": retryAt.UTC(),
		"retry_count": gorm.Expr("retry_count + 1"), "last_error_code": errorCode,
		"last_attempt_at": attemptedAt.UTC(), "failure_class": failureClass,
		"claim_token": nil, "lease_until": nil, "dead_lettered_at": nil,
	})
	return claimResult(result)
}

func (r *eventRepository) MarkDeadLetter(ctx context.Context, event *projection_model.Event, failureClass projection_model.EventFailureClass, errorCode string, attemptedAt time.Time) error {
	if err := validateClaimedEvent(event); err != nil {
		return err
	}
	if (failureClass != projection_model.EventFailureRetryable && failureClass != projection_model.EventFailurePermanent) || !validFailureCode(errorCode) || attemptedAt.IsZero() {
		return errors.New("failure class, safe error code, and attempt time are required")
	}
	result := r.claimedEventQuery(ctx, event).Updates(map[string]any{
		"status": projection_model.EventStatusDeadLetter, "available_at": attemptedAt.UTC(),
		"retry_count": gorm.Expr("retry_count + 1"), "last_error_code": errorCode,
		"last_attempt_at": attemptedAt.UTC(), "failure_class": failureClass,
		"claim_token": nil, "lease_until": nil, "dead_lettered_at": attemptedAt.UTC(),
	})
	return claimResult(result)
}

func validFailureCode(code string) bool {
	return code != "" && len(code) <= 64
}

func (r *eventRepository) claimedEventQuery(ctx context.Context, event *projection_model.Event) *gorm.DB {
	return r.db.WithContext(ctx).Model(&projection_model.Event{}).Where(
		"instance_id = ? AND resource = ? AND event_key = ? AND status = ? AND claim_token = ?",
		event.InstanceID, event.Resource, event.EventKey, projection_model.EventStatusProcessing, *event.ClaimToken,
	)
}

func claimResult(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrEventClaimLost
	}
	return nil
}

func validateEvent(event *projection_model.Event) error {
	if event == nil || event.InstanceID == "" || event.Resource == "" || event.EventKey == "" || event.EntityKey == "" || event.EventType == "" || event.OccurredAt.IsZero() {
		return errors.New("projection event identity, type, and occurrence time are required")
	}
	if len(event.Resource) > 64 || len(event.EventKey) > 255 || len(event.EntityKey) > 255 || len(event.EventType) > 64 {
		return errors.New("projection event field exceeds storage limit")
	}
	if len(event.Payload) == 0 || len(event.Payload) > maxEventPayloadSize || !json.Valid(event.Payload) {
		return errors.New("projection event payload must be valid JSON")
	}
	return nil
}

func validateClaimedEvent(event *projection_model.Event) error {
	if event == nil || event.InstanceID == "" || event.Resource == "" || event.EventKey == "" || event.ClaimToken == nil || *event.ClaimToken == "" {
		return errors.New("claimed projection event is required")
	}
	return nil
}
