package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	eventpayload "github.com/evolution-foundation/evolution-go/pkg/events/payload"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const maxClaimBatch = 1000

var (
	ErrClaimLost    = errors.New("external event outbox claim is no longer active")
	safeErrorCode   = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	validTransports = map[Transport]struct{}{TransportWebhook: {}, TransportRabbitMQ: {}, TransportNATS: {}}
	validTargets    = map[Destination]struct{}{DestinationInstance: {}, DestinationGlobal: {}}
)

type Repository interface {
	Record(context.Context, *projection_model.DurableEvent, []Delivery) error
	ClaimReady(context.Context, int, time.Duration) ([]Delivery, error)
	MarkDelivered(context.Context, *Delivery) error
	MarkRetry(context.Context, *Delivery, string, time.Time) error
	MarkDeadLetter(context.Context, *Delivery, string) error
}

type repository struct {
	db  *gorm.DB
	now func() time.Time
}

func NewRepository(db *gorm.DB) Repository {
	return &repository{db: db, now: time.Now}
}

// Record persists the normalized durable-history row and every external
// delivery in one transaction. Callers must finish event assembly before this
// boundary; no network operation belongs in the transaction.
func (r *repository) Record(ctx context.Context, event *projection_model.DurableEvent, deliveries []Delivery) error {
	if r == nil || r.db == nil || r.now == nil || ctx == nil {
		return errors.New("external event outbox repository and context are required")
	}
	if err := validateDurableEvent(event); err != nil {
		return err
	}
	now := r.now().UTC()
	for index := range deliveries {
		if err := prepareDelivery(&deliveries[index], event, now); err != nil {
			return fmt.Errorf("prepare external delivery %d: %w", index, err)
		}
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(event).Error; err != nil {
			return err
		}
		if len(deliveries) == 0 {
			return nil
		}
		return tx.Create(&deliveries).Error
	})
}

func (r *repository) ClaimReady(ctx context.Context, limit int, leaseDuration time.Duration) ([]Delivery, error) {
	if r == nil || r.db == nil || r.now == nil || ctx == nil {
		return nil, errors.New("external event outbox repository and context are required")
	}
	if limit < 1 || limit > maxClaimBatch {
		return nil, fmt.Errorf("claim limit must be between 1 and %d", maxClaimBatch)
	}
	if leaseDuration <= 0 {
		return nil, errors.New("claim lease duration must be positive")
	}
	now := r.now().UTC()
	claimToken := uuid.NewString()
	leaseUntil := now.Add(leaseDuration)
	var deliveries []Delivery
	err := r.db.WithContext(ctx).Raw(`WITH candidates AS (
    SELECT id
    FROM external_event_outbox
    WHERE (status = 'pending' AND available_at <= ?)
       OR (status = 'processing' AND lease_until <= ?)
    ORDER BY available_at ASC, created_at ASC, id ASC
    FOR UPDATE SKIP LOCKED
    LIMIT ?
)
UPDATE external_event_outbox AS outbox
SET status = 'processing', claim_token = ?, lease_until = ?, updated_at = ?
FROM candidates
WHERE outbox.id = candidates.id
RETURNING outbox.*`, now, now, limit, claimToken, leaseUntil, now).Scan(&deliveries).Error
	return deliveries, err
}

func (r *repository) MarkDelivered(ctx context.Context, delivery *Delivery) error {
	if err := r.validateClaimMutation(ctx, delivery); err != nil {
		return err
	}
	now := r.now().UTC()
	return claimResult(r.claimed(ctx, delivery).Updates(map[string]any{
		"status": StatusDelivered, "delivered_at": now, "last_attempt_at": now,
		"last_error_code": nil, "claim_token": nil, "lease_until": nil, "payload": json.RawMessage(`{}`), "updated_at": now,
	}))
}

