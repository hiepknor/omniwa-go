package bootstrap

import (
	"errors"
	"sync"
)

type ProcessRole string

const (
	ProcessRoleStarting         ProcessRole = "starting"
	ProcessRoleStandby          ProcessRole = "standby"
	ProcessRolePromotionPending ProcessRole = "promotion_pending"
	ProcessRoleActive           ProcessRole = "active"
	ProcessRoleDraining         ProcessRole = "draining"
	ProcessRoleTerminated       ProcessRole = "terminated"
)

type ProcessSnapshot struct {
	Role     ProcessRole
	Revision uint64
}

type ProcessStateObserver interface {
	ObserveProcessState(role string, ready bool, revision uint64)
	ObserveProcessTransition(from, to string)
}

// ProcessState owns the bounded application-role state machine. It is process
// state only; database ownership and infrastructure fencing remain separate
// safety boundaries.
type ProcessState struct {
	mu       sync.RWMutex
	snapshot ProcessSnapshot
	observer ProcessStateObserver
}

func NewProcessState(observer ProcessStateObserver) *ProcessState {
	state := &ProcessState{
		snapshot: ProcessSnapshot{Role: ProcessRoleStarting, Revision: 1},
		observer: observer,
	}
	state.observe(state.snapshot)
	return state
}

func (s *ProcessState) Snapshot() ProcessSnapshot {
	if s == nil {
		return ProcessSnapshot{Role: ProcessRoleTerminated}
	}
	s.mu.RLock()
	snapshot := s.snapshot
	s.mu.RUnlock()
	return snapshot
}

func (s *ProcessState) Live() bool {
	return s != nil && s.Snapshot().Role != ProcessRoleTerminated
}

func (s *ProcessState) Ready() bool {
	return s != nil && s.Snapshot().Role == ProcessRoleActive
}

func (s *ProcessState) Transition(next ProcessRole) error {
	if s == nil {
		return errors.New("process state is required")
	}
	s.mu.Lock()
	current := s.snapshot
	if current.Role == next {
		s.mu.Unlock()
		return nil
	}
	if !validProcessTransition(current.Role, next) {
		s.mu.Unlock()
		return errors.New("invalid process role transition")
	}
	s.snapshot = ProcessSnapshot{Role: next, Revision: current.Revision + 1}
	updated := s.snapshot
	s.mu.Unlock()

	if s.observer != nil {
		s.observer.ObserveProcessTransition(string(current.Role), string(next))
	}
	s.observe(updated)
	return nil
}

func (s *ProcessState) observe(snapshot ProcessSnapshot) {
	if s != nil && s.observer != nil {
		s.observer.ObserveProcessState(string(snapshot.Role), snapshot.Role == ProcessRoleActive, snapshot.Revision)
	}
}

func validProcessTransition(current, next ProcessRole) bool {
	switch current {
	case ProcessRoleStarting:
		return next == ProcessRoleStandby || next == ProcessRoleActive || next == ProcessRoleDraining
	case ProcessRoleStandby:
		return next == ProcessRolePromotionPending || next == ProcessRoleDraining
	case ProcessRolePromotionPending:
		return next == ProcessRoleActive || next == ProcessRoleDraining
	case ProcessRoleActive:
		return next == ProcessRoleDraining
	case ProcessRoleDraining:
		return next == ProcessRoleTerminated
	default:
		return false
	}
}
