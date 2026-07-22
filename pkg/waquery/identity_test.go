package waquery

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

func TestIdentityReadReturnsBoundedStaleCacheDuringBreakerCooldown(t *testing.T) {
	guard, _ := New(Settings{RatePerSecond: 100, Burst: 10, MaxWait: time.Second, Cooldown: time.Minute})
	resolver, _ := NewIdentityResolver(guard, IdentityCacheSettings{PositiveTTL: time.Second, NegativeTTL: time.Second, MaxEntries: 10})
	now := time.Unix(100, 0)
	resolver.now = func() time.Time { return now }
	var calls atomic.Int32
	query := func(_ context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
		calls.Add(1)
		return []types.IsOnWhatsAppResponse{{Query: phones[0], IsIn: true}}, nil
	}

	if _, err := resolver.Resolve(context.Background(), "instance-a", []string{"1"}, query); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	guard.ObserveError("instance-a", whatsmeow.ErrIQRateOverLimit)
	result, err := resolver.ResolveRead(context.Background(), "instance-a", []string{"1"}, query)
	if err != nil || !result.Stale || len(result.Responses) != 1 || result.Responses[0].Query != "1" || calls.Load() != 1 {
		t.Fatalf("ResolveRead() = %#v, %v, upstream calls=%d", result, err, calls.Load())
	}
}

func TestIdentityReadDoesNotReturnPartialStaleCache(t *testing.T) {
	guard, _ := New(Settings{RatePerSecond: 100, Burst: 10, MaxWait: time.Second, Cooldown: time.Minute})
	resolver, _ := NewIdentityResolver(guard, IdentityCacheSettings{PositiveTTL: time.Minute, NegativeTTL: time.Minute, MaxEntries: 10})
	query := func(_ context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
		return []types.IsOnWhatsAppResponse{{Query: phones[0], IsIn: true}}, nil
	}
	if _, err := resolver.Resolve(context.Background(), "instance-a", []string{"1"}, query); err != nil {
		t.Fatal(err)
	}
	guard.ObserveError("instance-a", whatsmeow.ErrIQRateOverLimit)
	result, err := resolver.ResolveRead(context.Background(), "instance-a", []string{"1", "2"}, query)
	var rateLimit *RateLimitError
	if !errors.As(err, &rateLimit) || result.Responses != nil {
		t.Fatalf("ResolveRead() = %#v, %v; want complete rate-limit failure", result, err)
	}
}

func TestIdentityReadFallsBackWhenLiveRefreshOpensBreaker(t *testing.T) {
	guard, _ := New(Settings{RatePerSecond: 100, Burst: 10, MaxWait: time.Second, Cooldown: time.Minute})
	resolver, _ := NewIdentityResolver(guard, IdentityCacheSettings{PositiveTTL: time.Second, NegativeTTL: time.Second, MaxEntries: 10})
	now := time.Unix(100, 0)
	resolver.now = func() time.Time { return now }
	seed := func(_ context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
		return []types.IsOnWhatsAppResponse{{Query: phones[0], IsIn: true}}, nil
	}
	if _, err := resolver.Resolve(context.Background(), "instance-a", []string{"1"}, seed); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	var calls atomic.Int32
	result, err := resolver.ResolveRead(context.Background(), "instance-a", []string{"1"}, func(context.Context, []string) ([]types.IsOnWhatsAppResponse, error) {
		calls.Add(1)
		return nil, whatsmeow.ErrIQRateOverLimit
	})
	if err != nil || !result.Stale || len(result.Responses) != 1 || calls.Load() != 1 {
		t.Fatalf("ResolveRead() = %#v, %v, upstream calls=%d", result, err, calls.Load())
	}
	if snapshot, ok := guard.Snapshot("instance-a"); !ok || snapshot.CircuitState != CircuitOpen {
		t.Fatalf("breaker snapshot = %#v, %v", snapshot, ok)
	}
}

func TestIdentityReadRejectsCacheBeyondStaleRetention(t *testing.T) {
	guard, _ := New(Settings{RatePerSecond: 100, Burst: 10, MaxWait: time.Second, Cooldown: time.Minute})
	resolver, _ := NewIdentityResolver(guard, IdentityCacheSettings{PositiveTTL: time.Second, NegativeTTL: time.Second, StaleTTL: time.Second, MaxEntries: 10})
	now := time.Unix(100, 0)
	resolver.now = func() time.Time { return now }
	seed := func(_ context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
		return []types.IsOnWhatsAppResponse{{Query: phones[0], IsIn: true}}, nil
	}
	if _, err := resolver.Resolve(context.Background(), "instance-a", []string{"1"}, seed); err != nil {
		t.Fatal(err)
	}
	now = now.Add(3 * time.Second)
	guard.ObserveError("instance-a", whatsmeow.ErrIQRateOverLimit)
	result, err := resolver.ResolveRead(context.Background(), "instance-a", []string{"1"}, seed)
	var rateLimit *RateLimitError
	if !errors.As(err, &rateLimit) || result.Responses != nil {
		t.Fatalf("ResolveRead() = %#v, %v; want rate limit after stale retention", result, err)
	}
}

