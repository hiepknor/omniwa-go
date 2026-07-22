package projection_service

import (
	"errors"
	"sort"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"gorm.io/gorm"
)

const CapabilityRateLimitRetryAfter = "rate_limit_retry_after"

var resourceCapabilities = map[string]string{
	"groups":   "groups_projection",
	"labels":   "labels_projection",
	"contacts": "contacts_projection",
	"chats":    "chats_projection",
	"messages": "messages_projection",
	"events":   "events_projection",
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
	SchemaVersion       int64                       `json:"schemaVersion"`
	LastEventAt         *time.Time                  `json:"lastEventAt,omitempty"`
	LastReconciledAt    *time.Time                  `json:"lastReconciledAt,omitempty"`
	StaleSince          *time.Time                  `json:"staleSince,omitempty"`
	EventLagSeconds     *int64                      `json:"eventLagSeconds,omitempty"`
	ReconcileAgeSeconds *int64                      `json:"reconcileAgeSeconds,omitempty"`
}

type stateService struct {
	repository projection_repository.StateRepository
	now        func() time.Time
}

func NewStateService(repository projection_repository.StateRepository) StateService {
	return &stateService{repository: repository, now: time.Now}
}

func (s *stateService) Get(instanceID, resource string) (*projection_model.State, error) {
	return s.repository.Get(instanceID, resource)
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
	capabilities := []string{CapabilityRateLimitRetryAfter}
	if instanceID == "" {
		return capabilities, nil
	}
	states, err := s.repository.ListByInstance(instanceID)
	if err != nil {
		return nil, err
	}
	for _, state := range states {
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
	now := s.now().UTC()
	health := &ProjectionHealth{
		Status:      "healthy",
		GeneratedAt: now,
		Total:       len(states),
		ByStatus:    map[string]int{},
		Resources:   make([]ProjectionResourceHealth, len(states)),
	}
	for index := range states {
		state := &states[index]
		health.ByStatus[string(state.SyncStatus)]++
		resource := ProjectionResourceHealth{
			InstanceID:       state.InstanceID,
			Resource:         state.Resource,
			SyncStatus:       state.SyncStatus,
			SchemaVersion:    state.SchemaVersion,
			LastEventAt:      utcTimePointer(state.LastEventAt),
			LastReconciledAt: utcTimePointer(state.LastReconciledAt),
			StaleSince:       utcTimePointer(state.StaleSince),
		}
		resource.EventLagSeconds = ageSeconds(now, state.LastEventAt)
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
