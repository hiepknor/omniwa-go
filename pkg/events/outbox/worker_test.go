package outbox

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/google/uuid"
)

type fakeOutboxRepository struct {
	mu          sync.Mutex
	delivered   int
	retried     int
	deadLetters int
	code        string
	retryAt     time.Time
	health      Health
}

func (*fakeOutboxRepository) Record(context.Context, *projection_model.DurableEvent, []Delivery) error {
	return nil
}
func (*fakeOutboxRepository) ClaimReady(context.Context, int, time.Duration) ([]Delivery, error) {
	return nil, nil
}
func (r *fakeOutboxRepository) MarkDelivered(context.Context, *Delivery) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.delivered++
	return nil
}
func (r *fakeOutboxRepository) MarkRetry(_ context.Context, _ *Delivery, code string, retryAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retried++
	r.code, r.retryAt = code, retryAt
	return nil
}
func (r *fakeOutboxRepository) MarkDeadLetter(_ context.Context, _ *Delivery, code string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deadLetters++
	r.code = code
	return nil
}
func (r *fakeOutboxRepository) Health(context.Context) (Health, error) { return r.health, nil }

type batchRepository struct {
	*fakeOutboxRepository
	ready []Delivery
}

type flakyHealthRepository struct {
	*fakeOutboxRepository
	calls     atomic.Int32
	recovered chan struct{}
}

func (r *flakyHealthRepository) Health(context.Context) (Health, error) {
	if r.calls.Add(1) == 1 {
		return Health{}, errors.New("temporary database failure")
	}
	select {
	case <-r.recovered:
	default:
		close(r.recovered)
	}
	return Health{}, nil
}

type workerObserver struct{ infrastructure chan string }

func (*workerObserver) ObserveAttempt(Transport, string, string, time.Duration) {}
func (*workerObserver) ObserveClaimed(int)                                      {}
func (*workerObserver) ObserveHealth(Health)                                    {}
func (o *workerObserver) ObserveInfrastructureFailure(code string)              { o.infrastructure <- code }

func (r *batchRepository) ClaimReady(context.Context, int, time.Duration) ([]Delivery, error) {
	return append([]Delivery(nil), r.ready...), nil
}

type dispatcherFunc func(context.Context, Delivery) error

func (f dispatcherFunc) Deliver(ctx context.Context, delivery Delivery) error {
	return f(ctx, delivery)
}

func testWorker(t *testing.T, repository Repository, dispatcher Dispatcher) *Worker {
	t.Helper()
	worker, err := NewWorker(repository, dispatcher, Settings{
		BatchSize: 10, LeaseDuration: time.Minute, PollInterval: time.Millisecond,
		AttemptTimeout: 20 * time.Millisecond, StateTimeout: 10 * time.Millisecond,
		RetryBase: time.Second, RetryMax: time.Minute,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	worker.now = func() time.Time { return time.Unix(10_000, 0).UTC() }
	return worker
}

func claimedDelivery(attempt, maximum int) Delivery {
	token := uuid.NewString()
	return Delivery{
		ID: uuid.NewString(), InstanceID: uuid.NewString(), Transport: TransportWebhook,
		Destination: DestinationInstance, RoutingKey: "instance.message", Status: StatusProcessing,
		ClaimToken: &token, AttemptCount: attempt, MaxAttempts: maximum,
	}
}

func TestWorkerTransitionsConfirmedRetryableAndPermanentAttempts(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		dispatch   error
		attempt    int
		maximum    int
		delivered  int
		retried    int
		deadLetter int
		code       string
	}{
		{name: "confirmed", attempt: 1, maximum: 3, delivered: 1},
		{name: "retryable", dispatch: &DeliveryError{Code: "connection_unavailable", Retryable: true}, attempt: 1, maximum: 3, retried: 1, code: "connection_unavailable"},
		{name: "permanent", dispatch: &DeliveryError{Code: "destination_disabled", Retryable: false}, attempt: 1, maximum: 3, deadLetter: 1, code: "destination_disabled"},
		{name: "budget exhausted", dispatch: &DeliveryError{Code: "connection_unavailable", Retryable: true}, attempt: 3, maximum: 3, deadLetter: 1, code: "connection_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakeOutboxRepository{}
			worker := testWorker(t, repository, dispatcherFunc(func(context.Context, Delivery) error { return test.dispatch }))
			delivery := claimedDelivery(test.attempt, test.maximum)
			if err := worker.processOne(context.Background(), &delivery); err != nil {
				t.Fatal(err)
			}
			if repository.delivered != test.delivered || repository.retried != test.retried ||
				repository.deadLetters != test.deadLetter || repository.code != test.code {
				t.Fatalf("transitions delivered=%d retried=%d dead=%d code=%q", repository.delivered, repository.retried, repository.deadLetters, repository.code)
			}
		})
	}
}

