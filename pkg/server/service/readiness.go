package server_service

import "time"

const DefaultReadinessMaxStaleness = 45 * time.Second

type runtimeHealth interface {
	Live() bool
	Ready() bool
}

type ReadinessRequirements struct {
	UsersDatabase bool
	EventDelivery bool
	MinIO         bool
	MaxStaleness  time.Duration
}

// ReadinessHealth combines the process-role state machine with opt-in,
// hysteresis-stabilized dependency requirements.
type ReadinessHealth struct {
	runtime      runtimeHealth
	dependencies *DependencyHealthRegistry
	requirements ReadinessRequirements
	now          func() time.Time
}

func NewReadinessHealth(runtime runtimeHealth, dependencies *DependencyHealthRegistry, requirements ReadinessRequirements) *ReadinessHealth {
	if requirements.MaxStaleness <= 0 {
		requirements.MaxStaleness = DefaultReadinessMaxStaleness
	}
	return &ReadinessHealth{runtime: runtime, dependencies: dependencies, requirements: requirements, now: time.Now}
}

func (h *ReadinessHealth) Live() bool {
	return h != nil && h.runtime != nil && h.runtime.Live()
}

func (h *ReadinessHealth) Ready() bool {
	if h == nil || h.runtime == nil || !h.runtime.Ready() {
		return false
	}
	required := h.requiredNames()
	if len(required) == 0 {
		return true
	}
	if h.dependencies == nil || h.now == nil {
		return false
	}
	snapshot := h.dependencies.Snapshot()
	byName := make(map[DependencyName]DependencyHealth, len(snapshot))
	for _, health := range snapshot {
		byName[health.Name] = health
	}
	now := h.now().UTC()
	for name := range required {
		health, ok := byName[name]
		if !ok || health.CheckedAt == nil || now.Sub(*health.CheckedAt) > h.requirements.MaxStaleness || !health.stableHealthy {
			return false
		}
	}
	return true
}

func (h *ReadinessHealth) requiredNames() map[DependencyName]struct{} {
	result := make(map[DependencyName]struct{})
	if h == nil {
		return result
	}
	if h.requirements.UsersDatabase {
		result[DependencyUsersDatabase] = struct{}{}
	}
	if h.requirements.EventDelivery {
		result[DependencyExternalEventOutbox] = struct{}{}
		if h.registered(DependencyRabbitMQ) {
			result[DependencyRabbitMQ] = struct{}{}
		}
	}
	if h.requirements.MinIO {
		for _, name := range []DependencyName{DependencyLegacyMedia, DependencyMediaAssets, DependencyCampaignMedia} {
			if h.registered(name) {
				result[name] = struct{}{}
			}
		}
	}
	return result
}

func (h *ReadinessHealth) registered(name DependencyName) bool {
	if h == nil || h.dependencies == nil {
		return false
	}
	for _, health := range h.dependencies.Snapshot() {
		if health.Name == name {
			return true
		}
	}
	return false
}
