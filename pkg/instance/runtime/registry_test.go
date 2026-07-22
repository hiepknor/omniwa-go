package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
)

func TestStaleGenerationCannotRemoveReplacement(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := NewRegistry[string](ctx)
	firstClient := &whatsmeow.Client{}
	secondClient := &whatsmeow.Client{}
	var firstCleanup atomic.Int32

	first, err := registry.Install("instance-a", firstClient, "first", func() { firstCleanup.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Install("instance-a", secondClient, "second", nil)
	if err != nil {
		t.Fatal(err)
	}

	if firstCleanup.Load() != 1 {
		t.Fatalf("replacement cleanup count=%d", firstCleanup.Load())
	}
	select {
	case <-first.Context.Done():
	default:
		t.Fatal("replaced runtime context is still active")
	}
	if registry.RemoveIfCurrent("instance-a", first.Generation) {
		t.Fatal("stale generation removed replacement")
	}
	current, ok := registry.Lookup("instance-a")
	if !ok || current.Generation != second.Generation || current.Client != secondClient || current.State != "second" {
		t.Fatalf("unexpected current runtime: %+v exists=%t", current, ok)
	}
	if !registry.RemoveIfCurrent("instance-a", second.Generation) || registry.RemoveIfCurrent("instance-a", second.Generation) {
		t.Fatal("current generation removal was not idempotent")
	}
}

func TestConcurrentReconnectsAreSingleFlightPerInstance(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := NewRegistry[struct{}](ctx)
	var calls atomic.Int32
	start := make(chan struct{})
	release := make(chan struct{})
	const callers = 100
	var wait sync.WaitGroup
	var ready sync.WaitGroup
	wait.Add(callers)
	ready.Add(callers)

	for range callers {
		go func() {
			defer wait.Done()
			ready.Done()
			<-start
			if err := registry.Reconnect("instance-a", func() error {
				calls.Add(1)
				<-release
				return nil
			}); err != nil {
				t.Errorf("Reconnect() error=%v", err)
			}
		}()
	}
	ready.Wait()
	close(start)
	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// Keep the leader in flight long enough for all released callers to join
	// the same key. The callback itself is the synchronization point under test.
	time.Sleep(100 * time.Millisecond)
	close(release)
	wait.Wait()

	if calls.Load() != 1 {
		t.Fatalf("reconnect callback calls=%d, want 1", calls.Load())
	}
}

func TestConcurrentStartsAreSingleFlightPerInstance(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := NewRegistry[struct{}](ctx)
	var calls atomic.Int32
	start := make(chan struct{})
	release := make(chan struct{})
	const callers = 100
	var wait sync.WaitGroup
	var ready sync.WaitGroup
	wait.Add(callers)
	ready.Add(callers)

	for range callers {
		go func() {
			defer wait.Done()
			ready.Done()
			<-start
			if err := registry.Start("instance-a", func() {
				calls.Add(1)
				<-release
			}); err != nil {
				t.Errorf("Start() error=%v", err)
			}
		}()
	}
	ready.Wait()
	close(start)
	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wait.Wait()

	if calls.Load() != 1 {
		t.Fatalf("start callback calls=%d, want 1", calls.Load())
	}
}

func TestParentCancellationStopsAllRuntimesOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	registry := NewRegistry[struct{}](ctx)
	var cleanups atomic.Int32
	first, _ := registry.Install("instance-a", &whatsmeow.Client{}, struct{}{}, func() { cleanups.Add(1) })
	second, _ := registry.Install("instance-b", &whatsmeow.Client{}, struct{}{}, func() { cleanups.Add(1) })

	cancel()
	select {
	case <-first.Context.Done():
	case <-time.After(time.Second):
		t.Fatal("first runtime was not canceled")
	}
	select {
	case <-second.Context.Done():
	case <-time.After(time.Second):
		t.Fatal("second runtime was not canceled")
	}
	registry.Close()
	if cleanups.Load() != 2 {
		t.Fatalf("cleanup count=%d, want 2", cleanups.Load())
	}
	if _, err := registry.Install("instance-c", &whatsmeow.Client{}, struct{}{}, nil); err == nil {
		t.Fatal("closed registry accepted a runtime")
	}
	called := false
	if err := registry.Start("instance-c", func() { called = true }); err == nil || called {
		t.Fatal("closed registry accepted a start callback")
	}
}

func TestReconnectsForDifferentInstancesDoNotBlockEachOther(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := NewRegistry[struct{}](ctx)
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- registry.Reconnect("instance-a", func() error {
			close(firstStarted)
			<-firstRelease
			return nil
		})
	}()
	<-firstStarted

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- registry.Reconnect("instance-b", func() error { return nil })
	}()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("instance-b reconnect was blocked by instance-a")
	}
	close(firstRelease)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}