func TestIdentityResolverCoalescesConcurrentCacheMisses(t *testing.T) {
	guard, _ := New(Settings{RatePerSecond: 100, Burst: 10, MaxWait: time.Second, Cooldown: time.Minute})
	resolver, _ := NewIdentityResolver(guard, DefaultIdentityCacheSettings())
	var calls atomic.Int32
	query := func(_ context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		return []types.IsOnWhatsAppResponse{{Query: phones[0], IsIn: true}}, nil
	}

	var waitGroup sync.WaitGroup
	waitGroup.Add(100)
	for range 100 {
		go func() {
			defer waitGroup.Done()
			result, err := resolver.Resolve(context.Background(), "instance-a", []string{"1"}, query)
			if err != nil || len(result) != 1 {
				t.Errorf("Resolve() = %#v, %v", result, err)
			}
		}()
	}
	waitGroup.Wait()
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls.Load())
	}
}

func TestIdentityResolverCachesPositiveAndNegativeResults(t *testing.T) {
	guard, _ := New(Settings{RatePerSecond: 100, Burst: 10, MaxWait: time.Second, Cooldown: time.Minute})
	resolver, _ := NewIdentityResolver(guard, IdentityCacheSettings{PositiveTTL: time.Minute, NegativeTTL: time.Minute, MaxEntries: 10})
	var calls atomic.Int32
	query := func(_ context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
		calls.Add(1)
		return []types.IsOnWhatsAppResponse{{Query: phones[0], IsIn: true}, {Query: phones[1], IsIn: false}}, nil
	}

	for range 2 {
		result, err := resolver.Resolve(context.Background(), "instance-a", []string{"1", "2"}, query)
		if err != nil || len(result) != 2 {
			t.Fatalf("Resolve() = %#v, %v", result, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls.Load())
	}
}

func TestIdentityResolverExpiresAndEvictsEntries(t *testing.T) {
	guard, _ := New(Settings{RatePerSecond: 100, Burst: 10, MaxWait: time.Second, Cooldown: time.Minute})
	resolver, _ := NewIdentityResolver(guard, IdentityCacheSettings{PositiveTTL: time.Second, NegativeTTL: time.Second, MaxEntries: 1})
	now := time.Unix(100, 0)
	resolver.now = func() time.Time { return now }
	var calls atomic.Int32
	query := func(_ context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
		calls.Add(1)
		return []types.IsOnWhatsAppResponse{{Query: phones[0], IsIn: true}}, nil
	}

	_, _ = resolver.Resolve(context.Background(), "instance-a", []string{"1"}, query)
	_, _ = resolver.Resolve(context.Background(), "instance-a", []string{"2"}, query)
	_, _ = resolver.Resolve(context.Background(), "instance-a", []string{"1"}, query)
	now = now.Add(2 * time.Second)
	_, _ = resolver.Resolve(context.Background(), "instance-a", []string{"1"}, query)
	if calls.Load() != 4 {
		t.Fatalf("upstream calls = %d, want 4 after eviction and expiry", calls.Load())
	}
}

func TestIdentityResolverRemoveInstance(t *testing.T) {
	guard, _ := New(Settings{RatePerSecond: 100, Burst: 10, MaxWait: time.Second, Cooldown: time.Minute})
	resolver, _ := NewIdentityResolver(guard, IdentityCacheSettings{PositiveTTL: time.Minute, NegativeTTL: time.Minute, MaxEntries: 10})
	var calls atomic.Int32
	query := func(_ context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
		calls.Add(1)
		return []types.IsOnWhatsAppResponse{{Query: phones[0], IsIn: true}}, nil
	}
	_, _ = resolver.Resolve(context.Background(), "instance-a", []string{"1"}, query)
	resolver.RemoveInstance("instance-a")
	_, _ = resolver.Resolve(context.Background(), "instance-a", []string{"1"}, query)
	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls.Load())
	}
}
