package projection_service

import (
	"context"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	waSyncAction "go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type captureLabels struct {
	label              *projection_model.Label
	chatAssociation    *projection_model.LabelChatAssociation
	messageAssociation *projection_model.LabelMessageAssociation
}

type captureLabelReadiness struct {
	unprocessed bool
}

func (c *captureLabelReadiness) HasUnprocessedEvents(context.Context, string, string, []string, string) (bool, error) {
	return c.unprocessed, nil
}

func (c *captureLabels) ApplyLabel(_ context.Context, label *projection_model.Label) (bool, error) {
	c.label = label
	return true, nil
}

func (c *captureLabels) ApplyChatAssociation(_ context.Context, association *projection_model.LabelChatAssociation) (bool, error) {
	c.chatAssociation = association
	return true, nil
}

func (c *captureLabels) ApplyMessageAssociation(_ context.Context, association *projection_model.LabelMessageAssociation) (bool, error) {
	c.messageAssociation = association
	return true, nil
}

func TestLabelProjectorAppliesDefinitionTombstoneAndRecordsState(t *testing.T) {
	name, deleted := "Removed", true
	event, _, err := NormalizeLabelEvent("instance-a", &events.LabelEdit{
		Timestamp: time.Unix(200, 0), LabelID: "label-1",
		Action: &waSyncAction.LabelEditAction{Name: &name, Deleted: &deleted},
	})
	if err != nil {
		t.Fatal(err)
	}
	labels := &captureLabels{}
	state := &captureProjectionState{}
	if err := NewLabelProjector(labels, state, &captureLabelReadiness{}).Handle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if labels.label == nil || labels.label.Name == nil || *labels.label.Name != name || labels.label.TombstonedAt == nil || labels.label.SourceEventKey != event.EventKey {
		t.Fatalf("projected label = %#v", labels.label)
	}
	if state.instanceID != "instance-a" || state.resource != labelResource || state.version != LabelsProjectionSchemaVersion || !state.occurredAt.Equal(event.OccurredAt) {
		t.Fatalf("projection state = %#v", state)
	}
}

func TestLabelProjectorMapsAssociationRemovalToTombstone(t *testing.T) {
	labeled := false
	event, _, err := NormalizeLabelEvent("instance-a", &events.LabelAssociationMessage{
		JID: types.NewJID("chat", types.DefaultUserServer), Timestamp: time.Unix(300, 0),
		LabelID: "label-1", MessageID: "message-1", Action: &waSyncAction.LabelAssociationAction{Labeled: &labeled},
	})
	if err != nil {
		t.Fatal(err)
	}
	labels := &captureLabels{}
	if err := NewLabelProjector(labels, &captureProjectionState{}, &captureLabelReadiness{}).Handle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	association := labels.messageAssociation
	if association == nil || association.ChatID != "chat@s.whatsapp.net" || association.MessageID != "message-1" || association.TombstonedAt == nil || !association.TombstonedAt.Equal(event.OccurredAt) {
		t.Fatalf("projected message association = %#v", association)
	}
}

func TestLabelProjectorWaitsForInboxBarrierBeforeReady(t *testing.T) {
	event, relevant, err := NormalizeLabelEvent("instance-a", &events.AppStateSyncComplete{Name: "regular", Version: 0})
	if err != nil || !relevant {
		t.Fatalf("sync completion normalization = %#v, %v, %v", event, relevant, err)
	}
	state := &captureProjectionState{}
	readiness := &captureLabelReadiness{unprocessed: true}
	projector := NewLabelProjector(&captureLabels{}, state, readiness)
	if err := projector.Handle(context.Background(), event); err == nil {
		t.Fatal("sync completion ignored unprocessed label events")
	}
	readiness.unprocessed = false
	if err := projector.Handle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if state.readyResource != labelResource || state.readyVersion != LabelsProjectionSchemaVersion || state.readyAt.IsZero() {
		t.Fatalf("ready state = %#v", state)
	}
}
