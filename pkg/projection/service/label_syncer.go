package projection_service

import (
	"context"
	"errors"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/evolution-foundation/evolution-go/pkg/waquery"
	"gorm.io/gorm"
)

type labelSyncState interface {
	Get(instanceID, resource string) (*projection_model.State, error)
	MarkSyncing(instanceID, resource string, schemaVersion int64) error
	MarkStale(instanceID, resource string, schemaVersion int64) error
	MarkFailed(instanceID, resource string, schemaVersion int64) error
}

type LabelSyncer struct {
	guard waquery.Guard
	state labelSyncState
}

func NewLabelSyncer(guard waquery.Guard, state labelSyncState) *LabelSyncer {
	return &LabelSyncer{guard: guard, state: state}
}

func (s *LabelSyncer) Sync(ctx context.Context, instanceID string, fetch func(context.Context) error) error {
	if s == nil || s.guard == nil || s.state == nil || instanceID == "" || fetch == nil {
		return errors.New("label sync dependencies and instance identity are required")
	}
	state, err := s.state.Get(instanceID, labelResource)
	if err == nil && state != nil && state.SyncStatus == projection_model.SyncStatusReady && state.SchemaVersion >= LabelsProjectionSchemaVersion {
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err := s.state.MarkSyncing(instanceID, labelResource, LabelsProjectionSchemaVersion); err != nil {
		return err
	}
	_, err = waquery.Do(ctx, s.guard, instanceID, "labels_full_sync", "appstate:regular", func(queryCtx context.Context) (struct{}, error) {
		return struct{}{}, fetch(queryCtx)
	})
	if err != nil {
		var markErr error
		if state != nil && state.LastReconciledAt != nil {
			markErr = s.state.MarkStale(instanceID, labelResource, LabelsProjectionSchemaVersion)
		} else {
			markErr = s.state.MarkFailed(instanceID, labelResource, LabelsProjectionSchemaVersion)
		}
		if markErr != nil {
			return errors.Join(err, markErr)
		}
		return err
	}
	return nil
}
