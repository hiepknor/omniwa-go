package projection_service

import (
	"context"
	"errors"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"gorm.io/gorm"
)

var ErrLabelsProjectionNotReady = errors.New("labels projection is not ready")

type labelReadRepository interface {
	GetLabel(context.Context, string, string) (*projection_model.Label, error)
	ListLabels(context.Context, string) ([]projection_model.Label, error)
}

type LabelReader struct {
	labels labelReadRepository
	state  groupReadState
}

func NewLabelReader(labels labelReadRepository, state groupReadState) *LabelReader {
	return &LabelReader{labels: labels, state: state}
}

func (r *LabelReader) List(ctx context.Context, instanceID string) ([]projection_model.Label, *ProjectionReadMeta, error) {
	meta, err := r.readMeta(instanceID)
	if err != nil {
		return nil, nil, err
	}
	labels, err := r.labels.ListLabels(ctx, instanceID)
	return labels, meta, err
}

func (r *LabelReader) Get(ctx context.Context, instanceID, labelID string) (*projection_model.Label, *ProjectionReadMeta, error) {
	if labelID == "" {
		return nil, nil, errors.New("label identity is required")
	}
	meta, err := r.readMeta(instanceID)
	if err != nil {
		return nil, nil, err
	}
	label, err := r.labels.GetLabel(ctx, instanceID, labelID)
	return label, meta, err
}

func (r *LabelReader) readMeta(instanceID string) (*ProjectionReadMeta, error) {
	if r == nil || r.labels == nil || r.state == nil || instanceID == "" {
		return nil, errors.New("label projection reader dependencies and instance identity are required")
	}
	state, err := r.state.GetServingState(instanceID, labelResource)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrLabelsProjectionNotReady
	}
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, ErrLabelsProjectionNotReady
	}
	usableStatus := state.SyncStatus == projection_model.SyncStatusReady || state.SyncStatus == projection_model.SyncStatusStale || state.SyncStatus == projection_model.SyncStatusSyncing
	if !usableStatus || state.LastReconciledAt == nil || state.SchemaVersion < LabelsProjectionSchemaVersion {
		return nil, ErrLabelsProjectionNotReady
	}
	lastSyncedAt := state.LastReconciledAt.UTC()
	return &ProjectionReadMeta{Source: "projection", SyncStatus: state.SyncStatus, LastSyncedAt: &lastSyncedAt}, nil
}
