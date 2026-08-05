package server_service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type dependencyObserverStub struct {
	mu           sync.Mutex
	observations []DependencyHealth
}

func (o *dependencyObserverStub) ObserveDependencyHealth(name DependencyName, status DependencyStatus, _ time.Time) {
	o.mu.Lock()
	o.observations = append(o.observations, DependencyHealth{Name: name, Status: status})
	o.mu.Unlock()
}

func TestDependencyHealthRegistryIsBoundedSortedAndKeepsLastSuccess(t *testing.T) {
	observer := &dependencyObserverStub{}
	registry, err := NewDependencyHealthRegistry(observer, DependencyRabbitMQ, DependencyUsersDatabase)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC)
	registry.now = func() time.Time { return now }
	if err := registry.Observe(DependencyUsersDatabase, nil, false); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := registry.Observe(DependencyUsersDatabase, errors.New("secret database detail"), true); err != nil {
		t.Fatal(err)
	}
	snapshot := registry.Snapshot()
	if len(snapshot) != 2 || snapshot[0].Name != DependencyRabbitMQ || snapshot[0].Status != DependencyUnknown ||
		snapshot[1].Name != DependencyUsersDatabase || snapshot[1].Status != DependencyUnavailable || snapshot[1].ErrorCode != "probe_timeout" ||
		snapshot[1].LastSuccessAt == nil || !snapshot[1].LastSuccessAt.Equal(now.Add(-time.Minute)) || snapshot[1].CheckedAt == nil || !snapshot[1].CheckedAt.Equal(now) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot[1].ErrorCode == "secret database detail" {
		t.Fatal("dependency health exposed a raw error")
	}
	snapshot[1].ErrorCode = "modified"
	if registry.Snapshot()[1].ErrorCode != "probe_timeout" {
		t.Fatal("snapshot mutation changed registry state")
	}
}

func TestDependencyHealthRegistryRejectsUnknownAndDuplicateNames(t *testing.T) {
	if _, err := NewDependencyHealthRegistry(nil, "unbounded"); err == nil {
		t.Fatal("registry accepted an unknown dependency")
	}
	if _, err := NewDependencyHealthRegistry(nil, DependencyRabbitMQ, DependencyRabbitMQ); err == nil {
		t.Fatal("registry accepted a duplicate dependency")
	}
	registry, _ := NewDependencyHealthRegistry(nil, DependencyRabbitMQ)
	if err := registry.Observe(DependencyUsersDatabase, nil, false); err == nil {
		t.Fatal("registry accepted an unregistered dependency")
	}
}

func TestDependencyProbeWorkerObservesSuccessTimeoutAndCancellation(t *testing.T) {
	registry, _ := NewDependencyHealthRegistry(nil, DependencyRabbitMQ)
	worker, err := NewDependencyProbeWorker(DependencyRabbitMQ, func(context.Context) error { return nil }, registry, time.Minute, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	deadline := time.After(time.Second)
	for registry.Snapshot()[0].Status == DependencyUnknown {
		select {
		case <-deadline:
			t.Fatal("successful probe did not publish health")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := registry.Snapshot()[0]; got.Status != DependencyHealthy {
		t.Fatalf("successful probe = %#v", got)
	}

	registry, _ = NewDependencyHealthRegistry(nil, DependencyRabbitMQ)
	worker, _ = NewDependencyProbeWorker(DependencyRabbitMQ, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}, registry, time.Second, time.Millisecond)
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	deadline = time.After(time.Second)
	for registry.Snapshot()[0].Status == DependencyUnknown {
		select {
		case <-deadline:
			t.Fatal("timed probe did not publish health")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := registry.Snapshot()[0]; got.Status != DependencyUnavailable || got.ErrorCode != "probe_timeout" {
		t.Fatalf("timed probe = %#v", got)
	}
}

func TestDependencyProbeWorkerRejectsUnsafeSettings(t *testing.T) {
	registry, _ := NewDependencyHealthRegistry(nil, DependencyRabbitMQ)
	probe := func(context.Context) error { return nil }
	for _, settings := range []struct {
		name     DependencyName
		interval time.Duration
		timeout  time.Duration
	}{
		{"unknown", time.Second, time.Millisecond},
		{DependencyRabbitMQ, 0, time.Millisecond},
		{DependencyRabbitMQ, time.Second, time.Second},
		{DependencyRabbitMQ, 11 * time.Minute, time.Second},
	} {
		if _, err := NewDependencyProbeWorker(settings.name, probe, registry, settings.interval, settings.timeout); err == nil {
			t.Fatalf("accepted settings %#v", settings)
		}
	}
}

func TestDependencyProbeWorkerDoesNotPublishShutdownCancellation(t *testing.T) {
	registry, _ := NewDependencyHealthRegistry(nil, DependencyRabbitMQ)
	started := make(chan struct{})
	worker, _ := NewDependencyProbeWorker(DependencyRabbitMQ, func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}, registry, time.Second, 100*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	<-started
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := registry.Snapshot()[0]; got.Status != DependencyUnknown || got.CheckedAt != nil {
		t.Fatalf("shutdown cancellation published false dependency failure: %#v", got)
	}
}
