package bootstrap

import (
	"sync"
	"testing"
)

type processObservation struct {
	mu          sync.Mutex
	states      []ProcessSnapshot
	transitions [][2]string
}

func (o *processObservation) ObserveProcessState(role string, _ bool, revision uint64) {
	o.mu.Lock()
	o.states = append(o.states, ProcessSnapshot{Role: ProcessRole(role), Revision: revision})
	o.mu.Unlock()
}

func (o *processObservation) ObserveProcessTransition(from, to string) {
	o.mu.Lock()
	o.transitions = append(o.transitions, [2]string{from, to})
	o.mu.Unlock()
}

func TestProcessStateActiveLifecycle(t *testing.T) {
	observer := &processObservation{}
	state := NewProcessState(observer)
	if !state.Live() || state.Ready() {
		t.Fatalf("starting state live=%t ready=%t", state.Live(), state.Ready())
	}
	for _, role := range []ProcessRole{ProcessRoleActive, ProcessRoleDraining, ProcessRoleTerminated} {
		if err := state.Transition(role); err != nil {
			t.Fatalf("transition to %q: %v", role, err)
		}
	}
	if state.Live() || state.Ready() {
		t.Fatalf("terminated state live=%t ready=%t", state.Live(), state.Ready())
	}
	if snapshot := state.Snapshot(); snapshot.Role != ProcessRoleTerminated || snapshot.Revision != 4 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if len(observer.states) != 4 || len(observer.transitions) != 3 {
		t.Fatalf("states=%+v transitions=%+v", observer.states, observer.transitions)
	}
}

func TestProcessStateStandbyPromotionLifecycle(t *testing.T) {
	state := NewProcessState(nil)
	for _, role := range []ProcessRole{ProcessRoleStandby, ProcessRolePromotionPending, ProcessRoleActive} {
		if err := state.Transition(role); err != nil {
			t.Fatalf("transition to %q: %v", role, err)
		}
	}
	if !state.Ready() {
		t.Fatal("promoted process must be ready")
	}
}

func TestProcessStateRejectsInvalidTransitions(t *testing.T) {
	state := NewProcessState(nil)
	if err := state.Transition(ProcessRoleTerminated); err == nil {
		t.Fatal("starting process must not transition directly to terminated")
	}
	if snapshot := state.Snapshot(); snapshot.Role != ProcessRoleStarting || snapshot.Revision != 1 {
		t.Fatalf("invalid transition changed state: %+v", snapshot)
	}
	if err := state.Transition(ProcessRole("instance-secret")); err == nil {
		t.Fatal("unbounded role must be rejected")
	}
}

func TestProcessStateTransitionMatrix(t *testing.T) {
	roles := []ProcessRole{
		ProcessRoleStarting, ProcessRoleStandby, ProcessRolePromotionPending,
		ProcessRoleActive, ProcessRoleDraining, ProcessRoleTerminated,
	}
	allowed := map[[2]ProcessRole]bool{
		{ProcessRoleStarting, ProcessRoleStandby}:          true,
		{ProcessRoleStarting, ProcessRoleActive}:           true,
		{ProcessRoleStarting, ProcessRoleDraining}:         true,
		{ProcessRoleStandby, ProcessRolePromotionPending}:  true,
		{ProcessRoleStandby, ProcessRoleDraining}:          true,
		{ProcessRolePromotionPending, ProcessRoleActive}:   true,
		{ProcessRolePromotionPending, ProcessRoleDraining}: true,
		{ProcessRoleActive, ProcessRoleDraining}:           true,
		{ProcessRoleDraining, ProcessRoleTerminated}:       true,
	}
	for _, current := range roles {
		for _, next := range roles {
			state := &ProcessState{snapshot: ProcessSnapshot{Role: current, Revision: 7}}
			err := state.Transition(next)
			wantAllowed := current == next || allowed[[2]ProcessRole{current, next}]
			if (err == nil) != wantAllowed {
				t.Fatalf("transition %q -> %q allowed=%t err=%v", current, next, wantAllowed, err)
			}
			wantRevision := uint64(7)
			if current != next && wantAllowed {
				wantRevision = 8
			}
			if snapshot := state.Snapshot(); snapshot.Revision != wantRevision {
				t.Fatalf("transition %q -> %q revision=%d want=%d", current, next, snapshot.Revision, wantRevision)
			}
		}
	}
}

func TestProcessStateConcurrentSnapshots(t *testing.T) {
	state := NewProcessState(nil)
	const readers = 64
	var wait sync.WaitGroup
	for index := 0; index < readers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				_ = state.Snapshot()
				_ = state.Live()
				_ = state.Ready()
			}
		}()
	}
	if err := state.Transition(ProcessRoleActive); err != nil {
		t.Fatal(err)
	}
	wait.Wait()
	if !state.Ready() {
		t.Fatal("state lost active transition")
	}
}

func TestNilProcessStateFailsClosed(t *testing.T) {
	var state *ProcessState
	if state.Live() || state.Ready() || state.Snapshot().Role != ProcessRoleTerminated {
		t.Fatal("nil state must be unavailable")
	}
	if err := state.Transition(ProcessRoleActive); err == nil {
		t.Fatal("nil state transition must fail")
	}
}
