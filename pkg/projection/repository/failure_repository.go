package projection_repository

import (
	"context"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const maxFailurePageSize = 200

var safeFailureRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{16,64}$`)

var (
	ErrProjectionFailureNotFound         = errors.New("projection failure was not found")
	ErrProjectionFailureNotActionable    = errors.New("projection failure is no longer actionable")
	ErrInvalidProjectionFailureOperation = errors.New("valid projection failure operation is required")
)

type FailureCursor struct {
	DeadLetteredAt time.Time
	InstanceID     string
	Resource       string
	EventKey       string
}

type FailurePage struct {
	Items      []FailureRecord
	NextCursor *FailureCursor
}

type FailureRecord struct {
	InstanceID     string
	Resource       string
	EventKey       string
	EventType      string
	OccurredAt     time.Time
	IngestedAt     time.Time
	RetryCount     int
	MaxAttempts    int
	FailureClass   *projection_model.EventFailureClass
	LastErrorCode  *string
	LastAttemptAt  *time.Time
	DeadLetteredAt *time.Time
}

type FailureOperation struct {
	InstanceID         string
	Resource           string
	EventKey           string
	Action             projection_model.FailureAction
	Reason             string
	ActorReferenceHash string
	RequestID          string
	OccurredAt         time.Time
}

type FailureRepository interface {
	ListDeadLetters(context.Context, string, string, int, *FailureCursor) (*FailurePage, error)
	ApplyOperation(context.Context, FailureOperation) error
}

type failureRepository struct{ db *gorm.DB }

func NewFailureRepository(db *gorm.DB) FailureRepository { return &failureRepository{db: db} }

func (r *failureRepository) ListDeadLetters(ctx context.Context, instanceID, resource string, limit int, cursor *FailureCursor) (*FailurePage, error) {
	if r == nil || r.db == nil || limit < 1 || limit > maxFailurePageSize || (instanceID != "" && uuid.Validate(instanceID) != nil) {
		return nil, errors.New("valid projection failure list parameters are required")
	}
	query := r.db.WithContext(ctx).Model(&projection_model.Event{}).
		Select("instance_id", "resource", "event_key", "event_type", "occurred_at", "ingested_at", "retry_count", "max_attempts", "failure_class", "last_error_code", "last_attempt_at", "dead_lettered_at").
		Where("status = ?", projection_model.EventStatusDeadLetter)
	if instanceID != "" {
		query = query.Where("instance_id = ?", instanceID)
	}
	if resource != "" {
		query = query.Where("resource = ?", resource)
	}
	if cursor != nil {
		if cursor.DeadLetteredAt.IsZero() || cursor.InstanceID == "" || cursor.Resource == "" || cursor.EventKey == "" {
			return nil, errors.New("valid projection failure cursor is required")
		}
		query = query.Where(
			"(dead_lettered_at, instance_id, resource, event_key) < (?, ?, ?, ?)",
			cursor.DeadLetteredAt.UTC(), cursor.InstanceID, cursor.Resource, cursor.EventKey,
		)
	}
	var events []FailureRecord
	if err := query.Order("dead_lettered_at DESC, instance_id DESC, resource DESC, event_key DESC").Limit(limit + 1).Scan(&events).Error; err != nil {
		return nil, err
	}
	page := &FailurePage{Items: events}
	if len(events) > limit {
		last := events[limit-1]
		page.Items = events[:limit]
		page.NextCursor = &FailureCursor{
			DeadLetteredAt: *last.DeadLetteredAt, InstanceID: last.InstanceID, Resource: last.Resource, EventKey: last.EventKey,
		}
	}
	return page, nil
}

func (r *failureRepository) ApplyOperation(ctx context.Context, operation FailureOperation) error {
	operation.Reason = strings.TrimSpace(operation.Reason)
	if r == nil || r.db == nil || !validFailureOperation(operation) {
		return ErrInvalidProjectionFailureOperation
	}
	operation.OccurredAt = operation.OccurredAt.UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{
			"claim_token": nil, "lease_until": nil, "dead_lettered_at": nil,
		}
		switch operation.Action {
		case projection_model.FailureActionReplay:
			updates["status"] = projection_model.EventStatusPending
			updates["available_at"] = operation.OccurredAt
			updates["retry_count"] = 0
			updates["last_error_code"] = nil
			updates["last_attempt_at"] = nil
			updates["failure_class"] = nil
			updates["discarded_at"] = nil
		case projection_model.FailureActionDiscard:
			updates["status"] = projection_model.EventStatusProcessed
			updates["processed_at"] = nil
			updates["discarded_at"] = operation.OccurredAt
		}
		result := tx.Model(&projection_model.Event{}).
			Where("instance_id = ? AND resource = ? AND event_key = ? AND status = ?", operation.InstanceID, operation.Resource, operation.EventKey, projection_model.EventStatusDeadLetter).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			var count int64
			if err := tx.Model(&projection_model.Event{}).Where("instance_id = ? AND resource = ? AND event_key = ?", operation.InstanceID, operation.Resource, operation.EventKey).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return ErrProjectionFailureNotFound
			}
			return ErrProjectionFailureNotActionable
		}
		audit := projection_model.FailureAudit{
			ID: uuid.NewString(), InstanceID: operation.InstanceID, Resource: operation.Resource, EventKey: operation.EventKey,
			Action: operation.Action, Reason: operation.Reason, ActorReferenceHash: operation.ActorReferenceHash,
			RequestID: operation.RequestID, OccurredAt: operation.OccurredAt,
		}
		return tx.Create(&audit).Error
	})
}

func validFailureOperation(operation FailureOperation) bool {
	if uuid.Validate(operation.InstanceID) != nil || operation.Resource == "" || operation.EventKey == "" || operation.OccurredAt.IsZero() ||
		len(operation.Resource) > 64 || len(operation.EventKey) > 255 || !validFailureReason(operation.Reason) {
		return false
	}
	if operation.Action != projection_model.FailureActionReplay && operation.Action != projection_model.FailureActionDiscard {
		return false
	}
	decoded, err := hex.DecodeString(operation.ActorReferenceHash)
	return err == nil && len(decoded) == 32 && operation.ActorReferenceHash == strings.ToLower(operation.ActorReferenceHash) && safeFailureRequestID.MatchString(operation.RequestID)
}

func validFailureReason(reason string) bool {
	if reason == "" || !utf8.ValidString(reason) || utf8.RuneCountInString(reason) > 500 {
		return false
	}
	for _, value := range reason {
		if unicode.IsControl(value) {
			return false
		}
	}
	return true
}
