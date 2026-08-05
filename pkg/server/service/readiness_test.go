package server_service

import (
	"errors"
	"testing"
	"time"
)

type readinessRuntime struct{ ready bool }

func (r readinessRuntime) Live() bool  { return true }
func (r readinessRuntime) Ready() bool { return r.ready }

func TestReadinessHealthPreservesRuntimeOnlyDefault(t *testing.T) {
	registry, err := NewDependencyHealthRegistry(nil, DependencyUsersDatabase)
	if err != nil {
		t.Fatal(err)
	}
	health := NewReadinessHealth(readinessRuntime{ready: true}, registry, ReadinessRequirements{})
	if !health.Ready() {
		t.Fatal("default requirements must preserve runtime-only readiness")
	}
}

func TestReadinessHealthAppliesFailureAndRecoveryHysteresis(t *testing.T) {
	registry, err := NewDependencyHealthRegistry(nil, DependencyUsersDatabase)
	if err != nil {
		t.Fatal(err)
	}
	health := NewReadinessHealth(readinessRuntime{ready: true}, registry, ReadinessRequirements{UsersDatabase: true})
	if health.Ready() {
		t.Fatal("unknown dependency must not be ready")
	}
	if err := registry.Observe(DependencyUsersDatabase, nil, false); err != nil {
		t.Fatal(err)
	}
	if health.Ready() {
		t.Fatal("one success must not recover readiness")
	}
	_ = registry.Observe(DependencyUsersDatabase, nil, false)
	if !health.Ready() {
		t.Fatal("two successes must recover readiness")
	}
	for i := 0; i < 2; i++ {
		_ = registry.Observe(DependencyUsersDatabase, errors.New("down"), false)
	}
	if !health.Ready() {
		t.Fatal("fewer than three failures must retain readiness")
	}
	_ = registry.Observe(DependencyUsersDatabase, errors.New("down"), false)
	if health.Ready() {
		t.Fatal("three failures must remove readiness")
	}
}

func TestReadinessHealthRejectsStaleRequiredDependency(t *testing.T) {
	registry, err := NewDependencyHealthRegistry(nil, DependencyUsersDatabase)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	registry.now = func() time.Time { return base }
	_ = registry.Observe(DependencyUsersDatabase, nil, false)
	_ = registry.Observe(DependencyUsersDatabase, nil, false)
	health := NewReadinessHealth(readinessRuntime{ready: true}, registry, ReadinessRequirements{UsersDatabase: true, MaxStaleness: time.Minute})
	health.now = func() time.Time { return base.Add(time.Minute + time.Second) }
	if health.Ready() {
		t.Fatal("stale dependency observation must not be ready")
	}
}
