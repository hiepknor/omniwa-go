package projection_service

import (
	"context"
	"errors"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"gorm.io/gorm"
)

type contactReadRepositoryStub struct{ contacts []projection_model.Contact }

func (s *contactReadRepositoryStub) List(context.Context, string) ([]projection_model.Contact, error) {
	return s.contacts, nil
}

func (s *contactReadRepositoryStub) GetByIdentity(context.Context, string, projection_model.ContactIdentityKind, string) (*projection_model.Contact, error) {
	if len(s.contacts) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &s.contacts[0], nil
}

func TestContactReaderDistinguishesReadyEmptyFromNotReady(t *testing.T) {
	state := &labelReaderStateStub{err: gorm.ErrRecordNotFound}
	reader := NewContactReader(&contactReadRepositoryStub{}, state)
	if _, _, err := reader.List(context.Background(), "instance-a"); !errors.Is(err, ErrContactsProjectionNotReady) {
		t.Fatalf("not-ready List() error = %v", err)
	}
	reconciledAt := time.Unix(500, 0)
	state.err = nil
	state.state = &projection_model.State{
		InstanceID: "instance-a", Resource: contactResource, SyncStatus: projection_model.SyncStatusReady,
		SchemaVersion: ContactsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}
	contacts, meta, err := reader.List(context.Background(), "instance-a")
	if err != nil || contacts == nil || len(contacts) != 0 || meta == nil || meta.Source != "projection" || meta.LastSyncedAt == nil || !meta.LastSyncedAt.Equal(reconciledAt) {
		t.Fatalf("ready empty List() = %#v, %#v, %v", contacts, meta, err)
	}
}

func TestContactReaderAllowsStaleSnapshotButRejectsUnreconciledSync(t *testing.T) {
	reconciledAt := time.Unix(500, 0)
	contact := projection_model.Contact{ContactID: "contact-1", PreferredJID: "15550001@s.whatsapp.net"}
	state := &labelReaderStateStub{state: &projection_model.State{
		SyncStatus: projection_model.SyncStatusStale, SchemaVersion: ContactsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}}
	reader := NewContactReader(&contactReadRepositoryStub{contacts: []projection_model.Contact{contact}}, state)
	contacts, meta, err := reader.List(context.Background(), "instance-a")
	if err != nil || len(contacts) != 1 || meta == nil || meta.SyncStatus != projection_model.SyncStatusStale {
		t.Fatalf("stale List() = %#v, %#v, %v", contacts, meta, err)
	}
	state.state = &projection_model.State{SyncStatus: projection_model.SyncStatusSyncing, SchemaVersion: ContactsProjectionSchemaVersion}
	if _, _, err := reader.List(context.Background(), "instance-a"); !errors.Is(err, ErrContactsProjectionNotReady) {
		t.Fatalf("unreconciled syncing List() error = %v", err)
	}
}

func TestContactReaderGetsContactByStableJID(t *testing.T) {
	reconciledAt := time.Unix(500, 0)
	projected := projection_model.Contact{ContactID: "contact-1", PreferredJID: "15550001@s.whatsapp.net"}
	reader := NewContactReader(&contactReadRepositoryStub{contacts: []projection_model.Contact{projected}}, &labelReaderStateStub{state: &projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: ContactsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}})
	contact, meta, err := reader.GetByJID(context.Background(), "instance-a", projected.PreferredJID)
	if err != nil || contact == nil || contact.ContactID != projected.ContactID || meta == nil {
		t.Fatalf("GetByJID() = %#v, %#v, %v", contact, meta, err)
	}
}
