package outbound

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestTokenBucketIsIndependentPerInstance(t *testing.T) {
	guard, err := New(Settings{RatePerSecond: 1, Burst: 1, MaxWait: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.Wait(context.Background(), "instance-a", 1); err != nil {
		t.Fatal(err)
	}
	if err := guard.Wait(context.Background(), "instance-a", 1); err == nil {
		t.Fatal("instance-a exceeded its outbound rate")
	}
	if err := guard.Wait(context.Background(), "instance-b", 1); err != nil {
		t.Fatalf("instance-b was blocked by instance-a: %v", err)
	}
}

func TestTokenBucketBoundsConcurrentBurstAndReturnsRetryAfter(t *testing.T) {
	guard, _ := New(Settings{RatePerSecond: 1, Burst: 3, MaxWait: 0})
	const callers = 100
	var admitted int
	var mutex sync.Mutex
	var wait sync.WaitGroup
	wait.Add(callers)
	for index := 0; index < callers; index++ {
		go func() {
			defer wait.Done()
			err := guard.Wait(context.Background(), "instance-a", 1)
			if err == nil {
				mutex.Lock()
				admitted++
				mutex.Unlock()
				return
			}
			if retry, ok := RetryAfter(err); !ok || retry < time.Second {
				t.Errorf("rate limit error = %v retry=%v", err, retry)
			}
		}()
	}
	wait.Wait()
	if admitted != 3 {
		t.Fatalf("admitted = %d, want burst 3", admitted)
	}
}

func TestTokenBucketCancellationAndValidation(t *testing.T) {
	if _, err := New(Settings{}); err == nil {
		t.Fatal("invalid settings accepted")
	}
	guard, _ := New(Settings{RatePerSecond: 1, Burst: 1, MaxWait: 2 * time.Second})
	if err := guard.Wait(context.Background(), "instance-a", 1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := guard.Wait(ctx, "instance-a", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait error = %v", err)
	}
}
