package label_service

import (
	"context"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
)

type labelProjectionReadRepositoryStub struct {
	labels []projection_model.Label
}

func (s *labelProjectionReadRepositoryStub) ListLabels(context.Context, string) ([]projection_model.Label, error) {
	return s.labels, nil
}

func (s *labelProjectionReadRepositoryStub) GetLabel(_ context.Context, _, labelID string) (*projection_model.Label, error) {
	for index := range s.labels {
		if s.labels[index].LabelID == labelID {
			return &s.labels[index], nil
		}
	}
	return nil, nil
}

type labelProjectionReadStateStub struct {
	state *projection_model.State
}

func (s *labelProjectionReadStateStub) Get(string, string) (*projection_model.State, error) {
	return s.state, nil
}

func TestLabelReadsUseProjectionWithoutWhatsAppConnection(t *testing.T) {
	name := "Priority"
	color := int32(4)
	predefinedID := int32(2)
	reconciledAt := time.Unix(500, 0)
	repository := &labelProjectionReadRepositoryStub{labels: []projection_model.Label{{
		InstanceID: "instance-a", LabelID: "label-1", Name: &name, Color: &color, PredefinedID: &predefinedID,
	}}}
	reader := projection_service.NewLabelReader(repository, &labelProjectionReadStateStub{state: &projection_model.State{
		InstanceID: "instance-a", Resource: "labels", SyncStatus: projection_model.SyncStatusReady,
		SchemaVersion: projection_service.LabelsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}})
	service := &labelService{projectionReader: reader}
	instance := &instance_model.Instance{Id: "instance-a"}

	labels, err := service.GetLabels(context.Background(), instance)
	if err != nil || len(labels) != 1 || labels[0].Id != "label-1" || labels[0].InstanceID != "instance-a" ||
		labels[0].LabelName != name || labels[0].LabelColor != "4" || labels[0].PredefinedId != "2" {
		t.Fatalf("GetLabels() = %#v, %v", labels, err)
	}
	label, meta, err := service.GetLabel(context.Background(), instance, "label-1")
	if err != nil || label == nil || label.LabelID != "label-1" || meta == nil || meta.Source != "projection" {
		t.Fatalf("GetLabel() = %#v, %#v, %v", label, meta, err)
	}
}
