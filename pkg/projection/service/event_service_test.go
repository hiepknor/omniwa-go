package projection_service

import (
	"context"
	"errors"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
)

type memoryEventRepository struct {
	events           []projection_model.Event
	processed        int
	failed           int
	deadLettered     int
	lastErrorCode    string
	lastFailureClass projection_model.EventFailureClass
	retryAt          time.Time
}

func (r *memoryEventRepository) Enqueue(_ context.Context, event *projection_model.Event) (bool, error) {
	for _, stored := range r.events {
		if stored.InstanceID == event.InstanceID && stored.Resource == event.Resource && stored.EventKey == event.EventKey {
			return false, nil
		}
	}
	r.events = append(r.events, *event)
	return true, nil
}

func (r *memoryEventRepository) ClaimPending(_ context.Context, limit int, _ time.Duration) ([]projection_model.Event, error) {
	if limit < len(r.events) {
		return append([]projection_model.Event(nil), r.events[:limit]...), nil
	}
	return append([]projection_model.Event(nil), r.events...), nil
}

func (r *memoryEventRepository) ClaimPendingFor(ctx context.Context, _ string, _ []string, limit int, lease time.Duration) ([]projection_model.Event, error) {
	return r.ClaimPending(ctx, limit, lease)
}

func (r *memoryEventRepository) MarkProcessed(_ context.Context, _ *projection_model.Event) error {
	r.processed++
	return nil
}

func (r *memoryEventRepository) MarkRetry(_ context.Context, _ *projection_model.Event, failureClass projection_model.EventFailureClass, errorCode string, _ time.Time, retryAt time.Time) error {
	r.failed++
	r.lastErrorCode = errorCode
	r.lastFailureClass = failureClass
	r.retryAt = retryAt
	return nil
}

func (r *memoryEventRepository) MarkDeadLetter(_ context.Context, _ *projection_model.Event, failureClass projection_model.EventFailureClass, errorCode string, _ time.Time) error {
	r.deadLettered++
	r.lastErrorCode = errorCode
	r.lastFailureClass = failureClass
	return nil
}

func TestEventServiceIngestIsIdempotent(t *testing.T) {
	repository := &memoryEventRepository{}
	service := NewEventService(repository, time.Minute, time.Second)
	event := &projection_model.Event{InstanceID: "instance-a", Resource: "groups", EventKey: "event-1"}
	inserted, err := service.Ingest(context.Background(), event)
	if err != nil || !inserted {
		t.Fatalf("first Ingest() = %v, %v", inserted, err)
	}
	inserted, err = service.Ingest(context.Background(), event)
	if err != nil || inserted {
		t.Fatalf("duplicate Ingest() = %v, %v", inserted, err)
	}
}

func TestEventServiceProcessesBatchAndPersistsOnlySafeErrorCode(t *testing.T) {
	claimToken := "claim"
	repository := &memoryEventRepository{events: []projection_model.Event{
		{InstanceID: "instance-a", Resource: "groups", EventKey: "ok", ClaimToken: &claimToken},
		{InstanceID: "instance-a", Resource: "groups", EventKey: "failed", ClaimToken: &claimToken},
	}}
	service := &eventService{repository: repository, leaseDuration: time.Minute, retryDelay: time.Second, now: func() time.Time { return time.Unix(100, 0) }}
	result, err := service.ProcessBatch(context.Background(), 10, func(_ context.Context, event *projection_model.Event) error {
		if event.EventKey == "failed" {
			return errors.New("sensitive provider payload")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Claimed != 2 || result.Processed != 1 || result.Failed != 1 || result.Retried != 1 || result.DeadLettered != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if repository.lastErrorCode != errorCodeProcessingFailed || repository.lastErrorCode == "sensitive provider payload" || repository.lastFailureClass != projection_model.EventFailureRetryable {
		t.Fatalf("unsafe error persisted: %q", repository.lastErrorCode)
	}
}

func TestEventServiceDeadLettersPermanentFailureImmediately(t *testing.T) {
	claimToken := "claim"
	repository := &memoryEventRepository{events: []projection_model.Event{{
		InstanceID: "instance-a", Resource: "groups", EventKey: "bad-payload", ClaimToken: &claimToken,
		MaxAttempts: projection_model.DefaultEventMaxAttempts,
	}}}
	service := &eventService{repository: repository, leaseDuration: time.Minute, retryDelay: time.Second, now: func() time.Time { return time.Unix(100, 0) }}
	result, err := service.ProcessBatch(context.Background(), 1, func(context.Context, *projection_model.Event) error {
		return permanentProjectionFailure(errorCodeInvalidPayload)
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeadLettered != 1 || result.Retried != 0 || repository.deadLettered != 1 || repository.lastFailureClass != projection_model.EventFailurePermanent || repository.lastErrorCode != errorCodeInvalidPayload {
		t.Fatalf("result=%#v repository=%#v", result, repository)
	}
}

func TestEventServiceDeadLettersRetryableFailureAtAttemptCeiling(t *testing.T) {
	claimToken := "claim"
	repository := &memoryEventRepository{events: []projection_model.Event{{
		InstanceID: "instance-a", Resource: "groups", EventKey: "database-down", ClaimToken: &claimToken,
		RetryCount: 2, MaxAttempts: 3,
	}}}
	service := &eventService{repository: repository, leaseDuration: time.Minute, retryDelay: time.Second, now: func() time.Time { return time.Unix(100, 0) }}
	result, err := service.ProcessBatch(context.Background(), 1, func(context.Context, *projection_model.Event) error {
		return errors.New("database unavailable")
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeadLettered != 1 || repository.lastFailureClass != projection_model.EventFailureRetryable || repository.lastErrorCode != errorCodeProcessingFailed {
		t.Fatalf("result=%#v repository=%#v", result, repository)
	}
}

func TestProjectionRetryDelayIsDeterministicJitteredAndCapped(t *testing.T) {
	event := &projection_model.Event{InstanceID: "instance-a", Resource: "groups", EventKey: "event-a"}
	first := projectionRetryDelay(time.Second, event, 1)
	if first != projectionRetryDelay(time.Second, event, 1) || first < 750*time.Millisecond || first > 1250*time.Millisecond {
		t.Fatalf("unexpected first retry delay: %v", first)
	}
	if later := projectionRetryDelay(time.Second, event, 5); later < 12*time.Second || later > 20*time.Second {
		t.Fatalf("unexpected exponential retry delay: %v", later)
	}
	if capped := projectionRetryDelay(time.Minute, event, 30); capped > maxProjectionRetryDelay || capped <= 0 {
		t.Fatalf("unexpected capped retry delay: %v", capped)
	}
}
