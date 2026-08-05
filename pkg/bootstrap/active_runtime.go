package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"sync"
)

var (
	ErrActiveRuntimeStarted  = errors.New("active runtime start has already been attempted")
	ErrActiveRuntimeDraining = errors.New("active runtime is draining")
)

// ActiveBuilder constructs the HTTP handler and registers all active-only
// background work with the supplied supervisor.
type ActiveBuilder func(context.Context, *Supervisor) (http.Handler, error)

// ActiveRuntime owns the cancellable context, background supervisor, and
// process-role transitions for the active application lifecycle.
type ActiveRuntime struct {
	ctx        context.Context
	cancel     context.CancelFunc
	supervisor *Supervisor
	state      *ProcessState

	mu              sync.Mutex
	startAttempted  bool
	draining        bool
	drainErr        error
	terminationErr  error
	drainOnce       sync.Once
	terminationOnce sync.Once
}

func NewActiveRuntime(parent context.Context, state *ProcessState, reporter ErrorReporter) (*ActiveRuntime, error) {
	if state == nil {
		return nil, errors.New("process state is required")
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &ActiveRuntime{
		ctx:        ctx,
		cancel:     cancel,
		supervisor: NewSupervisor(ctx, reporter),
		state:      state,
	}, nil
}

// Start invokes the builder at most once and marks the process active only
// after construction succeeds. A failed or interrupted build starts draining.
func (r *ActiveRuntime) Start(builder ActiveBuilder) (http.Handler, error) {
	if r == nil || builder == nil {
		return nil, errors.New("active runtime and builder are required")
	}

	r.mu.Lock()
	if r.draining {
		r.mu.Unlock()
		return nil, ErrActiveRuntimeDraining
	}
	if r.startAttempted {
		r.mu.Unlock()
		return nil, ErrActiveRuntimeStarted
	}
	r.startAttempted = true
	r.mu.Unlock()

	if err := r.ctx.Err(); err != nil {
		_ = r.BeginDrain()
		return nil, err
	}
	handler, err := builder(r.ctx, r.supervisor)
	if err != nil {
		_ = r.BeginDrain()
		return nil, err
	}
	if handler == nil {
		_ = r.BeginDrain()
		return nil, errors.New("active runtime builder returned a nil handler")
	}
	if err := r.state.Transition(ProcessRoleActive); err != nil {
		_ = r.BeginDrain()
		return nil, err
	}
	return handler, nil
}

// BeginDrain makes the process unready before cancellation and seals worker
// registration. It is safe to call repeatedly and concurrently.
func (r *ActiveRuntime) BeginDrain() error {
	if r == nil {
		return errors.New("active runtime is required")
	}
	r.drainOnce.Do(func() {
		r.mu.Lock()
		r.draining = true
		r.mu.Unlock()

		if r.state.Snapshot().Role != ProcessRoleTerminated {
			transitionErr := r.state.Transition(ProcessRoleDraining)
			r.mu.Lock()
			r.drainErr = transitionErr
			r.mu.Unlock()
		}
		r.supervisor.Seal()
	})
	r.mu.Lock()
	err := r.drainErr
	r.mu.Unlock()
	return err
}

// Stop begins draining, waits within the caller's deadline, and marks the
// process terminated only after every supervised worker has exited. A caller
// may retry Stop with a new context after a timeout.
func (r *ActiveRuntime) Stop(ctx context.Context) error {
	if r == nil {
		return errors.New("active runtime is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	drainErr := r.BeginDrain()
	r.cancel()
	stopped := r.supervisor.Stopped()
	select {
	case <-stopped:
		return r.finishTermination(drainErr)
	default:
	}
	select {
	case <-stopped:
		return r.finishTermination(drainErr)
	case <-ctx.Done():
		select {
		case <-stopped:
			return r.finishTermination(drainErr)
		default:
			return errors.Join(drainErr, ctx.Err())
		}
	}
}

func (r *ActiveRuntime) finishTermination(drainErr error) error {
	r.terminationOnce.Do(func() {
		transitionErr := r.state.Transition(ProcessRoleTerminated)
		r.mu.Lock()
		r.terminationErr = transitionErr
		r.mu.Unlock()
	})
	r.mu.Lock()
	terminationErr := r.terminationErr
	r.mu.Unlock()
	return errors.Join(drainErr, terminationErr)
}
