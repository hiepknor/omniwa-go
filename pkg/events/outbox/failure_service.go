package outbox

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

const deadLetterCursorVersion = 1

var (
	ErrInvalidDeadLetterCursor  = errors.New("invalid external event dead letter cursor")
	ErrInvalidDeadLetterRequest = errors.New("invalid external event dead letter request")
)

type DeadLetterItem struct {
	ID             string      `json:"id"`
	InstanceID     string      `json:"instanceId"`
	Transport      Transport   `json:"transport"`
	Destination    Destination `json:"destination"`
	AttemptCount   int         `json:"attemptCount"`
	MaxAttempts    int         `json:"maxAttempts"`
	LastErrorCode  *string     `json:"lastErrorCode,omitempty"`
	LastAttemptAt  *time.Time  `json:"lastAttemptAt,omitempty"`
	DeadLetteredAt time.Time   `json:"deadLetteredAt"`
	CreatedAt      time.Time   `json:"createdAt"`
}

type DeadLetterList struct {
	Items      []DeadLetterItem `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

type ReplayResult struct {
	ID         string    `json:"id"`
	Status     Status    `json:"status"`
	OccurredAt time.Time `json:"occurredAt"`
}

type deadLetterCursorEnvelope struct {
	Version        int       `json:"v"`
	Scope          string    `json:"scope"`
	DeadLetteredAt time.Time `json:"deadLetteredAt"`
	ID             string    `json:"id"`
}

type FailureService struct {
	repository FailureRepository
	now        func() time.Time
}

func NewFailureService(repository FailureRepository) *FailureService {
	return &FailureService{repository: repository, now: time.Now}
}

func (s *FailureService) List(ctx context.Context, instanceID string, transport Transport, limit int, cursor string) (*DeadLetterList, error) {
	if s == nil || s.repository == nil || limit < 1 || limit > 200 || (instanceID != "" && uuid.Validate(instanceID) != nil) ||
		(transport != "" && transport != TransportWebhook && transport != TransportRabbitMQ && transport != TransportNATS) {
		return nil, ErrInvalidDeadLetterRequest
	}
	scope := deadLetterScope(instanceID, transport)
	var decoded *DeadLetterCursor
	if cursor != "" {
		var err error
		decoded, err = decodeDeadLetterCursor(cursor, scope)
		if err != nil {
			return nil, err
		}
	}
	page, err := s.repository.ListDeadLetters(ctx, instanceID, transport, limit, decoded)
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, errors.New("external event failure repository returned no page")
	}
	result := &DeadLetterList{Items: make([]DeadLetterItem, len(page.Items))}
	for index, record := range page.Items {
		deadLetteredAt := time.Time{}
		if record.DeadLetteredAt != nil {
			deadLetteredAt = record.DeadLetteredAt.UTC()
		}
		result.Items[index] = DeadLetterItem{
			ID: record.ID, InstanceID: record.InstanceID, Transport: record.Transport, Destination: record.Destination,
			AttemptCount: record.AttemptCount, MaxAttempts: record.MaxAttempts, LastErrorCode: record.LastErrorCode,
			LastAttemptAt: utcPointer(record.LastAttemptAt), DeadLetteredAt: deadLetteredAt, CreatedAt: record.CreatedAt.UTC(),
		}
	}
	if page.NextCursor != nil {
		result.NextCursor, err = encodeDeadLetterCursor(scope, page.NextCursor)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *FailureService) Replay(ctx context.Context, deliveryID, reason, actorCredential, requestID string) (*ReplayResult, error) {
	if s == nil || s.repository == nil || s.now == nil || strings.TrimSpace(actorCredential) == "" || strings.TrimSpace(reason) == "" || requestID == "" {
		return nil, ErrInvalidDeadLetterRequest
	}
	when := s.now().UTC()
	actorHash := sha256.Sum256([]byte("external_event_replay_admin\x00" + actorCredential))
	err := s.repository.ReplayDeadLetter(ctx, ReplayOperation{
		DeliveryID: deliveryID, Reason: reason, ActorReferenceHash: hex.EncodeToString(actorHash[:]), RequestID: requestID, OccurredAt: when,
	})
	if errors.Is(err, ErrInvalidReplayOperation) {
		return nil, ErrInvalidDeadLetterRequest
	}
	if err != nil {
		return nil, err
	}
	return &ReplayResult{ID: deliveryID, Status: StatusPending, OccurredAt: when}, nil
}

func deadLetterScope(instanceID string, transport Transport) string {
	sum := sha256.Sum256([]byte(instanceID + "\x00" + string(transport)))
	return hex.EncodeToString(sum[:])
}

func encodeDeadLetterCursor(scope string, cursor *DeadLetterCursor) (string, error) {
	value, err := json.Marshal(deadLetterCursorEnvelope{Version: deadLetterCursorVersion, Scope: scope, DeadLetteredAt: cursor.DeadLetteredAt.UTC(), ID: cursor.ID})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func decodeDeadLetterCursor(encoded, scope string) (*DeadLetterCursor, error) {
	value, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(value) > 2048 {
		return nil, ErrInvalidDeadLetterCursor
	}
	var envelope deadLetterCursorEnvelope
	if err := json.Unmarshal(value, &envelope); err != nil || envelope.Version != deadLetterCursorVersion || envelope.Scope != scope ||
		envelope.DeadLetteredAt.IsZero() || uuid.Validate(envelope.ID) != nil {
		return nil, ErrInvalidDeadLetterCursor
	}
	return &DeadLetterCursor{DeadLetteredAt: envelope.DeadLetteredAt.UTC(), ID: envelope.ID}, nil
}

func utcPointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := value.UTC()
	return &result
}
