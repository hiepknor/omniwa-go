package projection_service

import (
	"context"
	"errors"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"gorm.io/gorm"
)

type labelReadRepositoryStub struct {
	labels []projection_model.Label
	label  *projection_model.Label
}

type labelReaderStateStub struct {
	state *projection_model.State
	err   error
}

func (s *labelReaderStateStub) GetServingState(string, string) (*projection_model.State, error) {
	return s.state, s.err
}

func (s *labelReadRepositoryStub) ListLabels(context.Context, string) ([]projection_model.Label, error) {
	return s.labels, nil
}

func (s *labelReadRepositoryStub) GetLabel(context.Context, string, string) (*projection_model.Label, error) {
	if s.label == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return s.label, nil
}

func TestLabelReaderDistinguishesReadyEmptyFromNotReady(t *testing.T) {
	state := &labelReaderStateStub{err: gorm.ErrRecordNotFound}
	reader := NewLabelReader(&labelReadRepositoryStub{}, state)
	if _, _, err := reader.List(context.Background(), "instance-a"); !errors.Is(err, ErrLabelsProjectionNotReady) {
		t.Fatalf("not-ready List() error = %v", err)
	}

	reconciledAt := time.Unix(500, 0)
	state.err = nil
	state.state = &projection_model.State{
		InstanceID: "instance-a", Resource: labelResource, SyncStatus: projection_model.SyncStatusReady,
		SchemaVersion: LabelsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}
	labels, meta, err := reader.List(context.Background(), "instance-a")
	if err != nil || len(labels) != 0 || meta == nil || meta.Source != "projection" || meta.LastSyncedAt == nil || !meta.LastSyncedAt.Equal(reconciledAt) {
		t.Fatalf("ready empty List() = %#v, %#v, %v", labels, meta, err)
	}
}

func TestLabelReaderGetsInstanceScopedLabel(t *testing.T) {
	reconciledAt := time.Unix(500, 0)
	projected := &projection_model.Label{InstanceID: "instance-a", LabelID: "label-1"}
	reader := NewLabelReader(&labelReadRepositoryStub{label: projected}, &labelReaderStateStub{state: &projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: LabelsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}})
	label, meta, err := reader.Get(context.Background(), "instance-a", "label-1")
	if err != nil || label != projected || meta == nil {
		t.Fatalf("Get() = %#v, %#v, %v", label, meta, err)
	}
}
