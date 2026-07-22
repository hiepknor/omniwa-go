package projection_service

import (
	"context"
	"errors"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type captureContactProjection struct {
	patch projection_repository.ContactPatch
	err   error
}

func (c *captureContactProjection) Apply(_ context.Context, patch projection_repository.ContactPatch) (*projection_model.Contact, bool, error) {
	c.patch = patch
	return &projection_model.Contact{}, true, c.err
}

func TestContactProjectorMapsNormalizedEventAndRecordsState(t *testing.T) {
	event, _, err := NormalizeContactEvent("instance-a", &events.UserAbout{
		JID: types.NewJID("15550001", types.DefaultUserServer), Status: "Available", Timestamp: time.Unix(500, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	contacts := &captureContactProjection{}
	state := &captureProjectionState{}
	if err := NewContactProjector(contacts, state).Handle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if contacts.patch.Aspect != projection_repository.ContactAspectAbout || contacts.patch.About == nil || *contacts.patch.About != "Available" || len(contacts.patch.Identities) != 2 {
		t.Fatalf("contact patch = %#v", contacts.patch)
	}
	if state.instanceID != "instance-a" || state.resource != contactResource || state.version != ContactsProjectionSchemaVersion || !state.occurredAt.Equal(event.OccurredAt) {
		t.Fatalf("projection state = %#v", state)
	}
}

func TestContactProjectorDoesNotRecordFailedWrite(t *testing.T) {
	event, _, err := NormalizeContactEvent("instance-a", &events.PushName{JID: types.NewJID("15550001", types.DefaultUserServer), NewPushName: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	contacts := &captureContactProjection{err: errors.New("database unavailable")}
	state := &captureProjectionState{}
	if err := NewContactProjector(contacts, state).Handle(context.Background(), event); err == nil {
		t.Fatal("failed contact write was accepted")
	}
	if state.resource != "" {
		t.Fatalf("failed write recorded projection state: %#v", state)
	}
}
