package projection_service

import (
	"context"
	"errors"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"go.mau.fi/whatsmeow/types"
	"gorm.io/gorm"
)

type captureContactSnapshots struct {
	patches []projection_repository.ContactPatch
}

func (c *captureContactSnapshots) Apply(_ context.Context, patch projection_repository.ContactPatch) (*projection_model.Contact, bool, error) {
	c.patches = append(c.patches, patch)
	return &projection_model.Contact{}, true, nil
}

type contactSyncStateStub struct {
	state  *projection_model.State
	status projection_model.SyncStatus
}

func (s *contactSyncStateStub) Get(string, string) (*projection_model.State, error) {
	if s.state == nil {
		return nil, gorm.ErrRecordNotFound
	}
	copy := *s.state
	return &copy, nil
}

func (s *contactSyncStateStub) MarkSyncing(instanceID, resource string, schemaVersion int64) error {
	s.status = projection_model.SyncStatusSyncing
	s.state = &projection_model.State{InstanceID: instanceID, Resource: resource, SchemaVersion: schemaVersion, SyncStatus: s.status}
	return nil
}

func (s *contactSyncStateStub) MarkStale(string, string, int64) error {
	s.status = projection_model.SyncStatusStale
	return nil
}

func (s *contactSyncStateStub) MarkFailed(string, string, int64) error {
	s.status = projection_model.SyncStatusFailed
	return nil
}

type captureContactSyncEvents struct{ event *projection_model.Event }

func (c *captureContactSyncEvents) Ingest(_ context.Context, event *projection_model.Event) (bool, error) {
	c.event = event
	return true, nil
}

func TestContactSyncerSnapshotsLocalStoreAndQueuesReadinessBarrier(t *testing.T) {
	writes := &captureContactSnapshots{}
	state := &contactSyncStateStub{}
	events := &captureContactSyncEvents{}
	syncer := NewContactSyncer(writes, state, events)
	syncer.now = func() time.Time { return time.Unix(700, 0) }
	err := syncer.Sync(context.Background(), "instance-a", func(context.Context) (map[types.JID]types.ContactInfo, error) {
		return map[types.JID]types.ContactInfo{
			types.NewJID("15550001", types.DefaultUserServer): {Found: true, FirstName: "Ada", FullName: "Ada Lovelace", PushName: "Ada", BusinessName: "Analytical Engines"},
			types.NewJID("group", types.GroupServer):          {Found: true, FullName: "Not a contact"},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(writes.patches) != 3 {
		t.Fatalf("snapshot patches = %#v", writes.patches)
	}
	for _, patch := range writes.patches {
		if len(patch.EventKey) > 255 || patch.EventKey == "" || patch.PreferredJID != "15550001@s.whatsapp.net" {
			t.Fatalf("invalid snapshot patch = %#v", patch)
		}
	}
	if events.event == nil || events.event.EventType != "contact_sync_complete" || events.event.Resource != contactResource || state.status != projection_model.SyncStatusSyncing {
		t.Fatalf("sync result = event %#v, state %s", events.event, state.status)
	}
}

func TestContactSyncerSkipsReadyProjectionAndMarksInitialFailure(t *testing.T) {
	readyState := &contactSyncStateStub{state: &projection_model.State{SyncStatus: projection_model.SyncStatusReady, SchemaVersion: ContactsProjectionSchemaVersion}}
	fetched := false
	if err := NewContactSyncer(&captureContactSnapshots{}, readyState, &captureContactSyncEvents{}).Sync(context.Background(), "instance-a", func(context.Context) (map[types.JID]types.ContactInfo, error) {
		fetched = true
		return nil, nil
	}); err != nil || fetched {
		t.Fatalf("ready sync = fetched %v, error %v", fetched, err)
	}
	failedState := &contactSyncStateStub{}
	err := NewContactSyncer(&captureContactSnapshots{}, failedState, &captureContactSyncEvents{}).Sync(context.Background(), "instance-a", func(context.Context) (map[types.JID]types.ContactInfo, error) {
		return nil, errors.New("store unavailable")
	})
	if err == nil || failedState.status != projection_model.SyncStatusFailed {
		t.Fatalf("failed sync = status %s, error %v", failedState.status, err)
	}
}

func TestContactSyncCompletionWaitsForPendingMutations(t *testing.T) {
	events := &captureContactSyncEvents{}
	syncer := NewContactSyncer(&captureContactSnapshots{}, &contactSyncStateStub{}, events)
	syncer.now = func() time.Time { return time.Unix(800, 0) }
	if err := syncer.Sync(context.Background(), "instance-a", func(context.Context) (map[types.JID]types.ContactInfo, error) {
		return map[types.JID]types.ContactInfo{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	state := &captureProjectionState{}
	readiness := &captureLabelReadiness{unprocessed: true}
	projector := NewContactProjector(&captureContactProjection{}, state, readiness)
	if err := projector.Handle(context.Background(), events.event); err == nil {
		t.Fatal("contact sync completion ignored pending mutations")
	}
	readiness.unprocessed = false
	if err := projector.Handle(context.Background(), events.event); err != nil {
		t.Fatal(err)
	}
	if state.readyResource != contactResource || state.readyVersion != ContactsProjectionSchemaVersion || state.readyAt.IsZero() {
		t.Fatalf("ready state = %#v", state)
	}
}
