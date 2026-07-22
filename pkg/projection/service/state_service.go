package projection_service

import (
	"errors"
	"sort"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"gorm.io/gorm"
)

const (
	CapabilityRateLimitRetryAfter   = "rate_limit_retry_after"
	CapabilityEventsProjection      = "events_projection"
	CapabilityOutboundRateLimit     = "outbound_rate_limit"
	CapabilityCampaignOrchestration = "campaign_orchestration"
	CapabilityFailureOperations     = "projection_failure_operations"
)

var resourceCapabilities = map[string]string{
	"groups":   "groups_projection",
	"labels":   "labels_projection",
	"contacts": "contacts_projection",
	"chats":    "chats_projection",
	"messages": "messages_projection",
}

var resourceSchemaVersions = map[string]int64{
	"groups":   GroupsProjectionSchemaVersion,
	"labels":   LabelsProjectionSchemaVersion,
	"contacts": ContactsProjectionSchemaVersion,
	"chats":    ChatsProjectionSchemaVersion,
	"messages": MessagesProjectionSchemaVersion,
}

type StateService interface {
	Get(instanceID, resource string) (*projection_model.State, error)
	GetServingState(instanceID, resource string) (*projection_model.State, error)
	Ensure(instanceID, resource string, schemaVersion int64) (*projection_model.State, error)
	RecordEvent(instanceID, resource string, schemaVersion int64, occurredAt time.Time) error
	MarkSyncing(instanceID, resource string, schemaVersion int64) error
	MarkReady(instanceID, resource string, schemaVersion int64, reconciledAt time.Time) error
	MarkStale(instanceID, resource string, schemaVersion int64) error
	MarkFailed(instanceID, resource string, schemaVersion int64) error
	Capabilities(instanceID string) ([]string, error)
	Health(instanceID string) (*ProjectionHealth, error)
}

type ProjectionHealth struct {
	Status      string                     `json:"status"`
	GeneratedAt time.Time                  `json:"generatedAt"`
	Total       int                        `json:"total"`
	ByStatus    map[string]int             `json:"byStatus"`
	Resources   []ProjectionResourceHealth `json:"resources"`
}

type ProjectionResourceHealth struct {
	InstanceID          string                      `json:"instanceId"`
	Resource            string                      `json:"resource"`
	SyncStatus          projection_model.SyncStatus `json:"syncStatus"`
	StoredSyncStatus    projection_model.SyncStatus `json:"storedSyncStatus,omitempty"`
	SchemaVersion       int64                       `json:"schemaVersion"`
	LastEventAt         *time.Time                  `json:"lastEventAt,omitempty"`
	LastReconciledAt    *time.Time                  `json:"lastReconciledAt,omitempty"`
	StaleSince          *time.Time                  `json:"staleSince,omitempty"`
	EventLagSeconds     *int64                      `json:"eventLagSeconds,omitempty"`
	WorkLagSeconds      *int64                      `json:"workLagSeconds,omitempty"`
	ReconcileAgeSeconds *int64                      `json:"reconcileAgeSeconds,omitempty"`
	OldestUnprocessedAt *time.Time                  `json:"oldestUnprocessedAt,omitempty"`
	PendingEvents       int64                       `json:"pendingEvents"`
	ProcessingEvents    int64                       `json:"processingEvents"`
	FailedEvents        int64                       `json:"failedEvents"`
	DeadLetterEvents    int64                       `json:"deadLetterEvents"`
	DegradedReasons     []string                    `json:"degradedReasons,omitempty"`
}

type stateService struct {
	repository projection_repository.StateRepository
	work       projection_repository.WorkHealthRepository
	policy     ProjectionHealthPolicy
	now        func() time.Time
}

type ProjectionHealthPolicy struct {
	WorkLagThreshold time.Duration
	MaxReconcileAge  map[string]time.Duration
}

const defaultProjectionWorkLagThreshold = 2 * time.Minute

func NewStateService(repository projection_repository.StateRepository) StateService {
	return &stateService{repository: repository, policy: ProjectionHealthPolicy{WorkLagThreshold: defaultProjectionWorkLagThreshold}, now: time.Now}
}

func NewStateServiceWithHealth(repository projection_repository.StateRepository, work projection_repository.WorkHealthRepository, policy ProjectionHealthPolicy) StateService {
	if policy.WorkLagThreshold <= 0 {
		policy.WorkLagThreshold = defaultProjectionWorkLagThreshold
	}
	policy.MaxReconcileAge = copyDurationMap(policy.MaxReconcileAge)
	return &stateService{repository: repository, work: work, policy: policy, now: time.Now}
}

func (s *stateService) Get(instanceID, resource string) (*projection_model.State, error) {
	return s.repository.Get(instanceID, resource)
}

func (s *stateService) GetServingState(instanceID, resource string) (*projection_model.State, error) {
	state, err := s.Get(instanceID, resource)
	if err != nil || s.work == nil {
		return state, err
	}
	work, err := s.work.Get(instanceID, resource)
	if err != nil {
		return nil, err
	}
	effective, _ := s.effectiveState(state, work, s.now().UTC())
	return effective, nil
}

