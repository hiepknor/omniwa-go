package projection_service

import (
	"context"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
)

type captureWorkerEventService struct {
	resource   string
	eventTypes []string
	called     int
}

func (s *captureWorkerEventService) Ingest(context.Context, *projection_model.Event) (bool, error) {
	return false, nil
}

func (s *captureWorkerEventService) ProcessBatch(context.Context, int, EventHandler) (EventBatchResult, error) {
	return EventBatchResult{}, nil
}

func (s *captureWorkerEventService) ProcessBatchFor(_ context.Context, resource string, eventTypes []string, _ int, _ EventHandler) (EventBatchResult, error) {
	s.called++
	s.resource = resource
	s.eventTypes = append([]string(nil), eventTypes...)
	return EventBatchResult{Claimed: 1, Processed: 1}, nil
}

func TestWorkerClaimsOnlyRegisteredEventTypesAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events := &captureWorkerEventService{}
	worker := NewWorker(events, "groups", []string{"joined_group"}, 10, time.Hour, func(context.Context, *projection_model.Event) error { return nil }, func(EventBatchResult, error) { cancel() })
	if err := worker.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if events.called != 1 || events.resource != "groups" || len(events.eventTypes) != 1 || events.eventTypes[0] != "joined_group" {
		t.Fatalf("unexpected worker selection: %#v", events)
	}
}
