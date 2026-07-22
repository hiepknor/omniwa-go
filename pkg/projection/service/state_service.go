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

type StateService interface {
	Get(instanceID, resource string) (*projection_model.State, error)
	Ensure(instanceID, resource string, schemaVersion int64) (*projection_model.State, error)
	RecordEvent(instanceID, resource string, schemaVersion int64, occurredAt time.Time) error
	MarkSyncing(instanceID, resource string, schemaVersion int64) error
	MarkReady(instanceID, resource string, schemaVersion int64, reconciledAt time.Time) error
	MarkStale(instanceID, resource string, schemaVersion int64) error
	MarkFailed(instanceID, resource string, schemaVersion int64) error
	Capabilities(instanceID string) ([]string, error)
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
			if state.Resource == groupResource && state.SchemaVersion < GroupsProjectionSchemaVersion {
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