func (s *stateService) Ensure(instanceID, resource string, schemaVersion int64) (*projection_model.State, error) {
	state, err := s.repository.Get(instanceID, resource)
	if err == nil {
		if schemaVersion > state.SchemaVersion {
			state.SchemaVersion = schemaVersion
			if err := s.repository.Upsert(state); err != nil {
				return nil, err
			}
		}
		return state, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	state = &projection_model.State{InstanceID: instanceID, Resource: resource, SyncStatus: projection_model.SyncStatusNotStarted, SchemaVersion: schemaVersion}
	if err := s.repository.Upsert(state); err != nil {
		return nil, err
	}
	return state, nil
}

func (s *stateService) RecordEvent(instanceID, resource string, schemaVersion int64, occurredAt time.Time) error {
	return s.repository.RecordEvent(instanceID, resource, schemaVersion, occurredAt)
}

func (s *stateService) MarkSyncing(instanceID, resource string, schemaVersion int64) error {
	return s.setStatus(instanceID, resource, schemaVersion, projection_model.SyncStatusSyncing, time.Time{})
}

func (s *stateService) MarkReady(instanceID, resource string, schemaVersion int64, reconciledAt time.Time) error {
	return s.setStatus(instanceID, resource, schemaVersion, projection_model.SyncStatusReady, reconciledAt)
}

func (s *stateService) MarkStale(instanceID, resource string, schemaVersion int64) error {
	return s.setStatus(instanceID, resource, schemaVersion, projection_model.SyncStatusStale, time.Time{})
}

func (s *stateService) MarkFailed(instanceID, resource string, schemaVersion int64) error {
	return s.setStatus(instanceID, resource, schemaVersion, projection_model.SyncStatusFailed, time.Time{})
}

func (s *stateService) setStatus(instanceID, resource string, schemaVersion int64, status projection_model.SyncStatus, reconciledAt time.Time) error {
	state, err := s.Ensure(instanceID, resource, schemaVersion)
	if err != nil {
		return err
	}
	state.SyncStatus = status
	if status == projection_model.SyncStatusReady {
		state.StaleSince = nil
		if !reconciledAt.IsZero() && (state.LastReconciledAt == nil || reconciledAt.After(*state.LastReconciledAt)) {
			reconciledAt = reconciledAt.UTC()
			state.LastReconciledAt = &reconciledAt
		}
	} else if (status == projection_model.SyncStatusStale || status == projection_model.SyncStatusFailed) && state.StaleSince == nil {
		now := s.now().UTC()
		state.StaleSince = &now
	}
	return s.repository.Upsert(state)
}

func (s *stateService) Capabilities(instanceID string) ([]string, error) {
	// Durable history has no initial-sync barrier: it is validly empty for a new
	// instance and explicitly does not claim pre-deployment backfill.
	capabilities := []string{CapabilityCampaignOrchestration, CapabilityEventsProjection, CapabilityOutboundRateLimit, CapabilityRateLimitRetryAfter}
	if instanceID == "" {
		capabilities = append(capabilities, CapabilityFailureOperations)
		sort.Strings(capabilities)
		return capabilities, nil
	}
	states, err := s.repository.ListByInstance(instanceID)
	if err != nil {
		return nil, err
	}
	workByResource, err := s.workByResource(instanceID)
	if err != nil {
		return nil, err
	}
	states = appendMissingWorkStates(states, workByResource)
	now := s.now().UTC()
	for index := range states {
		state, _ := s.effectiveState(&states[index], workByResource[projectionWorkKey(states[index].InstanceID, states[index].Resource)], now)
		if state.SyncStatus == projection_model.SyncStatusReady {
			if requiredVersion := resourceSchemaVersions[state.Resource]; requiredVersion > 0 && state.SchemaVersion < requiredVersion {
				continue
			}
			if capability := resourceCapabilities[state.Resource]; capability != "" {
				capabilities = append(capabilities, capability)
			}
		}
	}
	sort.Strings(capabilities)
	return capabilities, nil
}

func (s *stateService) Health(instanceID string) (*ProjectionHealth, error) {
	var states []projection_model.State
	var err error
	if instanceID == "" {
		states, err = s.repository.ListAll()
	} else {
		states, err = s.repository.ListByInstance(instanceID)
	}
	if err != nil {
		return nil, err
	}
	workByResource, err := s.workByResource(instanceID)
	if err != nil {
		return nil, err
	}
	states = appendMissingWorkStates(states, workByResource)
	now := s.now().UTC()
	status := "healthy"
	if len(states) == 0 {
		status = "not_started"
	}
	health := &ProjectionHealth{
		Status:      status,
		GeneratedAt: now,
		Total:       len(states),
		ByStatus:    map[string]int{},
		Resources:   make([]ProjectionResourceHealth, len(states)),
	}
	for index := range states {
		storedState := &states[index]
		work := workByResource[projectionWorkKey(storedState.InstanceID, storedState.Resource)]
		state, reasons := s.effectiveState(storedState, work, now)
		health.ByStatus[string(state.SyncStatus)]++
		resource := ProjectionResourceHealth{
			InstanceID:       state.InstanceID,
			Resource:         state.Resource,
			SyncStatus:       state.SyncStatus,
			SchemaVersion:    state.SchemaVersion,
			LastEventAt:      utcTimePointer(state.LastEventAt),
			LastReconciledAt: utcTimePointer(state.LastReconciledAt),
			StaleSince:       utcTimePointer(state.StaleSince),
			DegradedReasons:  reasons,
		}
		if state.SyncStatus != storedState.SyncStatus {
			resource.StoredSyncStatus = storedState.SyncStatus
		}
		if work != nil {
			resource.OldestUnprocessedAt = utcTimePointer(work.OldestUnprocessedAt)
			resource.PendingEvents = work.PendingEvents
			resource.ProcessingEvents = work.ProcessingEvents
			resource.FailedEvents = work.FailedEvents
			resource.DeadLetterEvents = work.DeadLetterEvents
		}
		resource.EventLagSeconds = ageSeconds(now, state.LastEventAt)
		resource.WorkLagSeconds = ageSeconds(now, resource.OldestUnprocessedAt)
		resource.ReconcileAgeSeconds = ageSeconds(now, state.LastReconciledAt)
		health.Resources[index] = resource
		switch state.SyncStatus {
		case projection_model.SyncStatusStale, projection_model.SyncStatusFailed:
			health.Status = "degraded"
		case projection_model.SyncStatusSyncing, projection_model.SyncStatusNotStarted:
			if health.Status == "healthy" {
				health.Status = "syncing"
			}
		}
	}
	return health, nil
}

func (s *stateService) workByResource(instanceID string) (map[string]*projection_repository.ProjectionWorkHealth, error) {
	result := map[string]*projection_repository.ProjectionWorkHealth{}
	if s.work == nil {
		return result, nil
	}
	records, err := s.work.List(instanceID)
	if err != nil {
		return nil, err
	}
	for index := range records {
		record := records[index]
		result[projectionWorkKey(record.InstanceID, record.Resource)] = &record
	}
	return result, nil
}

func appendMissingWorkStates(states []projection_model.State, workByResource map[string]*projection_repository.ProjectionWorkHealth) []projection_model.State {
	seen := make(map[string]struct{}, len(states))
	for index := range states {
		seen[projectionWorkKey(states[index].InstanceID, states[index].Resource)] = struct{}{}
	}
	for key, work := range workByResource {
		if work == nil {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		states = append(states, projection_model.State{
			InstanceID: work.InstanceID, Resource: work.Resource, SyncStatus: projection_model.SyncStatusNotStarted,
			SchemaVersion: resourceSchemaVersions[work.Resource],
		})
	}
	sort.Slice(states, func(left, right int) bool {
		if states[left].InstanceID == states[right].InstanceID {
			return states[left].Resource < states[right].Resource
		}
		return states[left].InstanceID < states[right].InstanceID
	})
	return states
}

func (s *stateService) effectiveState(stored *projection_model.State, work *projection_repository.ProjectionWorkHealth, now time.Time) (*projection_model.State, []string) {
	if stored == nil {
		return nil, nil
	}
	copy := *stored
	reasons := []string{}
	if work != nil {
		if work.DeadLetterEvents > 0 {
			reasons = append(reasons, "dead_letters")
		}
		if work.OldestUnprocessedAt != nil && s.policy.WorkLagThreshold > 0 && now.Sub(work.OldestUnprocessedAt.UTC()) > s.policy.WorkLagThreshold {
			reasons = append(reasons, "work_lag")
		}
	}
	if maxAge := s.policy.MaxReconcileAge[stored.Resource]; maxAge > 0 && (stored.LastReconciledAt == nil || now.Sub(stored.LastReconciledAt.UTC()) > maxAge) {
		reasons = append(reasons, "reconciliation_overdue")
	}
	if len(reasons) > 0 {
		switch stored.SyncStatus {
		case projection_model.SyncStatusReady:
			copy.SyncStatus = projection_model.SyncStatusStale
		case projection_model.SyncStatusNotStarted, projection_model.SyncStatusSyncing:
			if stored.LastReconciledAt != nil {
				copy.SyncStatus = projection_model.SyncStatusStale
			} else {
				copy.SyncStatus = projection_model.SyncStatusFailed
			}
		}
		if copy.StaleSince == nil {
			staleSince := now
			copy.StaleSince = &staleSince
		}
	}
	return &copy, reasons
}

func projectionWorkKey(instanceID, resource string) string {
	return instanceID + "\x00" + resource
}

func copyDurationMap(source map[string]time.Duration) map[string]time.Duration {
	copy := make(map[string]time.Duration, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}

func utcTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}

func ageSeconds(now time.Time, value *time.Time) *int64 {
	if value == nil {
		return nil
	}
	age := now.Sub(value.UTC())
	if age < 0 {
		age = 0
	}
	seconds := int64(age / time.Second)
	return &seconds
}
