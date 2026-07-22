package projection_service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
)

const failureTestInstanceID = "00000000-0000-0000-0000-000000000001"

type failureRepositoryStub struct {
	page      *projection_repository.FailurePage
	listErr   error
	operation projection_repository.FailureOperation
	applyErr  error
}

func (r *failureRepositoryStub) ListDeadLetters(_ context.Context, _, _ string, _ int, cursor *projection_repository.FailureCursor) (*projection_repository.FailurePage, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	if cursor != nil {
		return &projection_repository.FailurePage{Items: []projection_repository.FailureRecord{}}, nil
	}
	return r.page, nil
}

func (r *failureRepositoryStub) ApplyOperation(_ context.Context, operation projection_repository.FailureOperation) error {
	r.operation = operation
	return r.applyErr
}

func TestFailureServiceReturnsSafeSummaryAndScopeBoundCursor(t *testing.T) {
	deadLetteredAt := time.Unix(300, 0).UTC()
	failureClass := projection_model.EventFailurePermanent
	errorCode := "invalid_payload"
	repository := &failureRepositoryStub{page: &projection_repository.FailurePage{
		Items: []projection_repository.FailureRecord{{
			InstanceID: failureTestInstanceID, Resource: "groups", EventKey: "event-1",
			EventType: "group_info", OccurredAt: time.Unix(100, 0), IngestedAt: time.Unix(200, 0),
			RetryCount: 8, MaxAttempts: 8,
			FailureClass: &failureClass, LastErrorCode: &errorCode, DeadLetteredAt: &deadLetteredAt,
		}},
		NextCursor: &projection_repository.FailureCursor{DeadLetteredAt: deadLetteredAt, InstanceID: failureTestInstanceID, Resource: "groups", EventKey: "event-1"},
	}}
	service := NewFailureService(repository)
	page, err := service.List(context.Background(), failureTestInstanceID, "groups", 50, "")
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" || page.Items[0].LastErrorCode == nil || *page.Items[0].LastErrorCode != errorCode {
		t.Fatalf("List() = %#v, %v", page, err)
	}
	serialized, err := json.Marshal(page)
	if err != nil || strings.Contains(string(serialized), "must-not-leak") || strings.Contains(string(serialized), "15550001111") || strings.Contains(string(serialized), `"entityKey"`) || strings.Contains(string(serialized), `"payload"`) {
		t.Fatalf("unsafe failure summary = %s, %v", serialized, err)
	}
	if _, err := service.List(context.Background(), "00000000-0000-0000-0000-000000000002", "groups", 50, page.NextCursor); !errors.Is(err, ErrInvalidProjectionFailureCursor) {
		t.Fatalf("cross-scope cursor error = %v", err)
	}
	if _, err := service.List(context.Background(), failureTestInstanceID, "groups", 50, page.NextCursor); err != nil {
		t.Fatalf("same-scope cursor rejected: %v", err)
	}
}

func TestFailureServiceHashesActorAndReturnsSafeOperationResult(t *testing.T) {
	repository := &failureRepositoryStub{}
	service := NewFailureService(repository)
	service.now = func() time.Time { return time.Unix(500, 0) }
	result, err := service.Operate(context.Background(), failureTestInstanceID, "groups", "event-1", projection_model.FailureActionReplay, "fixed projector schema", "admin-secret", "request-identity-0001")
	if err != nil || result.Action != projection_model.FailureActionReplay || !result.OccurredAt.Equal(time.Unix(500, 0)) {
		t.Fatalf("Operate() = %#v, %v", result, err)
	}
	if repository.operation.ActorReferenceHash == "admin-secret" || len(repository.operation.ActorReferenceHash) != 64 || repository.operation.Reason != "fixed projector schema" || repository.operation.RequestID != "request-identity-0001" {
		t.Fatalf("unsafe or incomplete operation = %#v", repository.operation)
	}
	serialized, _ := json.Marshal(result)
	if strings.Contains(string(serialized), "admin-secret") || strings.Contains(string(serialized), "fixed projector schema") {
		t.Fatalf("operation response leaked audit material: %s", serialized)
	}
}

func TestFailureServiceMapsInvalidRepositoryOperation(t *testing.T) {
	repository := &failureRepositoryStub{applyErr: projection_repository.ErrInvalidProjectionFailureOperation}
	service := NewFailureService(repository)
	if _, err := service.Operate(context.Background(), "", "groups", "event-1", projection_model.FailureActionDiscard, "reason", "admin", "request-identity-0001"); !errors.Is(err, ErrInvalidProjectionFailureRequest) {
		t.Fatalf("Operate() error = %v", err)
	}
}
