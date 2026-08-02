package bootstrap

import (
	"errors"
	"net/http"
	"sync"
)

var ErrStandbyRuntimeStarted = errors.New("standby runtime start has already been attempted")

// StandbyRuntime owns the process-role transitions and minimal HTTP control
// plane for a cold standby. It intentionally has no application dependencies.
type StandbyRuntime struct {
	state *ProcessState

	mu              sync.Mutex
	startAttempted  bool
	drainErr        error
	terminationErr  error
	drainOnce       sync.Once
	terminationOnce sync.Once
}

func NewStandbyRuntime(state *ProcessState) (*StandbyRuntime, error) {
	if state == nil {
		return nil, errors.New("process state is required")
	}
	return &StandbyRuntime{state: state}, nil
}

// Start transitions to standby before returning the control-plane handler.
// Construction is a single attempt so a stopped runtime cannot be restarted.
func (r *StandbyRuntime) Start() (http.Handler, error) {
	if r == nil {
		return nil, errors.New("standby runtime is required")
	}
	r.mu.Lock()
	if r.startAttempted {
		r.mu.Unlock()
		return nil, ErrStandbyRuntimeStarted
	}
	r.startAttempted = true
	r.mu.Unlock()
	if err := r.state.Transition(ProcessRoleStandby); err != nil {
		_ = r.BeginDrain()
		return nil, err
	}
	return standbyControlPlane{state: r.state}, nil
}

// BeginDrain makes the standby control plane unready before server shutdown.
func (r *StandbyRuntime) BeginDrain() error {
	if r == nil {
		return errors.New("standby runtime is required")
	}
	r.drainOnce.Do(func() {
		if r.state.Snapshot().Role != ProcessRoleTerminated {
			r.mu.Lock()
			r.drainErr = r.state.Transition(ProcessRoleDraining)
			r.mu.Unlock()
		}
	})
	r.mu.Lock()
	err := r.drainErr
	r.mu.Unlock()
	return err
}

// Stop is idempotent and marks the process terminated after draining. A cold
// standby owns no background work, so there is no worker wait boundary.
func (r *StandbyRuntime) Stop() error {
	if r == nil {
		return errors.New("standby runtime is required")
	}
	drainErr := r.BeginDrain()
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

type standbyControlPlane struct {
	state *ProcessState
}

func (h standbyControlPlane) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	switch request.URL.Path {
	case "/server/ok":
		writeRuntimeStatus(response, http.StatusOK, "ok", false)
	case "/server/live":
		if h.state.Live() {
			writeRuntimeStatus(response, http.StatusOK, "ok", true)
			return
		}
		writeRuntimeStatus(response, http.StatusServiceUnavailable, "not_live", true)
	case "/server/ready":
		if h.state.Ready() {
			writeRuntimeStatus(response, http.StatusOK, "ready", true)
			return
		}
		writeRuntimeStatus(response, http.StatusServiceUnavailable, "not_ready", true)
	default:
		http.NotFound(response, request)
	}
}

func writeRuntimeStatus(response http.ResponseWriter, status int, value string, noStore bool) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	if noStore {
		response.Header().Set("Cache-Control", "no-store")
	}
	response.WriteHeader(status)
	_, _ = response.Write([]byte(`{"status":"` + value + `"}`))
}
