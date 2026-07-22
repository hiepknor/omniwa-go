package projection_repository

import (
	"errors"
	"fmt"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type StateRepository interface {
	Get(instanceID, resource string) (*projection_model.State, error)
	ListByInstance(instanceID string) ([]projection_model.State, error)
	Upsert(state *projection_model.State) error
}

type stateRepository struct{ db *gorm.DB }

func NewStateRepository(db *gorm.DB) StateRepository { return &stateRepository{db: db} }

func (r *stateRepository) Get(instanceID, resource string) (*projection_model.State, error) {
	var state projection_model.State
	if err := r.db.Where("instance_id = ? AND resource = ?", instanceID, resource).First(&state).Error; err != nil {
		return nil, err
	}
	return &state, nil
}

func (r *stateRepository) ListByInstance(instanceID string) ([]projection_model.State, error) {
	var states []projection_model.State
	if err := r.db.Where("instance_id = ?", instanceID).Order("resource ASC").Find(&states).Error; err != nil {
		return nil, err
	}
	return states, nil
}

func (r *stateRepository) Upsert(state *projection_model.State) error {
	if state == nil {
		return errors.New("projection state is required")
	}
	if state.InstanceID == "" || state.Resource == "" || !validSyncStatus(state.SyncStatus) || state.SchemaVersion <= 0 {
		return fmt.Errorf("invalid projection state")
	}
	return r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "instance_id"}, {Name: "resource"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"sync_status", "last_event_at", "last_reconciled_at", "stale_since", "schema_version", "updated_at",
		}),
	}).Create(state).Error
}

func validSyncStatus(status projection_model.SyncStatus) bool {
	switch status {
	case projection_model.SyncStatusNotStarted, projection_model.SyncStatusSyncing, projection_model.SyncStatusReady, projection_model.SyncStatusStale, projection_model.SyncStatusFailed:
		return true
	default:
		return false
	}
}
