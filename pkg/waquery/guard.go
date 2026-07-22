package waquery

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"
)

const halfOpenRetryAfter = time.Second

type CircuitState string

const (
	CircuitClosed   CircuitState = "closed"
	CircuitOpen     CircuitState = "open"
	CircuitHalfOpen CircuitState = "half_open"
)

type Settings struct {
	RatePerSecond float64
	Burst         int
	MaxWait       time.Duration
	Cooldown      time.Duration
}

func (s Settings) Validate() error {
	if s.RatePerSecond <= 0 || math.IsNaN(s.RatePerSecond) || math.IsInf(s.RatePerSecond, 0) {
		return errors.New("rate per second must be finite and positive")
	}
	if s.Burst <= 0 {
		return errors.New("burst must be positive")
	}
	if s.MaxWait < 0 {
		return errors.New("max wait must not be negative")
	}
	if s.Cooldown <= 0 {
		return errors.New("cooldown must be positive")
	}
	return nil
}

type Query func(context.Context) (any, error)

type Guard interface {
	Do(ctx context.Context, instanceID, operation, resource string, query Query) (any, error)
	ObserveError(instanceID string, err error) error
	RemoveInstance(instanceID string)
	Snapshot(instanceID string) (Snapshot, bool)
}

// ObserveError classifies an error from an operation that must remain outside
// the information-query limiter (for example, a mutation). An upstream 429
// still opens the shared per-instance breaker and is normalized for HTTP
// callers, without consuming a token or single-flighting the operation.
func (g *QueryGuard) ObserveError(instanceID string, err error) error {
	if err == nil {
		return nil
	}
	if strings.TrimSpace(instanceID) == "" || !g.classifier(err) {
		return err
	}

	g.instance(instanceID).breaker.open(g.now(), g.settings.Cooldown)
	return &RateLimitError{RetryAfter: g.settings.Cooldown, Source: LimitSourceUpstream, Cause: err}
}

// Do converts the untyped Guard boundary into a type-safe call for services.
// Keeping the Guard interface untyped allows tests and alternate implementations
// to remain simple while preventing repeated assertions at every call site.
func Do[T any](ctx context.Context, guard Guard, instanceID, operation, resource string, query func(context.Context) (T, error)) (T, error) {
	var zero T
	if guard == nil {
		return zero, errors.New("WhatsApp information query guard is required")
	}

	value, err := guard.Do(ctx, instanceID, operation, resource, func(queryCtx context.Context) (any, error) {
		return query(queryCtx)
	})
	if err != nil {
		return zero, err
	}

	typed, ok := value.(T)
	if !ok {
		return zero, fmt.Errorf("unexpected WhatsApp information query result type %T", value)
	}
	return typed, nil
}

type Snapshot struct {
	CircuitState CircuitState
	OpenUntil    time.Time
}

type QueryGuard struct {
	settings   Settings
	classifier func(error) bool
	now        func() time.Time

	mu        sync.Mutex
	instances map[string]*instanceState
}

type instanceState struct {
	limiter *rate.Limiter
	flights singleflight.Group
	breaker circuitBreaker
}

type circuitBreaker struct {
	mu            sync.Mutex
	state         CircuitState
	openUntil     time.Time
	trialInFlight bool
}

func New(settings Settings) (*QueryGuard, error) {
	if err := settings.Validate(); err != nil {
		return nil, fmt.Errorf("invalid WhatsApp information query guard settings: %w", err)
	}

	return &QueryGuard{
		settings:   settings,
		classifier: isWhatsAppRateLimit,
		now:        time.Now,
		instances:  make(map[string]*instanceState),
	}, nil
}

func (g *QueryGuard) Do(ctx context.Context, instanceID, operation, resource string, query Query) (any, error) {
	if ctx == nil {
		return nil, errors.New("query context is required")
	}
	if strings.TrimSpace(instanceID) == "" {
		return nil, errors.New("instance ID is required")
	}
	if strings.TrimSpace(operation) == "" {
		return nil, errors.New("query operation is required")
	}
	if query == nil {
		return nil, errors.New("query function is required")
	}

	state := g.instance(instanceID)
	flightKey := makeFlightKey(instanceID, operation, resource)
	result := state.flights.DoChan(flightKey, func() (any, error) {
		return g.execute(ctx, state, query)
	})

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-result:
		return res.Val, res.Err
	}
}