func (r *repository) MarkRetry(ctx context.Context, delivery *Delivery, errorCode string, retryAt time.Time) error {
	if err := r.validateClaimMutation(ctx, delivery); err != nil {
		return err
	}
	now := r.now().UTC()
	if !safeErrorCode.MatchString(errorCode) || !retryAt.After(now) {
		return errors.New("safe error code and future retry time are required")
	}
	if delivery.AttemptCount+1 >= delivery.MaxAttempts {
		return errors.New("delivery attempt budget is exhausted")
	}
	return claimResult(r.claimed(ctx, delivery).Updates(map[string]any{
		"status": StatusPending, "available_at": retryAt.UTC(), "attempt_count": gorm.Expr("attempt_count + 1"),
		"last_attempt_at": now, "last_error_code": errorCode, "claim_token": nil, "lease_until": nil, "updated_at": now,
	}))
}

func (r *repository) MarkDeadLetter(ctx context.Context, delivery *Delivery, errorCode string) error {
	if err := r.validateClaimMutation(ctx, delivery); err != nil {
		return err
	}
	if !safeErrorCode.MatchString(errorCode) {
		return errors.New("safe error code is required")
	}
	now := r.now().UTC()
	return claimResult(r.claimed(ctx, delivery).Updates(map[string]any{
		"status": StatusDeadLetter, "dead_lettered_at": now, "attempt_count": gorm.Expr("attempt_count + 1"),
		"last_attempt_at": now, "last_error_code": errorCode, "claim_token": nil, "lease_until": nil, "updated_at": now,
	}))
}

func (r *repository) validateClaimMutation(ctx context.Context, delivery *Delivery) error {
	if r == nil || r.db == nil || r.now == nil || ctx == nil || delivery == nil || uuid.Validate(delivery.ID) != nil ||
		delivery.Status != StatusProcessing || delivery.ClaimToken == nil || uuid.Validate(*delivery.ClaimToken) != nil {
		return errors.New("active external event outbox claim is required")
	}
	return nil
}

func (r *repository) claimed(ctx context.Context, delivery *Delivery) *gorm.DB {
	return r.db.WithContext(ctx).Model(&Delivery{}).Where(
		"id = ? AND status = ? AND claim_token = ?", delivery.ID, StatusProcessing, *delivery.ClaimToken,
	)
}

func claimResult(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrClaimLost
	}
	return nil
}

func prepareDelivery(delivery *Delivery, event *projection_model.DurableEvent, now time.Time) error {
	if delivery == nil || uuid.Validate(delivery.ID) != nil || delivery.RoutingKey == "" || len(delivery.RoutingKey) > 255 {
		return errors.New("delivery identity and routing key are required")
	}
	if _, ok := validTransports[delivery.Transport]; !ok {
		return errors.New("delivery transport is unsupported")
	}
	if _, ok := validTargets[delivery.Destination]; !ok {
		return errors.New("delivery destination is unsupported")
	}
	safePayload, err := eventpayload.SanitizeJSON(delivery.Payload)
	if err != nil || len(safePayload) > MaxPayloadBytes {
		return errors.New("delivery payload must be a bounded safe JSON object")
	}
	delivery.DurableEventID = event.ID
	delivery.InstanceID = event.InstanceID
	delivery.Payload = safePayload
	delivery.Status = StatusPending
	delivery.AvailableAt = now
	delivery.ClaimToken = nil
	delivery.LeaseUntil = nil
	delivery.AttemptCount = 0
	delivery.RetryPolicyVersion = RetryPolicyVersion
	if delivery.MaxAttempts == 0 {
		delivery.MaxAttempts = DefaultMaxAttempts
	}
	if delivery.MaxAttempts < 1 {
		return errors.New("delivery attempt budget must be positive")
	}
	delivery.LastAttemptAt = nil
	delivery.LastErrorCode = nil
	delivery.DeliveredAt = nil
	delivery.DeadLetteredAt = nil
	delivery.CreatedAt = now
	delivery.UpdatedAt = now
	return nil
}

func validateDurableEvent(event *projection_model.DurableEvent) error {
	if event == nil || uuid.Validate(event.ID) != nil || uuid.Validate(event.InstanceID) != nil || event.Type == "" || len(event.Type) > 64 ||
		event.OccurredAt.IsZero() || event.IngestedAt.IsZero() || event.ExpiresAt.IsZero() || len(event.Summary) == 0 || !json.Valid(event.Summary) {
		return errors.New("complete normalized durable event is required")
	}
	return nil
}
