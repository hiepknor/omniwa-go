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
func (r *memoryStateRepository) ListAll() ([]projection_model.State, error) {
	result := make([]projection_model.State, 0, len(r.states))
	for _, state := range r.states {
		result = append(result, state)
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
	if len(capabilities) != 2 || capabilities[0] != CapabilityEventsProjection || capabilities[1] != CapabilityRateLimitRetryAfter {
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
	if len(capabilities) != 2 || capabilities[0] != CapabilityEventsProjection || capabilities[1] != CapabilityRateLimitRetryAfter {
		t.Fatalf("capabilities = %v", capabilities)
	}
	if err := service.MarkReady("instance-a", "groups", GroupsProjectionSchemaVersion, time.Unix(500, 0)); err != nil {
		t.Fatal(err)
	}
	capabilities, _ = service.Capabilities("instance-a")
	if len(capabilities) != 3 || capabilities[0] != CapabilityEventsProjection || capabilities[1] != "groups_projection" || capabilities[2] != CapabilityRateLimitRetryAfter {
		t.Fatalf("groups capability = %v", capabilities)
	}
}

func TestAdminCapabilitiesOnlyExposeServerFeatures(t *testing.T) {
	service := NewStateService(newMemoryRepository())
	capabilities, err := service.Capabilities("")
	if err != nil || len(capabilities) != 2 || capabilities[0] != CapabilityEventsProjection || capabilities[1] != CapabilityRateLimitRetryAfter {
		t.Fatalf("Capabilities() = %v, %v", capabilities, err)
	}
}

func TestContactsCapabilityRequiresReadyCurrentSchema(t *testing.T) {
	service := NewStateService(newMemoryRepository())
	if err := service.MarkReady("instance-a", "contacts", ContactsProjectionSchemaVersion-1, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	capabilities, err := service.Capabilities("instance-a")
	if err != nil || containsCapability(capabilities, "contacts_projection") {
		t.Fatalf("premature contacts capability = %v, %v", capabilities, err)
	}
	if err := service.MarkReady("instance-a", "contacts", ContactsProjectionSchemaVersion, time.Unix(200, 0)); err != nil {
		t.Fatal(err)
	}
	capabilities, err = service.Capabilities("instance-a")
	if err != nil || !containsCapability(capabilities, "contacts_projection") {
		t.Fatalf("ready contacts capability = %v, %v", capabilities, err)
	}
}

func TestChatAndMessageCapabilitiesActivateIndependently(t *testing.T) {
	service := NewStateService(newMemoryRepository())
	if err := service.MarkReady("instance-a", "chats", ChatsProjectionSchemaVersion, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	capabilities, err := service.Capabilities("instance-a")
	if err != nil || !containsCapability(capabilities, "chats_projection") || containsCapability(capabilities, "messages_projection") {
		t.Fatalf("chat-only capabilities = %v, %v", capabilities, err)
	}
	if err := service.MarkReady("instance-a", "messages", MessagesProjectionSchemaVersion, time.Unix(200, 0)); err != nil {
		t.Fatal(err)
	}
	capabilities, err = service.Capabilities("instance-a")
	if err != nil || !containsCapability(capabilities, "chats_projection") || !containsCapability(capabilities, "messages_projection") {
		t.Fatalf("chat/message capabilities = %v, %v", capabilities, err)
	}
}

func containsCapability(capabilities []string, expected string) bool {
	for _, capability := range capabilities {
		if capability == expected {
			return true
		}
	}
	return false
}

func TestProjectionHealthMetricsAreScopedAndTimestamped(t *testing.T) {
	repository := newMemoryRepository()
	now := time.Unix(1000, 0).UTC()
	eventAt := time.Unix(900, 0).UTC()
	reconciledAt := time.Unix(800, 0).UTC()
	staleSince := time.Unix(950, 0).UTC()
	repository.states[stateKey("instance-a", "groups")] = projection_model.State{
		InstanceID: "instance-a", Resource: "groups", SyncStatus: projection_model.SyncStatusReady, SchemaVersion: 3,
		LastEventAt: &eventAt, LastReconciledAt: &reconciledAt,
	}
	repository.states[stateKey("instance-b", "contacts")] = projection_model.State{
		InstanceID: "instance-b", Resource: "contacts", SyncStatus: projection_model.SyncStatusStale, SchemaVersion: 1, StaleSince: &staleSince,
	}
	service := &stateService{repository: repository, now: func() time.Time { return now }}

	scoped, err := service.Health("instance-a")
	if err != nil || scoped.Status != "healthy" || scoped.Total != 1 || scoped.ByStatus["ready"] != 1 || len(scoped.Resources) != 1 ||
		scoped.Resources[0].InstanceID != "instance-a" || scoped.Resources[0].EventLagSeconds == nil || *scoped.Resources[0].EventLagSeconds != 100 ||
		scoped.Resources[0].ReconcileAgeSeconds == nil || *scoped.Resources[0].ReconcileAgeSeconds != 200 || !scoped.GeneratedAt.Equal(now) {
		t.Fatalf("scoped Health() = %#v, %v", scoped, err)
	}
	global, err := service.Health("")
	if err != nil || global.Status != "degraded" || global.Total != 2 || global.ByStatus["stale"] != 1 || len(global.Resources) != 2 {
		t.Fatalf("global Health() = %#v, %v", global, err)
	}
}
