package outbox

import (
	"context"
	"errors"
	"hash/fnv"
	"regexp"
	"sync"
	"time"
)

var safeDeliveryCode = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

const maxWorkerBatch = 32

type Settings struct {
	BatchSize      int
	LeaseDuration  time.Duration
	PollInterval   time.Duration
	AttemptTimeout time.Duration
	StateTimeout   time.Duration
	RetryBase      time.Duration
	RetryMax       time.Duration
}

type Dispatcher interface {
	Deliver(context.Context, Delivery) error
}

// DeliveryError is a bounded worker-facing classification. Cause is retained
// for errors.Is/errors.As only and must not be logged by the worker.
type DeliveryError struct {
	Code      string
	Retryable bool
	Cause     error
}

func (e *DeliveryError) Error() string { return "external event delivery failed" }
func (e *DeliveryError) Unwrap() error { return e.Cause }

type Observer interface {
	ObserveAttempt(Transport, string, string, time.Duration)
	ObserveClaimed(int)
	ObserveHealth(Health)
	ObserveInfrastructureFailure(string)
}

type noopObserver struct{}

func (noopObserver) ObserveAttempt(Transport, string, string, time.Duration) {}
func (noopObserver) ObserveClaimed(int)                                      {}
func (noopObserver) ObserveHealth(Health)                                    {}
func (noopObserver) ObserveInfrastructureFailure(string)                     {}

type Worker struct {
	repository Repository
	dispatcher Dispatcher
	settings   Settings
	observer   Observer
	now        func() time.Time
}

func NewWorker(repository Repository, dispatcher Dispatcher, settings Settings, observer Observer) (*Worker, error) {
	if repository == nil || dispatcher == nil || settings.BatchSize < 1 || settings.BatchSize > maxWorkerBatch ||
		settings.LeaseDuration <= 0 || settings.PollInterval <= 0 || settings.AttemptTimeout <= 0 ||
		settings.StateTimeout <= 0 || settings.RetryBase <= 0 || settings.RetryMax < settings.RetryBase ||
		settings.LeaseDuration > 30*time.Minute || settings.PollInterval > time.Minute ||
		settings.AttemptTimeout > 10*time.Minute || settings.StateTimeout > time.Minute || settings.RetryMax > 24*time.Hour {
		return nil, errors.New("external event outbox worker settings are invalid")
	}
	if settings.LeaseDuration <= settings.AttemptTimeout+settings.StateTimeout {
		return nil, errors.New("external event outbox lease must exceed attempt and state timeouts")
	}
	if observer == nil {
		observer = noopObserver{}
	}
	return &Worker{repository: repository, dispatcher: dispatcher, settings: settings, observer: observer, now: time.Now}, nil
}

func (w *Worker) Run(ctx context.Context) error {
	if w == nil || ctx == nil {
		return errors.New("external event outbox worker and context are required")
	}
	ticker := time.NewTicker(w.settings.PollInterval)
	defer ticker.Stop()
	for {
		if err := w.processBatch(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			w.observer.ObserveInfrastructureFailure("repository_error")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (w *Worker) processBatch(ctx context.Context) error {
	healthCtx, healthCancel := context.WithTimeout(ctx, w.settings.StateTimeout)
	health, err := w.repository.Health(healthCtx)
	healthCancel()
	if err != nil {
		return err
	}
	w.observer.ObserveHealth(health)

	claimCtx, claimCancel := context.WithTimeout(ctx, w.settings.StateTimeout)
	deliveries, err := w.repository.ClaimReady(claimCtx, w.settings.BatchSize, w.settings.LeaseDuration)
	claimCancel()
	if err != nil {
		return err
	}
	w.observer.ObserveClaimed(len(deliveries))
	// Start every row in this small bounded batch immediately. A later row
	// therefore cannot lose its lease while waiting behind earlier timeouts.
	errorsByDelivery := make(chan error, len(deliveries))
	var workers sync.WaitGroup
	workers.Add(len(deliveries))
	for index := range deliveries {
		go func(delivery *Delivery) {
			defer workers.Done()
			if err := w.processOne(ctx, delivery); err != nil {
				errorsByDelivery <- err
			}
		}(&deliveries[index])
	}
	workers.Wait()
	close(errorsByDelivery)
	return errors.Join(readErrors(errorsByDelivery)...)
}

func readErrors(source <-chan error) []error {
	result := make([]error, 0, len(source))
	for err := range source {
		result = append(result, err)
	}
	return result
}

func (w *Worker) processOne(ctx context.Context, delivery *Delivery) error {
	started := w.now()
	attemptCtx, attemptCancel := context.WithTimeout(ctx, w.settings.AttemptTimeout)
	err := w.dispatcher.Deliver(attemptCtx, *delivery)
	attemptTimedOut := errors.Is(attemptCtx.Err(), context.DeadlineExceeded)
	attemptCancel()
	if ctx.Err() != nil {
		// Leave the fenced claim untouched. Another worker may recover it only
		// after the lease expires, so shutdown never fabricates an outcome.
		return nil
	}
	duration := w.now().Sub(started)
	if err == nil {
		if markErr := w.withStateTimeout(ctx, func(stateCtx context.Context) error {
			return w.repository.MarkDelivered(stateCtx, delivery)
		}); markErr != nil {
			return markErr
		}
		w.observer.ObserveAttempt(delivery.Transport, "delivered", "", duration)
		return nil
	}

	code, retryable := classifyDeliveryError(err, attemptTimedOut)
	deadLetter := !retryable || delivery.AttemptCount >= delivery.MaxAttempts
	if deadLetter {
		if markErr := w.withStateTimeout(ctx, func(stateCtx context.Context) error {
			return w.repository.MarkDeadLetter(stateCtx, delivery, code)
		}); markErr != nil {
			return markErr
		}
		w.observer.ObserveAttempt(delivery.Transport, "dead_letter", code, duration)
		return nil
	}

	retryAt := w.now().UTC().Add(retryDelay(w.settings, delivery.ID, delivery.AttemptCount))
	if markErr := w.withStateTimeout(ctx, func(stateCtx context.Context) error {
		return w.repository.MarkRetry(stateCtx, delivery, code, retryAt)
	}); markErr != nil {
		return markErr
	}
	w.observer.ObserveAttempt(delivery.Transport, "retry", code, duration)
	return nil
}

func (w *Worker) withStateTimeout(ctx context.Context, work func(context.Context) error) error {
	stateCtx, cancel := context.WithTimeout(ctx, w.settings.StateTimeout)
	defer cancel()
	return work(stateCtx)
}

func classifyDeliveryError(err error, timedOut bool) (string, bool) {
	if timedOut || errors.Is(err, context.DeadlineExceeded) {
		return "attempt_timeout", true
	}
	var classified *DeliveryError
	if errors.As(err, &classified) && safeDeliveryCode.MatchString(classified.Code) {
		return classified.Code, classified.Retryable
	}
	return "delivery_failed", true
}

func retryDelay(settings Settings, deliveryID string, attempt int) time.Duration {
	delay := settings.RetryBase
	for step := 1; step < attempt && delay < settings.RetryMax/2; step++ {
		delay *= 2
	}
	if delay > settings.RetryMax {
		delay = settings.RetryMax
	}
	// Stable per-delivery jitter in [80%, 120%] avoids synchronized retry waves
	// without relying on mutable process randomness.
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(deliveryID))
	percent := int64(80 + hash.Sum32()%41)
	jittered := time.Duration(int64(delay) * percent / 100)
	if jittered > settings.RetryMax {
		return settings.RetryMax
	}
	return jittered
}
