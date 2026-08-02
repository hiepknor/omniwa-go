package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestActiveRuntimeStartAndStop(t *testing.T) {
	state := NewProcessState(nil)
	runtime, err := NewActiveRuntime(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	workerStopped := make(chan struct{})
	handler, err := runtime.Start(func(ctx context.Context, supervisor *Supervisor) (http.Handler, error) {
		if err := supervisor.Start("worker", func(ctx context.Context) error {
			<-ctx.Done()
			close(workerStopped)
			return nil
		}); err != nil {
			return nil, err
		}
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if handler == nil || state.Snapshot().Role != ProcessRoleActive {
		t.Fatalf("handler=%v role=%q", handler, state.Snapshot().Role)
	}
	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-workerStopped:
	default:
		t.Fatal("worker was not stopped")
	}
	if state.Snapshot().Role != ProcessRoleTerminated {
		t.Fatalf("role=%q", state.Snapshot().Role)
	}
}

func TestActiveRuntimeBecomesUnreadyBeforeWorkerCancellation(t *testing.T) {
	state := NewProcessState(nil)
	runtime, err := NewActiveRuntime(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	roleAtCancellation := make(chan ProcessRole, 1)
	_, err = runtime.Start(func(ctx context.Context, supervisor *Supervisor) (http.Handler, error) {
		if err := supervisor.Start("observer", func(ctx context.Context) error {
			<-ctx.Done()
			roleAtCancellation <- state.Snapshot().Role
			return nil
		}); err != nil {
			return nil, err
		}
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if role := <-roleAtCancellation; role != ProcessRoleDraining {
		t.Fatalf("role at cancellation=%q", role)
	}
}

func TestActiveRuntimeStartIsSingleAttempt(t *testing.T) {
	state := NewProcessState(nil)
	runtime, err := NewActiveRuntime(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	var builds atomic.Int32
	var successes atomic.Int32
	var wait sync.WaitGroup
	start := make(chan struct{})
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, startErr := runtime.Start(func(context.Context, *Supervisor) (http.Handler, error) {
				builds.Add(1)
				return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
			})
			if startErr == nil {
				successes.Add(1)
				return
			}
			if !errors.Is(startErr, ErrActiveRuntimeStarted) {
				t.Errorf("unexpected start error: %v", startErr)
			}
		}()
	}
	close(start)
	wait.Wait()
	if builds.Load() != 1 || successes.Load() != 1 {
		t.Fatalf("builds=%d successes=%d", builds.Load(), successes.Load())
	}
	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestActiveRuntimeFailedBuildDrainsWorkers(t *testing.T) {
	state := NewProcessState(nil)
	runtime, err := NewActiveRuntime(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("build failed")
	workerStopped := make(chan struct{})
	_, err = runtime.Start(func(ctx context.Context, supervisor *Supervisor) (http.Handler, error) {
		if err := supervisor.Start("worker", func(ctx context.Context) error {
			<-ctx.Done()
			close(workerStopped)
			return nil
		}); err != nil {
			return nil, err
		}
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error=%v", err)
	}
	if state.Snapshot().Role != ProcessRoleDraining {
		t.Fatalf("role=%q", state.Snapshot().Role)
	}
	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-workerStopped:
	default:
		t.Fatal("worker was not stopped")
	}
}

func TestActiveRuntimeStopTimeoutRemainsDrainingAndCanRetry(t *testing.T) {
	state := NewProcessState(nil)
	runtime, err := NewActiveRuntime(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	_, err = runtime.Start(func(ctx context.Context, supervisor *Supervisor) (http.Handler, error) {
		if err := supervisor.Start("blocked", func(context.Context) error {
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
	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := runtime.Stop(stopCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	if state.Snapshot().Role != ProcessRoleDraining {
		t.Fatalf("role=%q", state.Snapshot().Role)
	}
	close(release)
	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state.Snapshot().Role != ProcessRoleTerminated {
		t.Fatalf("role=%q", state.Snapshot().Role)
	}
}

func TestActiveRuntimeDrainSealsWorkerRegistration(t *testing.T) {
	state := NewProcessState(nil)
	runtime, err := NewActiveRuntime(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.BeginDrain(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.supervisor.Start("late", func(context.Context) error { return nil }); err == nil {
		t.Fatal("late worker registration succeeded")
	}
	if _, err := runtime.Start(func(context.Context, *Supervisor) (http.Handler, error) {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
	}); !errors.Is(err, ErrActiveRuntimeDraining) {
		t.Fatalf("error=%v", err)
	}
	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestActiveRuntimeConcurrentDrainIsIdempotent(t *testing.T) {
	state := NewProcessState(nil)
	runtime, err := NewActiveRuntime(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := runtime.BeginDrain(); err != nil {
				t.Errorf("drain: %v", err)
			}
		}()
	}
	wait.Wait()
	snapshot := state.Snapshot()
	if snapshot.Role != ProcessRoleDraining || snapshot.Revision != 2 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
