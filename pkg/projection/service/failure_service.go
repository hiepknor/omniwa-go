package projection_service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"github.com/google/uuid"
)

const projectionFailureCursorVersion = 1

var (
	ErrInvalidProjectionFailureCursor  = errors.New("invalid projection failure cursor")
	ErrInvalidProjectionFailureRequest = errors.New("invalid projection failure request")
)

type ProjectionFailureItem struct {
	InstanceID     string                              `json:"instanceId"`
	Resource       string                              `json:"resource"`
	EventKey       string                              `json:"eventKey"`
	EventType      string                              `json:"eventType"`
	OccurredAt     time.Time                           `json:"occurredAt"`
	IngestedAt     time.Time                           `json:"ingestedAt"`
	RetryCount     int                                 `json:"retryCount"`
	MaxAttempts    int                                 `json:"maxAttempts"`
	FailureClass   *projection_model.EventFailureClass `json:"failureClass,omitempty"`
	LastErrorCode  *string                             `json:"lastErrorCode,omitempty"`
	LastAttemptAt  *time.Time                          `json:"lastAttemptAt,omitempty"`
	DeadLetteredAt time.Time                           `json:"deadLetteredAt"`
}

type ProjectionFailurePage struct {
	Items      []ProjectionFailureItem `json:"items"`
	NextCursor string                  `json:"nextCursor,omitempty"`
}

type ProjectionFailureOperationResult struct {
	InstanceID string                         `json:"instanceId"`
	Resource   string                         `json:"resource"`
	EventKey   string                         `json:"eventKey"`
	Action     projection_model.FailureAction `json:"action"`
	OccurredAt time.Time                      `json:"occurredAt"`
}

type projectionFailureCursorEnvelope struct {
	Version        int       `json:"v"`
	Scope          string    `json:"scope"`
	DeadLetteredAt time.Time `json:"deadLetteredAt"`
	InstanceID     string    `json:"instanceId"`
	Resource       string    `json:"resource"`
	EventKey       string    `json:"eventKey"`
}

type FailureService struct {
	repository projection_repository.FailureRepository
	now        func() time.Time
}

func NewFailureService(repository projection_repository.FailureRepository) *FailureService {
	return &FailureService{repository: repository, now: time.Now}
}

func (s *FailureService) List(ctx context.Context, instanceID, resource string, limit int, encodedCursor string) (*ProjectionFailurePage, error) {
	if s == nil || s.repository == nil || limit < 1 || limit > 200 || len(resource) > 64 || (instanceID != "" && uuid.Validate(instanceID) != nil) {
		return nil, ErrInvalidProjectionFailureRequest
	}
	scope := failureScope(instanceID, resource)
	var cursor *projection_repository.FailureCursor
	if encodedCursor != "" {
		decoded, err := decodeFailureCursor(encodedCursor, scope)
		if err != nil {
			return nil, err
		}
		cursor = decoded
	}
	repositoryPage, err := s.repository.ListDeadLetters(ctx, instanceID, resource, limit, cursor)
	if err != nil {
		return nil, err
	}
	if repositoryPage == nil {
		return nil, errors.New("projection failure repository returned no page")
	}
	page := &ProjectionFailurePage{Items: make([]ProjectionFailureItem, len(repositoryPage.Items))}
	for index := range repositoryPage.Items {
		event := &repositoryPage.Items[index]
		deadLetteredAt := time.Time{}
		if event.DeadLetteredAt != nil {
			deadLetteredAt = event.DeadLetteredAt.UTC()
		}
		page.Items[index] = ProjectionFailureItem{
			InstanceID: event.InstanceID, Resource: event.Resource, EventKey: event.EventKey, EventType: event.EventType,
			OccurredAt: event.OccurredAt.UTC(), IngestedAt: event.IngestedAt.UTC(), RetryCount: event.RetryCount,
			MaxAttempts: event.MaxAttempts, FailureClass: event.FailureClass, LastErrorCode: event.LastErrorCode,
			LastAttemptAt: utcTimePointer(event.LastAttemptAt), DeadLetteredAt: deadLetteredAt,
		}
	}
	if repositoryPage.NextCursor != nil {
		page.NextCursor, err = encodeFailureCursor(scope, repositoryPage.NextCursor)
		if err != nil {
			return nil, err
		}
	}
	return page, nil
}

func (s *FailureService) Operate(ctx context.Context, instanceID, resource, eventKey string, action projection_model.FailureAction, reason, actorCredential, requestID string) (*ProjectionFailureOperationResult, error) {
	if s == nil || s.repository == nil || s.now == nil || strings.TrimSpace(actorCredential) == "" || requestID == "" {
		return nil, ErrInvalidProjectionFailureRequest
	}
	occurredAt := s.now().UTC()
	actorHash := sha256.Sum256([]byte("projection_failure_admin\x00" + actorCredential))
	operation := projection_repository.FailureOperation{
		InstanceID: instanceID, Resource: resource, EventKey: eventKey, Action: action, Reason: reason,
		ActorReferenceHash: hex.EncodeToString(actorHash[:]), RequestID: requestID, OccurredAt: occurredAt,
	}
	if err := s.repository.ApplyOperation(ctx, operation); err != nil {
		if errors.Is(err, projection_repository.ErrInvalidProjectionFailureOperation) {
			return nil, ErrInvalidProjectionFailureRequest
		}
		return nil, err
	}
	return &ProjectionFailureOperationResult{
		InstanceID: instanceID, Resource: resource, EventKey: eventKey, Action: action, OccurredAt: occurredAt,
	}, nil
}

func failureScope(instanceID, resource string) string {
	sum := sha256.Sum256([]byte(instanceID + "\x00" + resource))
	return hex.EncodeToString(sum[:])
}

func encodeFailureCursor(scope string, cursor *projection_repository.FailureCursor) (string, error) {
	value, err := json.Marshal(projectionFailureCursorEnvelope{
		Version: projectionFailureCursorVersion, Scope: scope, DeadLetteredAt: cursor.DeadLetteredAt.UTC(),
		InstanceID: cursor.InstanceID, Resource: cursor.Resource, EventKey: cursor.EventKey,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func decodeFailureCursor(encoded, scope string) (*projection_repository.FailureCursor, error) {
	value, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(value) > 2048 {
		return nil, ErrInvalidProjectionFailureCursor
	}
	var envelope projectionFailureCursorEnvelope
	if err := json.Unmarshal(value, &envelope); err != nil || envelope.Version != projectionFailureCursorVersion || envelope.Scope != scope ||
		envelope.DeadLetteredAt.IsZero() || envelope.InstanceID == "" || envelope.Resource == "" || envelope.EventKey == "" {
		return nil, ErrInvalidProjectionFailureCursor
	}
	return &projection_repository.FailureCursor{
		DeadLetteredAt: envelope.DeadLetteredAt.UTC(), InstanceID: envelope.InstanceID, Resource: envelope.Resource, EventKey: envelope.EventKey,
	}, nil
}
