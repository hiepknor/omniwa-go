package projection_service

import (
	"context"
	"errors"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
)

const processingErrorCode = "projection_processing_failed"

type EventHandler func(context.Context, *projection_model.Event) error

type EventBatchResult struct {
	Claimed   int
	Processed int
	Failed    int
}

type EventService interface {
	Ingest(ctx context.Context, event *projection_model.Event) (bool, error)
	ProcessBatch(ctx context.Context, limit int, handler EventHandler) (EventBatchResult, error)
}

type eventService struct {
	repository    projection_repository.EventRepository
	leaseDuration time.Duration
	retryDelay    time.Duration
	now           func() time.Time
}

func NewEventService(repository projection_repository.EventRepository, leaseDuration, retryDelay time.Duration) EventService {
	return &eventService{repository: repository, leaseDuration: leaseDuration, retryDelay: retryDelay, now: time.Now}
}

func (s *eventService) Ingest(ctx context.Context, event *projection_model.Event) (bool, error) {
	if s.repository == nil {
		return false, errors.New("projection event repository is required")
	}
	return s.repository.Enqueue(ctx, event)
}

func (s *eventService) ProcessBatch(ctx context.Context, limit int, handler EventHandler) (EventBatchResult, error) {
	var result EventBatchResult
	if s.repository == nil {
		return result, errors.New("projection event repository is required")
	}
	if handler == nil {
		return result, errors.New("projection event handler is required")
	}
	if s.leaseDuration <= 0 || s.retryDelay < 0 {
		return result, errors.New("projection event lease and retry durations are invalid")
	}
	events, err := s.repository.ClaimPending(ctx, limit, s.leaseDuration)
	if err != nil {
		return result, err
	}
	result.Claimed = len(events)
	var processingErrors []error
	for index := range events {
		event := &events[index]
		if err := handler(ctx, event); err != nil {
			result.Failed++
			if markErr := s.repository.MarkFailed(ctx, event, processingErrorCode, s.now().UTC().Add(s.retryDelay)); markErr != nil {
				processingErrors = append(processingErrors, markErr)
			}
			continue
		}
		if err := s.repository.MarkProcessed(ctx, event); err != nil {
			processingErrors = append(processingErrors, err)
			continue
		}
		result.Processed++
	}
	return result, errors.Join(processingErrors...)
}
