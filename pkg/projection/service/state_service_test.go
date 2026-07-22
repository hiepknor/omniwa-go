package projection_service

import (
	"errors"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
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

type memoryWorkHealthRepository struct {
	records map[string]projection_repository.ProjectionWorkHealth
	err     error
}

func (r *memoryWorkHealthRepository) Get(instanceID, resource string) (*projection_repository.ProjectionWorkHealth, error) {
	if r.err != nil {
		return nil, r.err
	}
	record, exists := r.records[stateKey(instanceID, resource)]
	if !exists {
		return &projection_repository.ProjectionWorkHealth{}, nil
	}
	return &record, nil
}

func (r *memoryWorkHealthRepository) List(instanceID string) ([]projection_repository.ProjectionWorkHealth, error) {
	if r.err != nil {
		return nil, r.err
	}
	result := []projection_repository.ProjectionWorkHealth{}
	for _, record := range r.records {
		if instanceID == "" || record.InstanceID == instanceID {
			result = append(result, record)
		}
	}
	return result, nil
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
	if len(capabilities) != 4 || capabilities[0] != CapabilityCampaignOrchestration || capabilities[1] != CapabilityEventsProjection || capabilities[2] != CapabilityOutboundRateLimit || capabilities[3] != CapabilityRateLimitRetryAfter {
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
	if len(capabilities) != 4 || capabilities[0] != CapabilityCampaignOrchestration || capabilities[1] != CapabilityEventsProjection || capabilities[2] != CapabilityOutboundRateLimit || capabilities[3] != CapabilityRateLimitRetryAfter {
		t.Fatalf("capabilities = %v", capabilities)
	}
	if err := service.MarkReady("instance-a", "groups", GroupsProjectionSchemaVersion, time.Unix(500, 0)); err != nil {
		t.Fatal(err)
	}
	capabilities, _ = service.Capabilities("instance-a")
	if len(capabilities) != 5 || capabilities[0] != CapabilityCampaignOrchestration || capabilities[1] != CapabilityEventsProjection || capabilities[2] != "groups_projection" || capabilities[3] != CapabilityOutboundRateLimit || capabilities[4] != CapabilityRateLimitRetryAfter {
		t.Fatalf("groups capability = %v", capabilities)
	}
}

func TestAdminCapabilitiesOnlyExposeServerFeatures(t *testing.T) {
	service := NewStateService(newMemoryRepository())
	capabilities, err := service.Capabilities("")
	if err != nil || len(capabilities) != 5 || capabilities[0] != CapabilityCampaignOrchestration || capabilities[1] != CapabilityEventsProjection || capabilities[2] != CapabilityOutboundRateLimit || capabilities[3] != CapabilityFailureOperations || capabilities[4] != CapabilityRateLimitRetryAfter {
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

func TestProjectionHealthDistinguishesNoStateFromReadyEmptyProjection(t *testing.T) {
	service := NewStateServiceWithHealth(newMemoryRepository(), &memoryWorkHealthRepository{records: map[string]projection_repository.ProjectionWorkHealth{}}, ProjectionHealthPolicy{})
	health, err := service.Health("instance-a")
	if err != nil || health.Status != "not_started" || health.Total != 0 || len(health.Resources) != 0 {
		t.Fatalf("empty projection health = %#v, %v", health, err)
	}
}

func TestProjectionHealthDerivesReadinessFromWorkBacklogAndDeadLetters(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	reconciledAt := now.Add(-time.Minute)
	repository := newMemoryRepository()
	repository.states[stateKey("instance-a", "groups")] = projection_model.State{
		InstanceID: "instance-a", Resource: "groups", SyncStatus: projection_model.SyncStatusReady,
		SchemaVersion: GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}
	recent := now.Add(-30 * time.Second)
	work := &memoryWorkHealthRepository{records: map[string]projection_repository.ProjectionWorkHealth{
		stateKey("instance-a", "groups"): {
			InstanceID: "instance-a", Resource: "groups", PendingEvents: 1, OldestUnprocessedAt: &recent,
		},
	}}
	service := &stateService{
		repository: repository, work: work,
		policy: ProjectionHealthPolicy{WorkLagThreshold: 2 * time.Minute, MaxReconcileAge: map[string]time.Duration{"groups": 10 * time.Minute}},
		now:    func() time.Time { return now },
	}

	health, err := service.Health("instance-a")
	if err != nil || health.Status != "healthy" || health.Resources[0].SyncStatus != projection_model.SyncStatusReady || health.Resources[0].PendingEvents != 1 {
		t.Fatalf("recent backlog health = %#v, %v", health, err)
	}
	capabilities, err := service.Capabilities("instance-a")
	if err != nil || !containsCapability(capabilities, "groups_projection") {
		t.Fatalf("recent backlog capabilities = %v, %v", capabilities, err)
	}

	oldest := now.Add(-3 * time.Minute)
	record := work.records[stateKey("instance-a", "groups")]
	record.OldestUnprocessedAt = &oldest
	work.records[stateKey("instance-a", "groups")] = record
	health, err = service.Health("instance-a")
	if err != nil || health.Status != "degraded" || health.Resources[0].SyncStatus != projection_model.SyncStatusStale ||
		health.Resources[0].StoredSyncStatus != projection_model.SyncStatusReady || len(health.Resources[0].DegradedReasons) != 1 ||
		health.Resources[0].DegradedReasons[0] != "work_lag" || health.Resources[0].StaleSince == nil ||
		health.Resources[0].WorkLagSeconds == nil || *health.Resources[0].WorkLagSeconds != 180 {
		t.Fatalf("lagged backlog health = %#v, %v", health, err)
	}
	capabilities, err = service.Capabilities("instance-a")
	if err != nil || containsCapability(capabilities, "groups_projection") {
		t.Fatalf("lagged backlog capabilities = %v, %v", capabilities, err)
	}

	record.DeadLetterEvents = 2
	record.OldestUnprocessedAt = &recent
	work.records[stateKey("instance-a", "groups")] = record
	health, err = service.Health("instance-a")
	if err != nil || health.Resources[0].DeadLetterEvents != 2 || len(health.Resources[0].DegradedReasons) != 1 || health.Resources[0].DegradedReasons[0] != "dead_letters" {
		t.Fatalf("dead-letter health = %#v, %v", health, err)
	}
}

func TestProjectionHealthPreservesUsableSnapshotAndFailsUnreadyResource(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	oldReconciliation := now.Add(-11 * time.Minute)
	repository := newMemoryRepository()
	repository.states[stateKey("instance-a", "groups")] = projection_model.State{
		InstanceID: "instance-a", Resource: "groups", SyncStatus: projection_model.SyncStatusSyncing,
		SchemaVersion: GroupsProjectionSchemaVersion, LastReconciledAt: &oldReconciliation,
	}
	repository.states[stateKey("instance-a", "labels")] = projection_model.State{
		InstanceID: "instance-a", Resource: "labels", SyncStatus: projection_model.SyncStatusReady,
		SchemaVersion: LabelsProjectionSchemaVersion, LastReconciledAt: &oldReconciliation,
	}
	work := &memoryWorkHealthRepository{records: map[string]projection_repository.ProjectionWorkHealth{
		stateKey("instance-a", "groups"):   {InstanceID: "instance-a", Resource: "groups", DeadLetterEvents: 1},
		stateKey("instance-a", "contacts"): {InstanceID: "instance-a", Resource: "contacts", DeadLetterEvents: 1},
	}}
	service := &stateService{
		repository: repository, work: work,
		policy: ProjectionHealthPolicy{WorkLagThreshold: 2 * time.Minute, MaxReconcileAge: map[string]time.Duration{"groups": 10 * time.Minute}},
		now:    func() time.Time { return now },
	}

	storedGroup, err := service.Get("instance-a", "groups")
	if err != nil || storedGroup.SyncStatus != projection_model.SyncStatusSyncing || storedGroup.StaleSince != nil {
		t.Fatalf("stored group state was mutated by health = %#v, %v", storedGroup, err)
	}
	group, err := service.GetServingState("instance-a", "groups")
	if err != nil || group.SyncStatus != projection_model.SyncStatusStale || group.StaleSince == nil {
		t.Fatalf("usable group snapshot = %#v, %v", group, err)
	}
	label, err := service.GetServingState("instance-a", "labels")
	if err != nil || label.SyncStatus != projection_model.SyncStatusReady {
		t.Fatalf("non-periodic label state = %#v, %v", label, err)
	}
	health, err := service.Health("instance-a")
	if err != nil || health.ByStatus["stale"] != 1 || health.ByStatus["failed"] != 1 || health.ByStatus["ready"] != 1 || len(health.Resources) != 3 {
		t.Fatalf("derived health = %#v, %v", health, err)
	}
	if health.Resources[0].Resource != "contacts" || health.Resources[0].SyncStatus != projection_model.SyncStatusFailed ||
		health.Resources[0].StoredSyncStatus != projection_model.SyncStatusNotStarted || health.Resources[0].DeadLetterEvents != 1 {
		t.Fatalf("work-only projection health = %#v", health.Resources[0])
	}
}

func TestProjectionHealthFailsClosedWhenWorkHealthCannotBeRead(t *testing.T) {
	want := errors.New("work health unavailable")
	repository := newMemoryRepository()
	repository.states[stateKey("instance-a", "groups")] = projection_model.State{InstanceID: "instance-a", Resource: "groups", SyncStatus: projection_model.SyncStatusReady}
	service := NewStateServiceWithHealth(repository, &memoryWorkHealthRepository{err: want}, ProjectionHealthPolicy{})
	if state, err := service.Get("instance-a", "groups"); err != nil || state.SyncStatus != projection_model.SyncStatusReady {
		t.Fatalf("Get() = %#v, %v", state, err)
	}
	if _, err := service.GetServingState("instance-a", "groups"); !errors.Is(err, want) {
		t.Fatalf("GetServingState() error = %v, want %v", err, want)
	}
	if _, err := service.Capabilities("instance-a"); !errors.Is(err, want) {
		t.Fatalf("Capabilities() error = %v, want %v", err, want)
	}
	if _, err := service.Health("instance-a"); !errors.Is(err, want) {
		t.Fatalf("Health() error = %v, want %v", err, want)
	}
}
