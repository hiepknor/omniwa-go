package webhook_producer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	producer_interfaces "github.com/evolution-foundation/evolution-go/pkg/events/interfaces"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	"github.com/evolution-foundation/evolution-go/pkg/netguard"
)

var _ producer_interfaces.Producer = (*Producer)(nil)

var (
	ErrQueueFull         = errors.New("webhook delivery queue is full")
	ErrInstanceQueueFull = errors.New("webhook delivery quota is full for instance")
	ErrStopped           = errors.New("webhook delivery producer is stopped")
)

type Settings struct {
	Workers               int
	QueueCapacity         int
	MaxPendingPerInstance int
	MaxAttempts           int
	RetryBase             time.Duration
}

type Stats struct {
	Pending   int
	Accepted  uint64
	Succeeded uint64
	Failed    uint64
	Retried   uint64
	Dropped   uint64
}

type delivery struct {
	url        string
	body       []byte
	instanceID string
}

type Producer struct {
	url           string
	loggerWrapper *logger_wrapper.LoggerManager
	requester     netguard.Requester
	settings      Settings
	queue         chan delivery

	mu                sync.Mutex
	started           bool
	accepting         bool
	pending           int
	pendingByInstance map[string]int

	accepted  atomic.Uint64
	succeeded atomic.Uint64
	failed    atomic.Uint64
	retried   atomic.Uint64
	dropped   atomic.Uint64
}

func NewWebhookProducer(
	url string,
	requester netguard.Requester,
	loggerWrapper *logger_wrapper.LoggerManager,
	settings Settings,
) (*Producer, error) {
	if settings.Workers <= 0 || settings.QueueCapacity <= 0 || settings.MaxPendingPerInstance <= 0 ||
		settings.MaxAttempts <= 0 || settings.RetryBase <= 0 {
		return nil, errors.New("webhook worker, queue, retry, and per-instance limits must be positive")
	}
	if settings.MaxPendingPerInstance > settings.QueueCapacity {
		return nil, errors.New("webhook per-instance pending limit cannot exceed queue capacity")
	}
	return &Producer{
		url:               url,
		requester:         requester,
		loggerWrapper:     loggerWrapper,
		settings:          settings,
		queue:             make(chan delivery, settings.QueueCapacity),
		accepting:         true,
		pendingByInstance: make(map[string]int),
	}, nil
}

// Run owns the fixed worker pool until ctx is cancelled. It is intended to be
// registered exactly once with the process supervisor.
func (p *Producer) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return errors.New("webhook delivery producer is already running")
	}
	p.started = true
	p.mu.Unlock()

	var workers sync.WaitGroup
	workers.Add(p.settings.Workers)
	for range p.settings.Workers {
		go func() {
			defer workers.Done()
			p.worker(ctx)
		}()
	}

	<-ctx.Done()
	p.mu.Lock()
	p.accepting = false
	p.mu.Unlock()
	workers.Wait()

	// Workers stop promptly on cancellation. Account for queued deliveries that
	// were deliberately abandoned rather than pretending they were delivered.
	for {
		select {
		case item := <-p.queue:
			p.dropped.Add(1)
			p.release(item.instanceID)
		default:
			return nil
		}
	}
}

func (p *Producer) Produce(queueName string, payload []byte, webhookURL string, userID string) error {
	if len(strings.Split(queueName, ".")) < 2 {
		return nil
	}

	instanceID := userID
	if instanceID == "" {
		instanceID = "unknown"
	}
	urls := make([]string, 0, 2)
	if p.url != "" {
		urls = append(urls, p.url)
	}
	if webhookURL != "" {
		urls = append(urls, webhookURL)
	}

	var enqueueErrors []error
	for _, target := range urls {
		item := delivery{url: target, body: append([]byte(nil), payload...), instanceID: instanceID}
		if err := p.enqueue(item); err != nil {
			enqueueErrors = append(enqueueErrors, err)
			p.log(instanceID, "warn", "enqueue", "dropped", errorCode(err), 0, 0)
		}
	}
	return errors.Join(enqueueErrors...)
}

func (p *Producer) enqueue(item delivery) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.accepting {
		p.dropped.Add(1)
		return ErrStopped
	}
	if p.pending >= p.settings.QueueCapacity {
		p.dropped.Add(1)
		return ErrQueueFull
	}
	if p.pendingByInstance[item.instanceID] >= p.settings.MaxPendingPerInstance {
		p.dropped.Add(1)
		return ErrInstanceQueueFull
	}
	p.pending++
	p.pendingByInstance[item.instanceID]++
	p.accepted.Add(1)
	// Outstanding reservations are bounded by the channel capacity, so this
	// send cannot block even when workers have not started yet.
	p.queue <- item
	return nil
}