func TestWorkerClassifiesTimeoutAndLeavesShutdownClaimUntouched(t *testing.T) {
	t.Parallel()
	repository := &fakeOutboxRepository{}
	worker := testWorker(t, repository, dispatcherFunc(func(ctx context.Context, _ Delivery) error {
		<-ctx.Done()
		return ctx.Err()
	}))
	delivery := claimedDelivery(1, 3)
	if err := worker.processOne(context.Background(), &delivery); err != nil {
		t.Fatal(err)
	}
	if repository.retried != 1 || repository.code != "attempt_timeout" {
		t.Fatalf("timeout transition retried=%d code=%q", repository.retried, repository.code)
	}

	shutdownRepository := &fakeOutboxRepository{}
	shutdownWorker := testWorker(t, shutdownRepository, dispatcherFunc(func(ctx context.Context, _ Delivery) error {
		return ctx.Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := shutdownWorker.processOne(ctx, &delivery); err != nil {
		t.Fatal(err)
	}
	if shutdownRepository.delivered+shutdownRepository.retried+shutdownRepository.deadLetters != 0 {
		t.Fatal("shutdown fabricated a terminal delivery state")
	}
}

func TestWorkerValidationClassificationAndStableRetryDelay(t *testing.T) {
	t.Parallel()
	if _, err := NewWorker(nil, nil, Settings{}, nil); err == nil {
		t.Fatal("invalid worker configuration was accepted")
	}
	if code, retryable := classifyDeliveryError(errors.New("secret destination failure"), false); code != "delivery_failed" || !retryable {
		t.Fatalf("unclassified error = %q retryable=%t", code, retryable)
	}
	settings := Settings{RetryBase: time.Second, RetryMax: 10 * time.Second}
	first := retryDelay(settings, "stable-delivery", 4)
	second := retryDelay(settings, "stable-delivery", 4)
	if first != second || first < 6400*time.Millisecond || first > 9600*time.Millisecond {
		t.Fatalf("stable bounded retry delay = %s, %s", first, second)
	}
	if capped := retryDelay(settings, "stable-delivery", 100); capped > settings.RetryMax {
		t.Fatalf("retry delay exceeded configured maximum: %s > %s", capped, settings.RetryMax)
	}
}

func TestWorkerStartsWholeBoundedBatchBeforeLeaseCanAge(t *testing.T) {
	t.Parallel()
	repository := &batchRepository{fakeOutboxRepository: &fakeOutboxRepository{}}
	for range 4 {
		repository.ready = append(repository.ready, claimedDelivery(1, 3))
	}
	started := make(chan struct{}, len(repository.ready))
	release := make(chan struct{})
	worker := testWorker(t, repository, dispatcherFunc(func(context.Context, Delivery) error {
		started <- struct{}{}
		<-release
		return nil
	}))
	done := make(chan error, 1)
	go func() { done <- worker.processBatch(context.Background()) }()
	for range repository.ready {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("claimed batch was processed serially")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if repository.delivered != len(repository.ready) {
		t.Fatalf("delivered=%d want=%d", repository.delivered, len(repository.ready))
	}
}

func TestWorkerRecoversAfterInfrastructureFailure(t *testing.T) {
	t.Parallel()
	repository := &flakyHealthRepository{fakeOutboxRepository: &fakeOutboxRepository{}, recovered: make(chan struct{})}
	observer := &workerObserver{infrastructure: make(chan string, 1)}
	worker, err := NewWorker(repository, dispatcherFunc(func(context.Context, Delivery) error { return nil }), Settings{
		BatchSize: 1, LeaseDuration: time.Minute, PollInterval: time.Millisecond,
		AttemptTimeout: 20 * time.Millisecond, StateTimeout: 10 * time.Millisecond,
		RetryBase: time.Second, RetryMax: time.Minute,
	}, observer)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case code := <-observer.infrastructure:
		if code != "repository_error" {
			t.Fatalf("infrastructure code=%q", code)
		}
	case <-time.After(time.Second):
		t.Fatal("infrastructure failure was not observed")
	}
	select {
	case <-repository.recovered:
	case <-time.After(time.Second):
		t.Fatal("worker did not resume polling")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type fakeWebhookDeliverer struct{ id string }

func (f *fakeWebhookDeliverer) DeliverConfirmed(_ context.Context, _ string, _ []byte, id string) error {
	f.id = id
	return nil
}

type fakeRabbitDeliverer struct{ id string }

func (f *fakeRabbitDeliverer) DeliverConfirmed(_ context.Context, _ string, _ []byte, _, id string) error {
	f.id = id
	return nil
}

type fakeTargets struct{}

type passthroughPayloadPolicy struct{}

func (passthroughPayloadPolicy) Apply(payload []byte) ([]byte, error) { return payload, nil }

func (fakeTargets) WebhookTarget(context.Context, Destination, string) (string, error) {
	return "https://example.test/events", nil
}
func (fakeTargets) RabbitMQMode(context.Context, Destination, string) (string, error) {
	return "enabled", nil
}

func TestTransportDispatcherForwardsStableIdentityAndRejectsNATS(t *testing.T) {
	t.Parallel()
	webhook := &fakeWebhookDeliverer{}
	rabbit := &fakeRabbitDeliverer{}
	dispatcher, err := NewTransportDispatcher(webhook, rabbit, fakeTargets{}, passthroughPayloadPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	webhookDelivery := claimedDelivery(1, 3)
	if err := dispatcher.Deliver(context.Background(), webhookDelivery); err != nil || webhook.id != webhookDelivery.ID {
		t.Fatalf("webhook delivery ID=%q error=%v", webhook.id, err)
	}
	rabbitDelivery := claimedDelivery(1, 3)
	rabbitDelivery.Transport = TransportRabbitMQ
	if err := dispatcher.Deliver(context.Background(), rabbitDelivery); err != nil || rabbit.id != rabbitDelivery.ID {
		t.Fatalf("RabbitMQ delivery ID=%q error=%v", rabbit.id, err)
	}
	natsDelivery := claimedDelivery(1, 3)
	natsDelivery.Transport = TransportNATS
	err = dispatcher.Deliver(context.Background(), natsDelivery)
	var classified *DeliveryError
	if !errors.As(err, &classified) || classified.Code != "transport_not_supported" || classified.Retryable {
		t.Fatalf("NATS classification = %#v, %v", classified, err)
	}
}
