package outbox

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	eventpayload "github.com/evolution-foundation/evolution-go/pkg/events/payload"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const maxClaimBatch = 1000

var (
	ErrClaimLost        = errors.New("external event outbox claim is no longer active")
	safeErrorCode       = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	safeReplayRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{16,64}$`)
	validTransports     = map[Transport]struct{}{TransportWebhook: {}, TransportRabbitMQ: {}, TransportNATS: {}}
	validTargets        = map[Destination]struct{}{DestinationInstance: {}, DestinationGlobal: {}}
)

type Repository interface {
	Record(context.Context, *projection_model.DurableEvent, []Delivery) error
	ClaimReady(context.Context, int, time.Duration) ([]Delivery, error)
	MarkDelivered(context.Context, *Delivery) error
	MarkRetry(context.Context, *Delivery, string, time.Time) error
	MarkDeadLetter(context.Context, *Delivery, string) error
	Health(context.Context) (Health, error)
}

type FailureRepository interface {
	ListDeadLetters(context.Context, string, Transport, int, *DeadLetterCursor) (*DeadLetterPage, error)
	ReplayDeadLetter(context.Context, ReplayOperation) error
}

type FullRepository interface {
	Repository
	FailureRepository
}

var (
	ErrDeadLetterNotFound      = errors.New("external event dead letter was not found")
	ErrDeadLetterNotActionable = errors.New("external event dead letter is no longer actionable")
	ErrInvalidReplayOperation  = errors.New("valid external event replay operation is required")
)

type DeadLetterCursor struct {
	DeadLetteredAt time.Time
	ID             string
}

type DeadLetterRecord struct {
	ID             string
	InstanceID     string
	Transport      Transport
	Destination    Destination
	AttemptCount   int
	MaxAttempts    int
	LastErrorCode  *string
	LastAttemptAt  *time.Time
	DeadLetteredAt *time.Time
	CreatedAt      time.Time
}

type DeadLetterPage struct {
	Items      []DeadLetterRecord
	NextCursor *DeadLetterCursor
}

type ReplayOperation struct {
	DeliveryID         string
	Reason             string
	ActorReferenceHash string
	RequestID          string
	OccurredAt         time.Time
}

// Health is an aggregate-only operational view. It deliberately excludes
// instance identifiers, routing keys, and payload data.
type Health struct {
	Pending          int64
	Processing       int64
	DeadLetter       int64
	OldestPendingAge time.Duration
}

type repository struct {
	db  *gorm.DB
	now func() time.Time
}

func NewRepository(db *gorm.DB) FullRepository {
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
	err := r.db.WithContext(ctx).Raw(`WITH exhausted AS (
	UPDATE external_event_outbox
	SET status = 'dead_letter', dead_lettered_at = ?, last_attempt_at = ?,
	    last_error_code = 'attempt_budget_exhausted', claim_token = NULL,
	    lease_until = NULL, updated_at = ?
	WHERE attempt_count >= max_attempts
	  AND ((status = 'pending' AND available_at <= ?)
	    OR (status = 'processing' AND lease_until <= ?))
	RETURNING id
), candidates AS (
	    SELECT id
	    FROM external_event_outbox
	    WHERE attempt_count < max_attempts
	      AND ((status = 'pending' AND available_at <= ?)
	       OR (status = 'processing' AND lease_until <= ?))
	    ORDER BY available_at ASC, created_at ASC, id ASC
	    FOR UPDATE SKIP LOCKED
	    LIMIT ?
)
UPDATE external_event_outbox AS outbox
SET status = 'processing', claim_token = ?, lease_until = ?,
    attempt_count = outbox.attempt_count + 1, updated_at = ?
FROM candidates
WHERE outbox.id = candidates.id
RETURNING outbox.*`, now, now, now, now, now, now, now, limit, claimToken, leaseUntil, now).Scan(&deliveries).Error
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
	if delivery.AttemptCount >= delivery.MaxAttempts {
		return errors.New("delivery attempt budget is exhausted")
	}
	return claimResult(r.claimed(ctx, delivery).Updates(map[string]any{
		"status": StatusPending, "available_at": retryAt.UTC(),
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
		"status": StatusDeadLetter, "dead_lettered_at": now,
		"last_attempt_at": now, "last_error_code": errorCode, "claim_token": nil, "lease_until": nil, "updated_at": now,
	}))
}

func (r *repository) Health(ctx context.Context) (Health, error) {
	if r == nil || r.db == nil || r.now == nil || ctx == nil {
		return Health{}, errors.New("external event outbox repository and context are required")
	}
	var row struct {
		Pending         int64
		Processing      int64
		DeadLetter      int64
		OldestPendingAt *time.Time
	}
	err := r.db.WithContext(ctx).Raw(`SELECT
	COUNT(*) FILTER (WHERE status = 'pending') AS pending,
	COUNT(*) FILTER (WHERE status = 'processing') AS processing,
	COUNT(*) FILTER (WHERE status = 'dead_letter') AS dead_letter,
	MIN(created_at) FILTER (WHERE status = 'pending') AS oldest_pending_at
FROM external_event_outbox`).Scan(&row).Error
	if err != nil {
		return Health{}, err
	}
	health := Health{Pending: row.Pending, Processing: row.Processing, DeadLetter: row.DeadLetter}
	if row.OldestPendingAt != nil {
		health.OldestPendingAge = r.now().UTC().Sub(row.OldestPendingAt.UTC())
		if health.OldestPendingAge < 0 {
			health.OldestPendingAge = 0
		}
	}
	return health, nil
}

func (r *repository) ListDeadLetters(ctx context.Context, instanceID string, transport Transport, limit int, cursor *DeadLetterCursor) (*DeadLetterPage, error) {
	if r == nil || r.db == nil || ctx == nil || limit < 1 || limit > 200 || (instanceID != "" && uuid.Validate(instanceID) != nil) ||
		(transport != "" && transport != TransportWebhook && transport != TransportRabbitMQ && transport != TransportNATS) {
		return nil, errors.New("valid external event dead letter list parameters are required")
	}
	query := r.db.WithContext(ctx).Model(&Delivery{}).
		Select("id", "instance_id", "transport", "destination", "attempt_count", "max_attempts", "last_error_code", "last_attempt_at", "dead_lettered_at", "created_at").
		Where("status = ?", StatusDeadLetter)
	if instanceID != "" {
		query = query.Where("instance_id = ?", instanceID)
	}
	if transport != "" {
		query = query.Where("transport = ?", transport)
	}
	if cursor != nil {
		if cursor.DeadLetteredAt.IsZero() || uuid.Validate(cursor.ID) != nil {
			return nil, errors.New("valid external event dead letter cursor is required")
		}
		query = query.Where("(dead_lettered_at, id) < (?, ?)", cursor.DeadLetteredAt.UTC(), cursor.ID)
	}
	var records []DeadLetterRecord
	if err := query.Order("dead_lettered_at DESC, id DESC").Limit(limit + 1).Scan(&records).Error; err != nil {
		return nil, err
	}
	page := &DeadLetterPage{Items: records}
	if len(records) > limit {
		last := records[limit-1]
		page.Items = records[:limit]
		if last.DeadLetteredAt != nil {
			page.NextCursor = &DeadLetterCursor{DeadLetteredAt: last.DeadLetteredAt.UTC(), ID: last.ID}
		}
	}
	return page, nil
}

func (r *repository) ReplayDeadLetter(ctx context.Context, operation ReplayOperation) error {
	operation.Reason = strings.TrimSpace(operation.Reason)
	if r == nil || r.db == nil || ctx == nil || !validReplayOperation(operation) {
		return ErrInvalidReplayOperation
	}
	operation.OccurredAt = operation.OccurredAt.UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&Delivery{}).Where("id = ? AND status = ?", operation.DeliveryID, StatusDeadLetter).Updates(map[string]any{
			"status": StatusPending, "available_at": operation.OccurredAt, "attempt_count": 0,
			"claim_token": nil, "lease_until": nil, "last_attempt_at": nil, "last_error_code": nil,
			"dead_lettered_at": nil, "delivered_at": nil, "updated_at": operation.OccurredAt,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			var count int64
			if err := tx.Model(&Delivery{}).Where("id = ?", operation.DeliveryID).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return ErrDeadLetterNotFound
			}
			return ErrDeadLetterNotActionable
		}
		return tx.Create(&ReplayAudit{
			ID: uuid.NewString(), DeliveryID: operation.DeliveryID, Reason: operation.Reason,
			ActorReferenceHash: operation.ActorReferenceHash, RequestID: operation.RequestID, OccurredAt: operation.OccurredAt,
		}).Error
	})
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

func validReplayOperation(operation ReplayOperation) bool {
	if uuid.Validate(operation.DeliveryID) != nil || operation.OccurredAt.IsZero() || !safeReplayRequestID.MatchString(operation.RequestID) ||
		operation.Reason == "" || !utf8.ValidString(operation.Reason) || utf8.RuneCountInString(operation.Reason) > 500 {
		return false
	}
	for _, value := range operation.Reason {
		if unicode.IsControl(value) {
			return false
		}
	}
	decoded, err := hex.DecodeString(operation.ActorReferenceHash)
	return err == nil && len(decoded) == 32 && operation.ActorReferenceHash == strings.ToLower(operation.ActorReferenceHash)
}