func (p *Producer) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-p.queue:
			p.deliver(ctx, item)
			p.release(item.instanceID)
		}
	}
}

func (p *Producer) deliver(ctx context.Context, item delivery) {
	for attempt := 1; attempt <= p.settings.MaxAttempts; attempt++ {
		err, statusCode := p.sendWebhook(ctx, item.url, item.body)
		if err == nil {
			p.succeeded.Add(1)
			p.log(item.instanceID, "info", "deliver", "success", "", attempt, statusCode)
			return
		}
		if ctx.Err() != nil {
			p.dropped.Add(1)
			p.log(item.instanceID, "warn", "deliver", "cancelled", "shutdown", attempt, statusCode)
			return
		}
		if !isRetryable(ctx, err, statusCode) || attempt == p.settings.MaxAttempts {
			p.failed.Add(1)
			p.log(item.instanceID, "error", "deliver", "failed", errorCode(err), attempt, statusCode)
			return
		}

		p.retried.Add(1)
		p.log(item.instanceID, "warn", "retry", "scheduled", errorCode(err), attempt, statusCode)
		delay := retryDelay(p.settings.RetryBase, attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			p.dropped.Add(1)
			p.log(item.instanceID, "warn", "deliver", "cancelled", "shutdown", attempt, statusCode)
			return
		case <-timer.C:
		}
	}
}

func (p *Producer) release(instanceID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending > 0 {
		p.pending--
	}
	if p.pendingByInstance[instanceID] <= 1 {
		delete(p.pendingByInstance, instanceID)
	} else {
		p.pendingByInstance[instanceID]--
	}
}

func (p *Producer) Stats() Stats {
	p.mu.Lock()
	pending := p.pending
	p.mu.Unlock()
	return Stats{
		Pending: pending, Accepted: p.accepted.Load(), Succeeded: p.succeeded.Load(),
		Failed: p.failed.Load(), Retried: p.retried.Load(), Dropped: p.dropped.Load(),
	}
}

func (p *Producer) sendWebhook(ctx context.Context, url string, body []byte) (error, int) {
	if p.requester == nil {
		return fmt.Errorf("%w: webhook host is not configured", netguard.ErrUnsafeTarget), 0
	}
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	resp, err := p.requester.Do(ctx, http.MethodPost, url, header, body)
	if err != nil {
		return err, 0
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("received non-2xx webhook response"), resp.StatusCode
	}
	return nil, resp.StatusCode
}

func isRetryable(ctx context.Context, err error, statusCode int) bool {
	if ctx.Err() != nil {
		return false
	}
	if errors.Is(err, netguard.ErrUnsafeTarget) || errors.Is(err, netguard.ErrResponseLarge) ||
		errors.Is(err, context.Canceled) {
		return false
	}
	if statusCode == 0 {
		return true
	}
	return statusCode == http.StatusRequestTimeout || statusCode == http.StatusTooEarly ||
		statusCode == http.StatusTooManyRequests || statusCode >= http.StatusInternalServerError
}

func retryDelay(base time.Duration, attempt int) time.Duration {
	delay := base
	for i := 1; i < attempt && delay <= time.Hour/2; i++ {
		delay *= 2
	}
	return delay
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, ErrQueueFull):
		return "queue_full"
	case errors.Is(err, ErrInstanceQueueFull):
		return "instance_queue_full"
	case errors.Is(err, ErrStopped):
		return "stopped"
	case errors.Is(err, netguard.ErrUnsafeTarget):
		return "unsafe_target"
	case errors.Is(err, netguard.ErrResponseLarge):
		return "response_too_large"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "cancelled"
	default:
		return "delivery_error"
	}
}

func (p *Producer) log(instanceID, level, action, result, code string, attempt, statusCode int) {
	if p.loggerWrapper == nil {
		return
	}
	message := "component=webhook action=%s result=%s error_code=%s attempt=%d status_code=%d"
	instanceLogger := p.loggerWrapper.GetLogger(instanceID)
	switch level {
	case "info":
		instanceLogger.LogInfo(message, action, result, code, attempt, statusCode)
	case "warn":
		instanceLogger.LogWarn(message, action, result, code, attempt, statusCode)
	default:
		instanceLogger.LogError(message, action, result, code, attempt, statusCode)
	}
}

// CreateGlobalQueues does nothing for the webhook producer.
func (p *Producer) CreateGlobalQueues() error { return nil }
