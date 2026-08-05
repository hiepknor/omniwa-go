package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

type shutdownRecorder struct {
	shutdown func(context.Context) error
	close    func() error
}

func TestShutdownActiveServerBoundsHTTPAndWorkerPhases(t *testing.T) {
	state := NewProcessState(nil)
	runtime, err := NewActiveRuntime(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	_, err = runtime.Start(func(context.Context, *Supervisor) (http.Handler, error) {
		if err := runtime.supervisor.Start("blocked", func(context.Context) error {
			<-release
			return nil
		}); err != nil {
			return nil, err
		}
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	forcedClosed := false
	server := shutdownRecorder{shutdown: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}, close: func() error {
		forcedClosed = true
		return nil
	}}
	err = ShutdownActiveServer(server, runtime, 10*time.Millisecond, 10*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	if !forcedClosed {
		t.Fatal("timed out HTTP server was not force closed")
	}
	if state.Snapshot().Role != ProcessRoleDraining {
		t.Fatalf("role=%q", state.Snapshot().Role)
	}
	close(release)
	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (s shutdownRecorder) Shutdown(ctx context.Context) error { return s.shutdown(ctx) }
func (s shutdownRecorder) Close() error {
	if s.close == nil {
		return nil
	}
	return s.close()
}

func TestShutdownActiveServerDrainsHTTPBeforeCancellingWorkers(t *testing.T) {
	state := NewProcessState(nil)
	runtime, err := NewActiveRuntime(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	workerCancelled := make(chan struct{})
	_, err = runtime.Start(func(ctx context.Context, supervisor *Supervisor) (http.Handler, error) {
		if err := supervisor.Start("worker", func(ctx context.Context) error {
			<-ctx.Done()
			close(workerCancelled)
			return nil
		}); err != nil {
			return nil, err
		}
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	httpObservedWorkerRunning := false
	server := shutdownRecorder{shutdown: func(context.Context) error {
		if state.Ready() {
			t.Fatal("runtime remained ready during HTTP drain")
		}
		select {
		case <-workerCancelled:
			t.Fatal("worker was cancelled before HTTP drain")
		default:
			mu.Lock()
			httpObservedWorkerRunning = true
			mu.Unlock()
		}
		return nil
	}}
	if err := ShutdownActiveServer(server, runtime, time.Second, time.Second); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	observed := httpObservedWorkerRunning
	mu.Unlock()
	if !observed {
		t.Fatal("HTTP drain was not observed")
	}
	select {
	case <-workerCancelled:
	default:
		t.Fatal("worker was not cancelled after HTTP drain")
	}
	if state.Snapshot().Role != ProcessRoleTerminated {
		t.Fatalf("role=%q", state.Snapshot().Role)
	}
}

func TestBeginDrainDoesNotCancelWorkers(t *testing.T) {
	state := NewProcessState(nil)
	runtime, err := NewActiveRuntime(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := make(chan struct{})
	_, err = runtime.Start(func(ctx context.Context, supervisor *Supervisor) (http.Handler, error) {
		_ = supervisor.Start("worker", func(ctx context.Context) error { <-ctx.Done(); close(cancelled); return nil })
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.BeginDrain(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
		t.Fatal("drain cancelled worker")
	default:
	}
	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
