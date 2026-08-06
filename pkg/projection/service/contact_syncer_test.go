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
	}, nil)
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

func TestContactSyncerEnrichesSnapshotFromLocalLIDMapping(t *testing.T) {
	writes := &captureContactSnapshots{}
	syncer := NewContactSyncer(writes, &contactSyncStateStub{}, &captureContactSyncEvents{})
	syncer.now = func() time.Time { return time.Unix(750, 0) }
	phone := types.NewJID("15550009", types.DefaultUserServer)
	lid := types.NewJID("9000009", types.HiddenUserServer)
	err := syncer.Sync(context.Background(), "instance-a", func(context.Context) (map[types.JID]types.ContactInfo, error) {
		return map[types.JID]types.ContactInfo{phone: {Found: true, FullName: "Mapped"}}, nil
	}, &contactLIDResolverFake{phone: phone, lid: lid})
	if err != nil {
		t.Fatal(err)
	}
	if len(writes.patches) != 3 {
		t.Fatalf("snapshot patches = %#v", writes.patches)
	}
	for _, patch := range writes.patches {
		if patch.PhoneJID == nil || *patch.PhoneJID != phone.String() || patch.LID == nil || *patch.LID != lid.String() ||
			patch.PreferredJID != phone.String() || len(patch.Identities) != 4 {
			t.Fatalf("mapped snapshot patch = %#v", patch)
		}
	}
}

func TestContactSyncerCapturesOnlyDirectSnapshotPhoneEvidence(t *testing.T) {
	repository := &phoneEvidenceRepositoryStub{}
	recorder := NewPhoneIdentityEvidenceRecorder(repository, nil)
	writes := &captureContactSnapshots{}
	syncer := NewContactSyncer(writes, &contactSyncStateStub{}, &captureContactSyncEvents{}).WithPhoneIdentityEvidence(recorder)
	syncer.now = func() time.Time { return time.Unix(760, 0) }
	phone := types.NewJID("15550009", types.DefaultUserServer)
	lid := types.NewJID("9000009", types.HiddenUserServer)
	if err := syncer.Sync(context.Background(), "instance-a", func(context.Context) (map[types.JID]types.ContactInfo, error) {
		return map[types.JID]types.ContactInfo{phone: {Found: true}, lid: {Found: true}}, nil
	}, &contactLIDResolverFake{phone: phone, lid: lid}); err != nil {
		t.Fatal(err)
	}
	if len(repository.observed) != 1 || repository.observed[0].PhoneJID != phone.String() || repository.observed[0].LIDJID != nil {
		t.Fatalf("snapshot evidence included inferred global mapping: %#v", repository.observed)
	}
}

func TestContactSyncerRefreshesReadyProjectionAndMarksInitialFailure(t *testing.T) {
	readyState := &contactSyncStateStub{state: &projection_model.State{SyncStatus: projection_model.SyncStatusReady, SchemaVersion: ContactsProjectionSchemaVersion}}
	fetched := false
	if err := NewContactSyncer(&captureContactSnapshots{}, readyState, &captureContactSyncEvents{}).Sync(context.Background(), "instance-a", func(context.Context) (map[types.JID]types.ContactInfo, error) {
		fetched = true
		return map[types.JID]types.ContactInfo{}, nil
	}, nil); err != nil || !fetched || readyState.status != projection_model.SyncStatusSyncing {
		t.Fatalf("ready sync = fetched %v, error %v", fetched, err)
	}
	failedState := &contactSyncStateStub{}
	err := NewContactSyncer(&captureContactSnapshots{}, failedState, &captureContactSyncEvents{}).Sync(context.Background(), "instance-a", func(context.Context) (map[types.JID]types.ContactInfo, error) {
		return nil, errors.New("store unavailable")
	}, nil)
	if err == nil || failedState.status != projection_model.SyncStatusFailed {
		t.Fatalf("failed sync = status %s, error %v", failedState.status, err)
	}
}

func TestContactSyncerCoalescesConcurrentInstanceRefreshes(t *testing.T) {
	syncer := NewContactSyncer(&captureContactSnapshots{}, &contactSyncStateStub{}, &captureContactSyncEvents{})
	started := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- syncer.Sync(context.Background(), "instance-a", func(context.Context) (map[types.JID]types.ContactInfo, error) {
			close(started)
			<-release
			return map[types.JID]types.ContactInfo{}, nil
		}, nil)
	}()
	<-started
	secondFetched := false
	if err := syncer.Sync(context.Background(), "instance-a", func(context.Context) (map[types.JID]types.ContactInfo, error) {
		secondFetched = true
		return map[types.JID]types.ContactInfo{}, nil
	}, nil); err != nil || secondFetched {
		t.Fatalf("concurrent refresh was not coalesced: fetched=%v error=%v", secondFetched, err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("primary refresh failed: %v", err)
	}
}

func TestContactSyncCompletionWaitsForPendingMutations(t *testing.T) {
	events := &captureContactSyncEvents{}
	syncer := NewContactSyncer(&captureContactSnapshots{}, &contactSyncStateStub{}, events)
	syncer.now = func() time.Time { return time.Unix(800, 0) }
	if err := syncer.Sync(context.Background(), "instance-a", func(context.Context) (map[types.JID]types.ContactInfo, error) {
		return map[types.JID]types.ContactInfo{}, nil
	}, nil); err != nil {
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
