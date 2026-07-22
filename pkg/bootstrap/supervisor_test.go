package bootstrap

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSupervisorWaitsForCancellationAndReportsErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	reported := map[string]error{}
	supervisor := NewSupervisor(ctx, func(name string, err error) {
		mu.Lock()
		defer mu.Unlock()
		reported[name] = err
	})
	if err := supervisor.Start("long-running", func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	workerErr := errors.New("worker failed")
	if err := supervisor.Start("failing", func(context.Context) error { return workerErr }); err != nil {
		t.Fatal(err)
	}
	cancel()
	supervisor.Wait()
	mu.Lock()
	defer mu.Unlock()
	if !errors.Is(reported["failing"], workerErr) {
		t.Fatalf("reported errors = %#v", reported)
	}
}

func TestSupervisorRejectsRegistrationAfterWaitStarts(t *testing.T) {
	supervisor := NewSupervisor(context.Background(), nil)
	supervisor.Wait()
	if err := supervisor.Start("late", func(context.Context) error { return nil }); err == nil {
		t.Fatal("late worker registration was accepted")
	}
	if err := supervisor.Start("", func(context.Context) error { return nil }); err == nil {
		t.Fatal("empty worker name was accepted")
	}
}

func TestSupervisorConcurrentWaitIsSafe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	supervisor := NewSupervisor(ctx, nil)
	if err := supervisor.Start("worker", func(ctx context.Context) error { <-ctx.Done(); return nil }); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	wait.Add(2)
	go func() { defer wait.Done(); supervisor.Wait() }()
	go func() { defer wait.Done(); supervisor.Wait() }()
	cancel()
	done := make(chan struct{})
	go func() { wait.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("concurrent Wait calls did not return")
	}
}
