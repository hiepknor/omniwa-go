package waquery

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
)

func testSettings() Settings {
	return Settings{
		RatePerSecond: 1000,
		Burst:         100,
		MaxWait:       time.Second,
		Cooldown:      time.Minute,
	}
}

func TestConcurrentIdenticalQueriesUseOneUpstreamCall(t *testing.T) {
	guard, err := New(testSettings())
	if err != nil {
		t.Fatal(err)
	}

	const callers = 100
	start := make(chan struct{})
	release := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(callers)
	var upstreamCalls atomic.Int32
	var callersWG sync.WaitGroup
	callersWG.Add(callers)
	errorsCh := make(chan error, callers)

	query := func(context.Context) (any, error) {
		upstreamCalls.Add(1)
		<-release
		return "shared", nil
	}

	for range callers {
		go func() {
			defer callersWG.Done()
			ready.Done()
			<-start
			value, err := guard.Do(context.Background(), "instance-a", "group_info", "group-1", query)
			if err == nil && value != "shared" {
				err = errors.New("unexpected shared value")
			}
			errorsCh <- err
		}()
	}

	ready.Wait()
	close(start)
	waitFor(t, time.Second, func() bool { return upstreamCalls.Load() == 1 })
	time.Sleep(25 * time.Millisecond)
	close(release)
	callersWG.Wait()
	close(errorsCh)

	for err := range errorsCh {
		if err != nil {
			t.Fatalf("guard.Do() error = %v", err)
		}
	}
	if got := upstreamCalls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
}

func TestRateLimitIsIsolatedPerInstance(t *testing.T) {
	settings := testSettings()
	settings.RatePerSecond = 1
	settings.Burst = 1
	settings.MaxWait = 0
	guard, err := New(settings)
	if err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	query := func(context.Context) (any, error) {
		calls.Add(1)
		return "ok", nil
	}

	if _, err := guard.Do(context.Background(), "instance-a", "group_info", "one", query); err != nil {
		t.Fatal(err)
	}
	_, err = guard.Do(context.Background(), "instance-a", "group_info", "two", query)
	assertRateLimitSource(t, err, LimitSourceLocal)
	if _, err := guard.Do(context.Background(), "instance-b", "group_info", "one", query); err != nil {
		t.Fatalf("instance B was blocked by instance A: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2", got)
	}
}

func TestUpstreamRateLimitOpensCircuitWithoutProbing(t *testing.T) {
	guard, err := New(testSettings())
	if err != nil {
		t.Fatal(err)
	}
	upstreamRateLimit := errors.New("upstream rate limit")
	guard.classifier = func(err error) bool { return errors.Is(err, upstreamRateLimit) }

	var calls atomic.Int32
	query := func(context.Context) (any, error) {
		calls.Add(1)
		return nil, upstreamRateLimit
	}

	_, err = guard.Do(context.Background(), "instance-a", "group_info", "first", query)
	assertRateLimitSource(t, err, LimitSourceUpstream)

	for i := 0; i < 20; i++ {
		_, err = guard.Do(context.Background(), "instance-a", "group_info", ResourceKey(string(rune(i))), query)
		assertRateLimitSource(t, err, LimitSourceCircuitOpen)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls during cooldown = %d, want 1", got)
	}

	snapshot, ok := guard.Snapshot("instance-a")
	if !ok || snapshot.CircuitState != CircuitOpen {
		t.Fatalf("snapshot = %+v, %v, want open circuit", snapshot, ok)
	}
}

