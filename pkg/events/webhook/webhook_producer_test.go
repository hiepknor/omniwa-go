package webhook_producer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/netguard"
)

type requesterFunc func(context.Context, string, string, http.Header, []byte) (*netguard.Response, error)

func (f requesterFunc) Do(ctx context.Context, method, rawURL string, header http.Header, body []byte) (*netguard.Response, error) {
	return f(ctx, method, rawURL, header, body)
}

func testSettings() Settings {
	return Settings{Workers: 1, QueueCapacity: 8, MaxPendingPerInstance: 4, MaxAttempts: 3, RetryBase: time.Millisecond}
}

func TestWebhookUsesBoundedConfiguredRequester(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected request method=%s content-type=%s", r.Method, r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	requester, err := netguard.NewRequester(netguard.RequestSettings{
		AllowedHosts: []string{parsed.Hostname()}, AllowedPorts: []string{parsed.Port()}, AllowPrivate: true, Timeout: time.Second,
		MaxRequestBytes: 1024, MaxResponseBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	producer, err := NewWebhookProducer("", requester, nil, testSettings())
	if err != nil {
		t.Fatal(err)
	}
	if sendErr, status := producer.sendWebhook(context.Background(), server.URL, []byte(`{"ok":true}`)); sendErr != nil || status != http.StatusNoContent {
		t.Fatalf("sendWebhook() status=%d error=%v", status, sendErr)
	}
}

func TestWebhookRejectsUnconfiguredTarget(t *testing.T) {
	producer, err := NewWebhookProducer("", nil, nil, testSettings())
	if err != nil {
		t.Fatal(err)
	}
	sendErr, _ := producer.sendWebhook(context.Background(), "https://example.com", nil)
	if !errors.Is(sendErr, netguard.ErrUnsafeTarget) {
		t.Fatalf("sendWebhook() error=%v, want ErrUnsafeTarget", sendErr)
	}
}

func TestProducerEnforcesGlobalAndPerInstanceOutstandingLimits(t *testing.T) {
	settings := testSettings()
	settings.QueueCapacity = 3
	settings.MaxPendingPerInstance = 2
	producer, err := NewWebhookProducer("", nil, nil, settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := producer.Produce("event.message", []byte("one"), "https://example.com", "instance-a"); err != nil {
		t.Fatal(err)
	}
	if err := producer.Produce("event.message", []byte("two"), "https://example.com", "instance-a"); err != nil {
		t.Fatal(err)
	}
	if err := producer.Produce("event.message", nil, "https://example.com", "instance-a"); !errors.Is(err, ErrInstanceQueueFull) {
		t.Fatalf("third instance delivery error = %v", err)
	}
	if err := producer.Produce("event.message", nil, "https://example.com", "instance-b"); err != nil {
		t.Fatal(err)
	}
	if err := producer.Produce("event.message", nil, "https://example.com", "instance-c"); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("delivery beyond global capacity error = %v", err)
	}
	stats := producer.Stats()
	if stats.Pending != 3 || stats.Accepted != 3 || stats.Dropped != 2 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestProducerConcurrentAdmissionNeverExceedsCapacity(t *testing.T) {
	settings := testSettings()
	settings.QueueCapacity = 16
	settings.MaxPendingPerInstance = 4
	producer, err := NewWebhookProducer("", nil, nil, settings)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for i := 0; i < 100; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_ = producer.Produce("event.message", nil, "https://example.com", "instance-"+string(rune('a'+index%10)))
		}(i)
	}
	wait.Wait()
	stats := producer.Stats()
	if stats.Pending != settings.QueueCapacity || stats.Accepted != uint64(settings.QueueCapacity) || stats.Dropped != 100-uint64(settings.QueueCapacity) {
		t.Fatalf("concurrent admission stats = %+v", stats)
	}
}

