package waquery

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

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