func TestHalfOpenAllowsOneTrial(t *testing.T) {
	settings := testSettings()
	settings.Cooldown = time.Minute
	guard, err := New(settings)
	if err != nil {
		t.Fatal(err)
	}
	upstreamRateLimit := errors.New("upstream rate limit")
	guard.classifier = func(err error) bool { return errors.Is(err, upstreamRateLimit) }

	now := time.Unix(1_000, 0)
	guard.now = func() time.Time { return now }
	var calls atomic.Int32
	_, err = guard.Do(context.Background(), "instance-a", "group_info", "initial", func(context.Context) (any, error) {
		calls.Add(1)
		return nil, upstreamRateLimit
	})
	assertRateLimitSource(t, err, LimitSourceUpstream)
	now = now.Add(settings.Cooldown)

	const callers = 20
	start := make(chan struct{})
	release := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	var successes atomic.Int32
	var limited atomic.Int32

	for i := 0; i < callers; i++ {
		resource := ResourceKey(string(rune(i)))
		go func() {
			defer wg.Done()
			ready.Done()
			<-start
			_, err := guard.Do(context.Background(), "instance-a", "group_info", resource, func(context.Context) (any, error) {
				calls.Add(1)
				<-release
				return "ok", nil
			})
			if err == nil {
				successes.Add(1)
				return
			}
			var rateErr *RateLimitError
			if errors.As(err, &rateErr) && rateErr.Source == LimitSourceCircuitOpen {
				limited.Add(1)
			}
		}()
	}

	ready.Wait()
	close(start)
	waitFor(t, time.Second, func() bool { return calls.Load() == 2 })
	time.Sleep(25 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 2 {
		t.Fatalf("total upstream calls = %d, want initial plus one trial", got)
	}
	if successes.Load() != 1 || limited.Load() != callers-1 {
		t.Fatalf("successes = %d, limited = %d", successes.Load(), limited.Load())
	}
	snapshot, _ := guard.Snapshot("instance-a")
	if snapshot.CircuitState != CircuitClosed {
		t.Fatalf("circuit state = %s, want closed", snapshot.CircuitState)
	}
}

func TestCanceledWaiterDoesNotCancelSharedQuery(t *testing.T) {
	guard, err := New(testSettings())
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	query := func(context.Context) (any, error) {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release
		return "ok", nil
	}

	leaderDone := make(chan error, 1)
	go func() {
		_, err := guard.Do(context.Background(), "instance-a", "user_info", "user-1", query)
		leaderDone <- err
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := guard.Do(ctx, "instance-a", "user_info", "user-1", query); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter error = %v", err)
	}
	close(release)
	if err := <-leaderDone; err != nil {
		t.Fatalf("leader error = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
}

func TestRemoveInstanceResetsGuardState(t *testing.T) {
	settings := testSettings()
	settings.RatePerSecond = 1
	settings.Burst = 1
	settings.MaxWait = 0
	guard, err := New(settings)
	if err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	query := func(context.Context) (any, error) {
		calls.Add(1)
		return nil, nil
	}
	if _, err := guard.Do(context.Background(), "instance-a", "group_info", "one", query); err != nil {
		t.Fatal(err)
	}
	_, err = guard.Do(context.Background(), "instance-a", "group_info", "two", query)
	assertRateLimitSource(t, err, LimitSourceLocal)

	guard.RemoveInstance("instance-a")
	if _, ok := guard.Snapshot("instance-a"); ok {
		t.Fatal("removed instance still has guard state")
	}
	if _, err := guard.Do(context.Background(), "instance-a", "group_info", "three", query); err != nil {
		t.Fatalf("query after reset failed: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2", got)
	}
}

func TestRetryAfterSecondsRoundsUp(t *testing.T) {
	err := &RateLimitError{RetryAfter: 1500 * time.Millisecond, Source: LimitSourceLocal}
	if got := err.RetryAfterSeconds(); got != 2 {
		t.Fatalf("RetryAfterSeconds() = %d, want 2", got)
	}
	err.RetryAfter = 0
	if got := err.RetryAfterSeconds(); got != 1 {
		t.Fatalf("RetryAfterSeconds() minimum = %d, want 1", got)
	}
}

func TestWhatsAppRateLimitClassifierHandlesTypedAndWrappedErrors(t *testing.T) {
	if !isWhatsAppRateLimit(whatsmeow.ErrIQRateOverLimit) {
		t.Fatal("typed WhatsApp rate-limit error was not classified")
	}
	wrapped := fmt.Errorf("query failed: %w", whatsmeow.ErrIQRateOverLimit)
	if !isWhatsAppRateLimit(wrapped) {
		t.Fatal("wrapped WhatsApp rate-limit error was not classified")
	}
	if isWhatsAppRateLimit(errors.New("status 429 in an untyped string")) {
		t.Fatal("untyped error string must not be classified as upstream rate limit")
	}
}

func TestObserveMutationRateLimitOpensCircuitWithoutRunningQuery(t *testing.T) {
	guard, err := New(testSettings())
	if err != nil {
		t.Fatal(err)
	}

	err = guard.ObserveError("instance-a", whatsmeow.ErrIQRateOverLimit)
	assertRateLimitSource(t, err, LimitSourceUpstream)

	var calls atomic.Int32
	_, err = guard.Do(context.Background(), "instance-a", OperationGroupInfo, "group-a", func(context.Context) (any, error) {
		calls.Add(1)
		return "unexpected", nil
	})
	assertRateLimitSource(t, err, LimitSourceCircuitOpen)
	if calls.Load() != 0 {
		t.Fatalf("upstream calls = %d, want 0 during cooldown", calls.Load())
	}
}

func TestObserveNonRateLimitErrorDoesNotCreateState(t *testing.T) {
	guard, _ := New(testSettings())
	want := errors.New("mutation failed")
	if got := guard.ObserveError("instance-a", want); !errors.Is(got, want) {
		t.Fatalf("ObserveError() = %v, want original error", got)
	}
	if _, ok := guard.Snapshot("instance-a"); ok {
		t.Fatal("non-rate-limit mutation error created guard state")
	}
}

func TestSettingsRejectNonFiniteRate(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		settings := testSettings()
		settings.RatePerSecond = value
		if _, err := New(settings); err == nil {
			t.Fatalf("New() unexpectedly accepted rate %v", value)
		}
	}
}

func TestResourceKeyIsOrderIndependent(t *testing.T) {
	if ResourceKey("a", "b", "c") != ResourceKey("c", "a", "b") {
		t.Fatal("ResourceKey must be order independent")
	}
	if ResourceKey("a", "b") == ResourceKey("a", "c") {
		t.Fatal("ResourceKey collided for different resources")
	}
}

func assertRateLimitSource(t *testing.T, err error, source LimitSource) {
	t.Helper()
	var rateErr *RateLimitError
	if !errors.As(err, &rateErr) {
		t.Fatalf("error = %v, want RateLimitError", err)
	}
	if rateErr.Source != source {
		t.Fatalf("rate limit source = %s, want %s", rateErr.Source, source)
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not met before timeout")
		}
		time.Sleep(time.Millisecond)
	}
}
