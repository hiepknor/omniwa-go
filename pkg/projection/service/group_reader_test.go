package projection_service

import (
	"context"
	"errors"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"gorm.io/gorm"
)

type groupReaderRepositoryStub struct {
	records []projection_repository.GroupRecord
	get     *projection_repository.GroupRecord
	calls   int
	page    *projection_repository.GroupPage
	cursor  *projection_repository.GroupCursor
}

func (s *groupReaderRepositoryStub) List(context.Context, string) ([]projection_repository.GroupRecord, error) {
	s.calls++
	return s.records, nil
}

func (s *groupReaderRepositoryStub) Search(_ context.Context, _ string, _ string, _ int, cursor *projection_repository.GroupCursor) (*projection_repository.GroupPage, error) {
	s.calls++
	s.cursor = cursor
	if s.page != nil {
		return s.page, nil
	}
	return &projection_repository.GroupPage{Items: s.records}, nil
}

func (s *groupReaderRepositoryStub) Get(context.Context, string, string) (*projection_model.Group, []projection_model.GroupParticipant, error) {
	s.calls++
	if s.get == nil {
		return nil, nil, gorm.ErrRecordNotFound
	}
	return &s.get.Group, s.get.Participants, nil
}

func (s *groupReaderRepositoryStub) GetInviteLink(context.Context, string, string) (*string, error) {
	s.calls++
	if s.get == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return s.get.Group.InviteLink, nil
}

type groupReaderStateStub struct {
	state *projection_model.State
	err   error
}

func (s groupReaderStateStub) GetServingState(string, string) (*projection_model.State, error) {
	return s.state, s.err
}

func TestGroupReaderReturnsCompatibleProjectionAndMetadata(t *testing.T) {
	reconciledAt := time.Unix(900, 0)
	name := "Projected group"
	owner := "owner@s.whatsapp.net"
	topicDeleted := true
	count := 1
	repository := &groupReaderRepositoryStub{records: []projection_repository.GroupRecord{{
		Group: projection_model.Group{
			GroupID: "group@g.us", Name: &name, OwnerJID: &owner, TopicDeleted: &topicDeleted,
			ParticipantCount: &count, ProviderCreatedAt: &reconciledAt,
		},
		Participants: []projection_model.GroupParticipant{{ParticipantID: owner, Role: projection_model.ParticipantRoleSuperAdmin}},
	}}}
	reader := NewGroupReader(repository, groupReaderStateStub{state: &projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}})

	groups, meta, err := reader.List(context.Background(), "instance-a")
	if err != nil || len(groups) != 1 || groups[0].JID.String() != "group@g.us" || groups[0].Name != name ||
		groups[0].OwnerJID.String() != owner || !groups[0].TopicDeleted || groups[0].ParticipantCount != 1 ||
		len(groups[0].Participants) != 1 || !groups[0].Participants[0].IsSuperAdmin {
		t.Fatalf("List() = %#v, %#v, %v", groups, meta, err)
	}
	if meta == nil || meta.Source != "projection" || meta.SyncStatus != projection_model.SyncStatusReady || meta.LastSyncedAt == nil || !meta.LastSyncedAt.Equal(reconciledAt) {
		t.Fatalf("projection metadata = %#v", meta)
	}
}

func TestGroupReaderDoesNotReadBeforeCompleteReconciliation(t *testing.T) {
	for _, state := range []*projection_model.State{
		{SyncStatus: projection_model.SyncStatusNotStarted, SchemaVersion: GroupsProjectionSchemaVersion},
		{SyncStatus: projection_model.SyncStatusReady, SchemaVersion: GroupsProjectionSchemaVersion - 1, LastReconciledAt: timePointer(time.Unix(1, 0))},
		{SyncStatus: projection_model.SyncStatusFailed, SchemaVersion: GroupsProjectionSchemaVersion, LastReconciledAt: timePointer(time.Unix(1, 0))},
	} {
		repository := &groupReaderRepositoryStub{}
		reader := NewGroupReader(repository, groupReaderStateStub{state: state})
		if _, _, err := reader.List(context.Background(), "instance-a"); !errors.Is(err, ErrGroupsProjectionNotReady) {
			t.Fatalf("state %#v returned error %v", state, err)
		}
		if repository.calls != 0 {
			t.Fatalf("projection rows were read before state was usable: %d", repository.calls)
		}
	}
}

func TestGroupReaderUsesStaleDataWithoutProviderProbe(t *testing.T) {
	reconciledAt := time.Unix(500, 0)
	repository := &groupReaderRepositoryStub{records: []projection_repository.GroupRecord{}}
	reader := NewGroupReader(repository, groupReaderStateStub{state: &projection_model.State{
		SyncStatus: projection_model.SyncStatusStale, SchemaVersion: GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}})
	groups, meta, err := reader.List(context.Background(), "instance-a")
	if err != nil || groups == nil || len(groups) != 0 || meta == nil || meta.SyncStatus != projection_model.SyncStatusStale || repository.calls != 1 {
		t.Fatalf("stale List() = %#v, %#v, %v calls=%d", groups, meta, err, repository.calls)
	}
}

func TestGroupReaderReturnsCachedInviteLink(t *testing.T) {
	reconciledAt := time.Unix(500, 0)
	inviteLink := "https://chat.whatsapp.com/cached"
	repository := &groupReaderRepositoryStub{get: &projection_repository.GroupRecord{Group: projection_model.Group{InviteLink: &inviteLink}}}
	reader := NewGroupReader(repository, groupReaderStateStub{state: &projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}})
	got, meta, found, err := reader.InviteLink(context.Background(), "instance-a", "group@g.us")
	if err != nil || !found || got != inviteLink || meta == nil || repository.calls != 1 {
		t.Fatalf("InviteLink() = %q, %#v, %v, %v calls=%d", got, meta, found, err, repository.calls)
	}
}

func TestGroupReaderSearchCursorIsOpaqueAndQueryScoped(t *testing.T) {
	reconciledAt := time.Unix(500, 0)
	repository := &groupReaderRepositoryStub{page: &projection_repository.GroupPage{
		Items: []projection_repository.GroupRecord{}, NextCursor: &projection_repository.GroupCursor{GroupID: "123@g.us"},
	}}
	reader := NewGroupReader(repository, groupReaderStateStub{state: &projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}})
	items, meta, err := reader.Search(context.Background(), "instance-a", " Alpha ", 10, "")
	if err != nil || items == nil || meta == nil || meta.NextCursor == "" {
		t.Fatalf("Search() = %#v, %#v, %v", items, meta, err)
	}
	repository.page.NextCursor = nil
	if _, _, err := reader.Search(context.Background(), "instance-a", "alpha", 10, meta.NextCursor); err != nil || repository.cursor == nil || repository.cursor.GroupID != "123@g.us" {
		t.Fatalf("decoded cursor = %#v, error=%v", repository.cursor, err)
	}
	if _, _, err := reader.Search(context.Background(), "instance-a", "beta", 10, meta.NextCursor); !errors.Is(err, ErrInvalidGroupCursor) {
		t.Fatalf("cross-query cursor error = %v", err)
	}
	if _, _, err := reader.Search(context.Background(), "instance-b", "alpha", 10, meta.NextCursor); !errors.Is(err, ErrInvalidGroupCursor) {
		t.Fatalf("cross-instance cursor error = %v", err)
	}
}

func timePointer(value time.Time) *time.Time { return &value }