func (g *QueryGuard) execute(ctx context.Context, state *instanceState, query Query) (any, error) {
	now := g.now()
	allowed, trial, retryAfter := state.breaker.allow(now)
	if !allowed {
		return nil, &RateLimitError{
			RetryAfter: retryAfter,
			Source:     LimitSourceCircuitOpen,
		}
	}

	if err := g.waitForToken(ctx, state.limiter); err != nil {
		if trial {
			state.breaker.abortTrial()
		}
		return nil, err
	}

	value, err := query(ctx)
	completedAt := g.now()
	if err != nil && g.classifier(err) {
		state.breaker.open(completedAt, g.settings.Cooldown)
		return nil, &RateLimitError{
			RetryAfter: g.settings.Cooldown,
			Source:     LimitSourceUpstream,
			Cause:      err,
		}
	}

	if trial {
		state.breaker.completeTrial(completedAt, err, g.settings.Cooldown)
	}
	return value, err
}

func (g *QueryGuard) waitForToken(ctx context.Context, limiter *rate.Limiter) error {
	now := g.now()
	reservation := limiter.ReserveN(now, 1)
	if !reservation.OK() {
		return &RateLimitError{
			RetryAfter: g.settings.Cooldown,
			Source:     LimitSourceLocal,
		}
	}

	delay := reservation.DelayFrom(now)
	if delay <= 0 {
		return nil
	}
	if delay > g.settings.MaxWait {
		reservation.CancelAt(now)
		return &RateLimitError{
			RetryAfter: delay,
			Source:     LimitSourceLocal,
		}
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		reservation.CancelAt(g.now())
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (g *QueryGuard) instance(instanceID string) *instanceState {
	g.mu.Lock()
	defer g.mu.Unlock()

	state := g.instances[instanceID]
	if state == nil {
		state = &instanceState{
			limiter: rate.NewLimiter(rate.Limit(g.settings.RatePerSecond), g.settings.Burst),
			breaker: circuitBreaker{state: CircuitClosed},
		}
		g.instances[instanceID] = state
	}
	return state
}

// RemoveInstance releases guard state after an instance has been deleted or
// logged out. Callers must not invoke it while new queries are being admitted.
func (g *QueryGuard) RemoveInstance(instanceID string) {
	g.mu.Lock()
	delete(g.instances, instanceID)
	g.mu.Unlock()
}

func (g *QueryGuard) Snapshot(instanceID string) (Snapshot, bool) {
	g.mu.Lock()
	state := g.instances[instanceID]
	g.mu.Unlock()
	if state == nil {
		return Snapshot{}, false
	}
	return state.breaker.snapshot(g.now()), true
}

func (b *circuitBreaker) allow(now time.Time) (allowed, trial bool, retryAfter time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case CircuitOpen:
		if now.Before(b.openUntil) {
			return false, false, b.openUntil.Sub(now)
		}
		b.state = CircuitHalfOpen
		b.trialInFlight = false
	case CircuitHalfOpen:
		// Keep the existing half-open state.
	default:
		return true, false, 0
	}

	if b.trialInFlight {
		return false, false, halfOpenRetryAfter
	}
	b.trialInFlight = true
	return true, true, 0
}

func (b *circuitBreaker) open(now time.Time, cooldown time.Duration) {
	b.mu.Lock()
	b.state = CircuitOpen
	b.openUntil = now.Add(cooldown)
	b.trialInFlight = false
	b.mu.Unlock()
}

func (b *circuitBreaker) abortTrial() {
	b.mu.Lock()
	if b.state == CircuitHalfOpen {
		b.trialInFlight = false
	}
	b.mu.Unlock()
}

func (b *circuitBreaker) completeTrial(now time.Time, queryErr error, cooldown time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.trialInFlight = false
	if queryErr == nil {
		b.state = CircuitClosed
		b.openUntil = time.Time{}
		return
	}
	b.state = CircuitOpen
	b.openUntil = now.Add(cooldown)
}

func (b *circuitBreaker) snapshot(now time.Time) Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

	state := b.state
	if state == CircuitOpen && !now.Before(b.openUntil) {
		state = CircuitHalfOpen
	}
	return Snapshot{CircuitState: state, OpenUntil: b.openUntil}
}

func isWhatsAppRateLimit(err error) bool {
	if errors.Is(err, whatsmeow.ErrIQRateOverLimit) {
		return true
	}
	var iqErr *whatsmeow.IQError
	return errors.As(err, &iqErr) && iqErr.Code == 429
}

// ResourceKey builds an order-independent, non-plaintext key for a normalized
// resource set. Callers remain responsible for JID or phone normalization.
func ResourceKey(resources ...string) string {
	items := append([]string(nil), resources...)
	sort.Strings(items)
	sum := sha256.Sum256([]byte(strings.Join(items, "\x00")))
	return fmt.Sprintf("%x", sum[:])
}

func makeFlightKey(instanceID, operation, resource string) string {
	sum := sha256.Sum256([]byte(instanceID + "\x00" + operation + "\x00" + resource))
	return fmt.Sprintf("%x", sum[:])
}
