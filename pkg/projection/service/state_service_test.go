package projection_service

import (
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"gorm.io/gorm"
)

type memoryStateRepository struct {
	states map[string]projection_model.State
}

func newMemoryRepository() *memoryStateRepository {
	return &memoryStateRepository{states: make(map[string]projection_model.State)}
}
func stateKey(instanceID, resource string) string { return instanceID + "\x00" + resource }
func (r *memoryStateRepository) Get(instanceID, resource string) (*projection_model.State, error) {
	state, ok := r.states[stateKey(instanceID, resource)]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	copy := state
	return &copy, nil
}
func (r *memoryStateRepository) ListByInstance(instanceID string) ([]projection_model.State, error) {
	var result []projection_model.State
	for _, state := range r.states {
		if state.InstanceID == instanceID {
			result = append(result, state)
		}
	}
	return result, nil
}
func (r *memoryStateRepository) Upsert(state *projection_model.State) error {
	r.states[stateKey(state.InstanceID, state.Resource)] = *state
	return nil
}
func (r *memoryStateRepository) RecordEvent(instanceID, resource string, schemaVersion int64, occurredAt time.Time) error {
	key := stateKey(instanceID, resource)
	state, exists := r.states[key]
	if !exists {
		state = projection_model.State{InstanceID: instanceID, Resource: resource, SyncStatus: projection_model.SyncStatusNotStarted}
	}
	if state.LastEventAt == nil || occurredAt.After(*state.LastEventAt) {
		occurredAt = occurredAt.UTC()
		state.LastEventAt = &occurredAt
	}
	if schemaVersion > state.SchemaVersion {
		state.SchemaVersion = schemaVersion
	}
	r.states[key] = state
	return nil
}

func TestStateLifecyclePreservesNewestEventAndControlsCapabilities(t *testing.T) {
	repository := newMemoryRepository()
	service := &stateService{repository: repository, now: func() time.Time { return time.Unix(300, 0) }}
	newer := time.Unix(200, 0)
	older := time.Unix(100, 0)
	if err := service.RecordEvent("instance-a", "groups", 1, newer); err != nil {
		t.Fatal(err)
	}
	if err := service.RecordEvent("instance-a", "groups", 1, older); err != nil {
		t.Fatal(err)
	}
	if err := service.MarkStale("instance-a", "groups", 1); err != nil {
		t.Fatal(err)
	}
	state, _ := service.Get("instance-a", "groups")
	if !state.LastEventAt.Equal(newer) || state.StaleSince == nil {
		t.Fatalf("unexpected stale state: %#v", state)
	}

	capabilities, _ := service.Capabilities("instance-a")
	if len(capabilities) != 1 || capabilities[0] != CapabilityRateLimitRetryAfter {
		t.Fatalf("premature capabilities: %v", capabilities)
	}
	if err := service.MarkReady("instance-a", "groups", 2, time.Unix(400, 0)); err != nil {
		t.Fatal(err)
	}
	state, _ = service.Get("instance-a", "groups")
	if state.StaleSince != nil || state.SchemaVersion != 2 {
		t.Fatalf("unexpected ready state: %#v", state)
	}
	capabilities, _ = service.Capabilities("instance-a")
	if len(capabilities) != 1 || capabilities[0] != CapabilityRateLimitRetryAfter {
		t.Fatalf("capabilities = %v", capabilities)
	}
	if err := service.MarkReady("instance-a", "groups", GroupsProjectionSchemaVersion, time.Unix(500, 0)); err != nil {
		t.Fatal(err)
	}
	capabilities, _ = service.Capabilities("instance-a")
	if len(capabilities) != 2 || capabilities[0] != "groups_projection" || capabilities[1] != CapabilityRateLimitRetryAfter {
		t.Fatalf("groups capability = %v", capabilities)
	}
}

func TestAdminCapabilitiesOnlyExposeServerFeatures(t *testing.T) {
	service := NewStateService(newMemoryRepository())
	capabilities, err := service.Capabilities("")
	if err != nil || len(capabilities) != 1 || capabilities[0] != CapabilityRateLimitRetryAfter {
		t.Fatalf("Capabilities() = %v, %v", capabilities, err)
	}
}
