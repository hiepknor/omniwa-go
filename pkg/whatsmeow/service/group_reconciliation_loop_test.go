package whatsmeow_service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunPeriodicGroupReconciliationRunsImmediatelyAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var calls atomic.Int32
	go func() {
		defer close(done)
		runPeriodicGroupReconciliation(ctx, 10*time.Millisecond, func(context.Context) {
			calls.Add(1)
		})
	}()

	waitForReconciliationCalls(t, &calls, 2)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("periodic reconciliation did not stop after cancellation")
	}
	stoppedAt := calls.Load()
	time.Sleep(30 * time.Millisecond)
	if got := calls.Load(); got != stoppedAt {
		t.Fatalf("reconciliation ran after cancellation: before=%d after=%d", stoppedAt, got)
	}
}

func TestRunPeriodicGroupReconciliationCanDisablePeriodicRuns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var calls atomic.Int32
	go func() {
		defer close(done)
		runPeriodicGroupReconciliation(ctx, 0, func(context.Context) {
			calls.Add(1)
		})
	}()

	waitForReconciliationCalls(t, &calls, 1)
	time.Sleep(30 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("disabled periodic reconciliation ran %d times, want one initial run", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disabled periodic reconciliation did not stop after cancellation")
	}
}

func TestStableGroupReconciliationInterval(t *testing.T) {
	base := 6 * time.Hour
	first := stableGroupReconciliationInterval("instance-a", base)
	second := stableGroupReconciliationInterval("instance-a", base)
	if first != second {
		t.Fatalf("jitter is not stable: first=%v second=%v", first, second)
	}
	if first < base-base/10 || first > base+base/10 {
		t.Fatalf("jittered interval %v is outside ten percent of %v", first, base)
	}
	if got := stableGroupReconciliationInterval("instance-a", 0); got != 0 {
		t.Fatalf("disabled interval = %v, want 0", got)
	}
}

func waitForReconciliationCalls(t *testing.T, calls *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for calls.Load() < want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := calls.Load(); got < want {
		t.Fatalf("reconciliation calls = %d, want at least %d", got, want)
	}
}
