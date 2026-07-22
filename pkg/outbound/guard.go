package outbound

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type Settings struct {
	RatePerSecond float64
	Burst         int
	MaxWait       time.Duration
}

func (s Settings) Validate() error {
	if s.RatePerSecond <= 0 || math.IsNaN(s.RatePerSecond) || math.IsInf(s.RatePerSecond, 0) || s.Burst <= 0 || s.MaxWait < 0 {
		return errors.New("outbound rate and burst must be positive and max wait must be non-negative")
	}
	return nil
}

type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string { return "outbound message rate limited" }

func RetryAfter(err error) (time.Duration, bool) {
	var rateLimit *RateLimitError
	if !errors.As(err, &rateLimit) {
		return 0, false
	}
	return rateLimit.RetryAfter, true
}

type Guard interface {
	Wait(context.Context, string, int) error
	RemoveInstance(string)
}

type TokenBucket struct {
	settings Settings
	now      func() time.Time
	mu       sync.Mutex
	limits   map[string]*rate.Limiter
}

func New(settings Settings) (*TokenBucket, error) {
	if err := settings.Validate(); err != nil {
		return nil, fmt.Errorf("invalid outbound guard settings: %w", err)
	}
	return &TokenBucket{settings: settings, now: time.Now, limits: make(map[string]*rate.Limiter)}, nil
}

func (g *TokenBucket) Wait(ctx context.Context, instanceID string, cost int) error {
	if g == nil || ctx == nil || strings.TrimSpace(instanceID) == "" || cost <= 0 || cost > g.settings.Burst {
		return errors.New("outbound guard context, instance, and bounded cost are required")
	}
	now := g.now()
	reservation := g.limiter(instanceID).ReserveN(now, cost)
	if !reservation.OK() {
		return errors.New("outbound cost exceeds limiter capacity")
	}
	delay := reservation.DelayFrom(now)
	if delay > g.settings.MaxWait {
		reservation.CancelAt(now)
		return &RateLimitError{RetryAfter: positiveRetryAfter(delay)}
	}
	if delay <= 0 {
		return nil
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

func (g *TokenBucket) RemoveInstance(instanceID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	delete(g.limits, instanceID)
	g.mu.Unlock()
}

func (g *TokenBucket) limiter(instanceID string) *rate.Limiter {
	g.mu.Lock()
	defer g.mu.Unlock()
	limiter := g.limits[instanceID]
	if limiter == nil {
		limiter = rate.NewLimiter(rate.Limit(g.settings.RatePerSecond), g.settings.Burst)
		g.limits[instanceID] = limiter
	}
	return limiter
}

func positiveRetryAfter(delay time.Duration) time.Duration {
	seconds := math.Ceil(delay.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	return time.Duration(seconds) * time.Second
}