func TestProducerBoundsConcurrentRequestsAndStopsWithContext(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	started := make(chan struct{}, 8)
	requester := requesterFunc(func(ctx context.Context, _, _ string, _ http.Header, _ []byte) (*netguard.Response, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	settings := testSettings()
	settings.Workers = 3
	settings.QueueCapacity = 8
	settings.MaxPendingPerInstance = 8
	producer, err := NewWebhookProducer("", requester, nil, settings)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- producer.Run(ctx) }()
	for i := 0; i < settings.QueueCapacity; i++ {
		if err := producer.Produce("event.message", nil, "https://example.com", "instance-a"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < settings.Workers; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("workers did not start requests")
		}
	}
	if got := maximum.Load(); got != int32(settings.Workers) {
		t.Fatalf("maximum concurrent requests = %d, want %d", got, settings.Workers)
	}
	cancel()
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatal(runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("producer did not stop after cancellation")
	}
	if err := producer.Produce("event.message", nil, "https://example.com", "instance-b"); !errors.Is(err, ErrStopped) {
		t.Fatalf("produce after shutdown error = %v", err)
	}
	if stats := producer.Stats(); stats.Pending != 0 || stats.Dropped == 0 {
		t.Fatalf("shutdown stats = %+v", stats)
	}
}

func TestProducerRetriesOnlyTransientFailures(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	requester := requesterFunc(func(_ context.Context, _, rawURL string, _ http.Header, _ []byte) (*netguard.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		calls[rawURL]++
		if rawURL == "https://transient.example" && calls[rawURL] == 1 {
			return &netguard.Response{StatusCode: http.StatusServiceUnavailable}, nil
		}
		if rawURL == "https://permanent.example" {
			return &netguard.Response{StatusCode: http.StatusBadRequest}, nil
		}
		return &netguard.Response{StatusCode: http.StatusNoContent}, nil
	})
	producer, err := NewWebhookProducer("", requester, nil, testSettings())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- producer.Run(ctx) }()
	if err := producer.Produce("event.message", nil, "https://transient.example", "instance-a"); err != nil {
		t.Fatal(err)
	}
	if err := producer.Produce("event.message", nil, "https://permanent.example", "instance-b"); err != nil {
		t.Fatal(err)
	}
	waitForStats(t, producer, func(stats Stats) bool { return stats.Succeeded == 1 && stats.Failed == 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["https://transient.example"] != 2 || calls["https://permanent.example"] != 1 {
		t.Fatalf("request counts = %#v", calls)
	}
	if stats := producer.Stats(); stats.Retried != 1 {
		t.Fatalf("retry stats = %+v", stats)
	}
}

func TestRetryClassificationHonorsRequestTimeoutAndShutdown(t *testing.T) {
	if !isRetryable(context.Background(), context.DeadlineExceeded, 0) {
		t.Fatal("request timeout was not classified as transient")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if isRetryable(ctx, errors.New("network failure"), 0) {
		t.Fatal("shutdown failure was classified as retryable")
	}
	if isRetryable(context.Background(), errors.New("bad request"), http.StatusBadRequest) {
		t.Fatal("ordinary 4xx response was classified as retryable")
	}
	if !isRetryable(context.Background(), errors.New("throttled"), http.StatusTooManyRequests) {
		t.Fatal("429 response was not classified as retryable")
	}
}

func TestProducerCopiesPayloadBeforeReturning(t *testing.T) {
	received := make(chan string, 1)
	requester := requesterFunc(func(_ context.Context, _, _ string, _ http.Header, body []byte) (*netguard.Response, error) {
		received <- string(body)
		return &netguard.Response{StatusCode: http.StatusNoContent}, nil
	})
	producer, err := NewWebhookProducer("", requester, nil, testSettings())
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("original")
	if err := producer.Produce("event.message", payload, "https://example.com", "instance-a"); err != nil {
		t.Fatal(err)
	}
	copy(payload, "mutated!")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- producer.Run(ctx) }()
	select {
	case got := <-received:
		if got != "original" {
			t.Fatalf("received payload = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("delivery was not received")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestNewProducerRejectsInvalidSettingsAndDuplicateRun(t *testing.T) {
	if _, err := NewWebhookProducer("", nil, nil, Settings{}); err == nil {
		t.Fatal("invalid settings were accepted")
	}
	settings := testSettings()
	settings.MaxPendingPerInstance = settings.QueueCapacity + 1
	if _, err := NewWebhookProducer("", nil, nil, settings); err == nil {
		t.Fatal("oversized per-instance limit was accepted")
	}
	producer, err := NewWebhookProducer("", nil, nil, testSettings())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- producer.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for {
		producer.mu.Lock()
		started := producer.started
		producer.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("producer did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if err := producer.Run(context.Background()); err == nil {
		t.Fatal("duplicate Run was accepted")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func waitForStats(t *testing.T, producer *Producer, ready func(Stats) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		if stats := producer.Stats(); ready(stats) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for stats: %+v", producer.Stats())
		}
		time.Sleep(time.Millisecond)
	}
}
