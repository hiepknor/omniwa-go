package projection_service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"gorm.io/gorm"
)

type contactReadRepositoryStub struct {
	contacts     []projection_model.Contact
	searchPage   *projection_repository.ContactPage
	searchTerm   string
	searchLimit  int
	searchCursor *projection_repository.ContactCursor
	getID        string
	getAlias     string
}

func (s *contactReadRepositoryStub) List(context.Context, string) ([]projection_model.Contact, error) {
	return s.contacts, nil
}

func (s *contactReadRepositoryStub) Count(context.Context, string) (int64, error) {
	return int64(len(s.contacts)), nil
}

func (s *contactReadRepositoryStub) Get(_ context.Context, _ string, contactID string) (*projection_model.Contact, error) {
	s.getID = contactID
	if len(s.contacts) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &s.contacts[0], nil
}

func (s *contactReadRepositoryStub) GetByIdentity(_ context.Context, _ string, _ projection_model.ContactIdentityKind, value string) (*projection_model.Contact, error) {
	s.getAlias = value
	if len(s.contacts) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &s.contacts[0], nil
}

func (s *contactReadRepositoryStub) Search(_ context.Context, _ string, term string, limit int, cursor *projection_repository.ContactCursor) (*projection_repository.ContactPage, error) {
	s.searchTerm, s.searchLimit, s.searchCursor = term, limit, cursor
	if s.searchPage != nil {
		return s.searchPage, nil
	}
	return &projection_repository.ContactPage{Items: s.contacts, Total: int64(len(s.contacts))}, nil
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
	if err != nil || contacts == nil || len(contacts) != 0 || meta == nil || meta.Source != "projection" || meta.Total == nil || *meta.Total != 0 || meta.LastSyncedAt == nil || !meta.LastSyncedAt.Equal(reconciledAt) {
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

func TestContactReaderResolvesCanonicalAndAliasReferences(t *testing.T) {
	reconciledAt := time.Unix(500, 0)
	repository := &contactReadRepositoryStub{contacts: []projection_model.Contact{{ContactID: "11111111-1111-1111-1111-111111111111"}}}
	reader := NewContactReader(repository, &labelReaderStateStub{state: &projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: ContactsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}})
	if _, _, err := reader.GetByReference(context.Background(), "instance-a", "22222222-2222-2222-2222-222222222222"); err != nil || repository.getID == "" {
		t.Fatalf("canonical reference = id %q, error %v", repository.getID, err)
	}
	if _, _, err := reader.GetByReference(context.Background(), "instance-a", "15550001@s.whatsapp.net"); err != nil || repository.getAlias != "15550001@s.whatsapp.net" {
		t.Fatalf("alias reference = alias %q, error %v", repository.getAlias, err)
	}
	if _, _, err := reader.GetByReference(context.Background(), "instance-a", "not-a-contact"); !errors.Is(err, ErrInvalidContactReference) {
		t.Fatalf("invalid reference error = %v", err)
	}
}

func TestContactReaderSearchUsesQueryBoundOpaqueCursor(t *testing.T) {
	reconciledAt := time.Unix(500, 0)
	repository := &contactReadRepositoryStub{searchPage: &projection_repository.ContactPage{
		Items:      []projection_model.Contact{{ContactID: "11111111-1111-1111-1111-111111111111", PreferredJID: "alice@s.whatsapp.net"}},
		NextCursor: &projection_repository.ContactCursor{SortKey: "alice", ContactID: "11111111-1111-1111-1111-111111111111"},
		Total:      1,
	}}
	reader := NewContactReader(repository, &labelReaderStateStub{state: &projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: ContactsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}})
	contacts, meta, err := reader.Search(context.Background(), "instance-a", " Alice ", 1, "")
	if err != nil || len(contacts) != 1 || meta == nil || meta.NextCursor == "" || meta.Total == nil || *meta.Total != 1 || repository.searchTerm != "alice" || repository.searchLimit != 1 {
		t.Fatalf("Search() = %#v, %#v, %v repository=%#v", contacts, meta, err, repository)
	}
	cursor := meta.NextCursor
	repository.searchPage = &projection_repository.ContactPage{Items: []projection_model.Contact{}}
	if _, _, err := reader.Search(context.Background(), "instance-a", "ALICE", 1, cursor); err != nil || repository.searchCursor == nil || repository.searchCursor.SortKey != "alice" {
		t.Fatalf("same-scope cursor search error=%v cursor=%#v", err, repository.searchCursor)
	}
	if _, _, err := reader.Search(context.Background(), "instance-a", "bob", 1, cursor); !errors.Is(err, ErrInvalidContactCursor) {
		t.Fatalf("cross-query cursor error = %v", err)
	}
	if _, _, err := reader.Search(context.Background(), "instance-b", "alice", 1, cursor); !errors.Is(err, ErrInvalidContactCursor) {
		t.Fatalf("cross-instance cursor error = %v", err)
	}
}

func TestContactReaderSearchRejectsInvalidBounds(t *testing.T) {
	reconciledAt := time.Unix(500, 0)
	reader := NewContactReader(&contactReadRepositoryStub{}, &labelReaderStateStub{state: &projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: ContactsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}})
	if _, _, err := reader.Search(context.Background(), "instance-a", string(make([]byte, maxContactSearchTerm+1)), 1, ""); !errors.Is(err, ErrInvalidContactSearch) {
		t.Fatalf("oversized search error = %v", err)
	}
	if _, _, err := reader.Search(context.Background(), "instance-a", "", 0, ""); !errors.Is(err, ErrInvalidContactSearch) {
		t.Fatalf("invalid limit error = %v", err)
	}
	if _, _, err := reader.Search(context.Background(), "instance-a", "", 1, "not-base64"); !errors.Is(err, ErrInvalidContactCursor) {
		t.Fatalf("invalid cursor error = %v", err)
	}
	legacyPayload, err := json.Marshal(contactCursorEnvelope{
		Version: 1, Kind: "contacts", Scope: contactCursorScope("instance-a", ""), SortKey: "alice",
		ContactID: "11111111-1111-1111-1111-111111111111",
	})
	if err != nil {
		t.Fatal(err)
	}
	legacyCursor := base64.RawURLEncoding.EncodeToString(legacyPayload)
	if _, _, err := reader.Search(context.Background(), "instance-a", "", 1, legacyCursor); !errors.Is(err, ErrInvalidContactCursor) {
		t.Fatalf("legacy cursor error = %v", err)
	}
}
