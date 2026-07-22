package projection_service

import (
	"context"
	"errors"
	"hash/fnv"
	"math"
	"strconv"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
)

const maxProjectionRetryDelay = 5 * time.Minute

type EventHandler func(context.Context, *projection_model.Event) error

type EventBatchResult struct {
	Claimed      int
	Processed    int
	Failed       int
	Retried      int
	DeadLettered int
}

type EventService interface {
	Ingest(ctx context.Context, event *projection_model.Event) (bool, error)
	ProcessBatch(ctx context.Context, limit int, handler EventHandler) (EventBatchResult, error)
	ProcessBatchFor(ctx context.Context, resource string, eventTypes []string, limit int, handler EventHandler) (EventBatchResult, error)
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
	return s.processBatch(ctx, "", nil, limit, handler)
}

func (s *eventService) ProcessBatchFor(ctx context.Context, resource string, eventTypes []string, limit int, handler EventHandler) (EventBatchResult, error) {
	if resource == "" || len(eventTypes) == 0 {
		return EventBatchResult{}, errors.New("projection resource and event types are required")
	}
	return s.processBatch(ctx, resource, eventTypes, limit, handler)
}

func (s *eventService) processBatch(ctx context.Context, resource string, eventTypes []string, limit int, handler EventHandler) (EventBatchResult, error) {
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
	var events []projection_model.Event
	var err error
	if resource == "" {
		events, err = s.repository.ClaimPending(ctx, limit, s.leaseDuration)
	} else {
		events, err = s.repository.ClaimPendingFor(ctx, resource, eventTypes, limit, s.leaseDuration)
	}
	if err != nil {
		return result, err
	}
	result.Claimed = len(events)
	var processingErrors []error
	for index := range events {
		event := &events[index]
		if err := handler(ctx, event); err != nil {
			result.Failed++
			attemptedAt := s.now().UTC()
			failureClass, errorCode := classifyProcessingFailure(err)
			attempt := event.RetryCount + 1
			maxAttempts := event.MaxAttempts
			if maxAttempts <= 0 {
				maxAttempts = projection_model.DefaultEventMaxAttempts
			}
			if failureClass == projection_model.EventFailurePermanent || attempt >= maxAttempts {
				if markErr := s.repository.MarkDeadLetter(ctx, event, failureClass, errorCode, attemptedAt); markErr != nil {
					processingErrors = append(processingErrors, markErr)
				} else {
					result.DeadLettered++
				}
				continue
			}
			retryAt := attemptedAt.Add(projectionRetryDelay(s.retryDelay, event, attempt))
			if markErr := s.repository.MarkRetry(ctx, event, failureClass, errorCode, attemptedAt, retryAt); markErr != nil {
				processingErrors = append(processingErrors, markErr)
			} else {
				result.Retried++
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

func projectionRetryDelay(base time.Duration, event *projection_model.Event, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}
	exponent := attempt - 1
	if exponent < 0 {
		exponent = 0
	}
	if exponent > 30 {
		exponent = 30
	}
	delay := float64(base) * math.Pow(2, float64(exponent))
	if delay > float64(maxProjectionRetryDelay) {
		delay = float64(maxProjectionRetryDelay)
	}
	hash := fnv.New32a()
	if event != nil {
		_, _ = hash.Write([]byte(event.InstanceID))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(event.Resource))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(event.EventKey))
	}
	_, _ = hash.Write([]byte(strconv.Itoa(attempt)))
	jitterPermille := 750 + int(hash.Sum32()%501)
	delay = delay * float64(jitterPermille) / 1000
	if delay > float64(maxProjectionRetryDelay) {
		delay = float64(maxProjectionRetryDelay)
	}
	return time.Duration(delay)
}
