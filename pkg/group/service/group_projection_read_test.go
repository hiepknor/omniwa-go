package group_service

import (
	"context"
	"errors"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type groupServiceReadRepository struct {
	record        projection_repository.GroupRecord
	inviteLinkErr error
	inviteCalls   int
	listCalls     int
	getCalls      int
}

func inviteLinkManagementReader(state projection_model.State, inviteLink *string, recordErr error) *ManagementReader {
	suspended, announce, isParent := false, false, false
	membership := projection_model.GroupActorMembershipJoined
	return NewManagementReader(&managementReadRepositoryStub{recordErr: recordErr, record: &projection_repository.GroupManagementRecord{
		Group: projection_model.Group{
			GroupID: "group@g.us", Suspended: &suspended, Announce: &announce, IsParent: &isParent,
			MembershipState: &membership, InviteLink: inviteLink, UpdatedAt: time.Unix(1000, 0).UTC(),
		},
		ActorParticipant: &projection_model.GroupParticipant{ParticipantID: "actor@s.whatsapp.net", Role: projection_model.ParticipantRoleAdmin},
	}}, managementReadStateStub{state: &state})
}

func TestGroupServiceNotReadyReadsNeverFallbackToWhatsApp(t *testing.T) {
	repository := &groupServiceReadRepository{}
	reader := projection_service.NewGroupReader(repository, groupServiceReadState{state: projection_model.State{
		SyncStatus: projection_model.SyncStatusNotStarted, SchemaVersion: projection_service.GroupsProjectionSchemaVersion,
	}})
	service := &groupService{groupReader: reader, managementReader: inviteLinkManagementReader(projection_model.State{
		SyncStatus: projection_model.SyncStatusNotStarted, SchemaVersion: projection_service.GroupsProjectionSchemaVersion,
	}, nil, nil)}
	instance := &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"}
	if _, _, err := service.ListGroupsRead(context.Background(), instance); !errors.Is(err, projection_service.ErrGroupsProjectionNotReady) {
		t.Fatalf("ListGroupsRead() error = %v", err)
	}
	if _, _, err := service.GetGroupInfoRead(context.Background(), &GetGroupInfoStruct{GroupJID: "group@g.us"}, instance); !errors.Is(err, projection_service.ErrGroupsProjectionNotReady) {
		t.Fatalf("GetGroupInfoRead() error = %v", err)
	}
	if _, _, err := service.GetGroupInviteLink(context.Background(), &GetGroupInviteLinkStruct{GroupJID: "group@g.us"}, instance); !errors.Is(err, projection_service.ErrGroupsProjectionNotReady) {
		t.Fatalf("GetGroupInviteLink() error = %v", err)
	}
}

func TestGroupServiceInviteLinkRequiresCanonicalGroupJID(t *testing.T) {
	service := &groupService{}
	for _, groupJID := range []string{"not-a-jid", "15550000001@s.whatsapp.net"} {
		if _, _, err := service.GetGroupInviteLink(context.Background(), &GetGroupInviteLinkStruct{GroupJID: groupJID}, &instance_model.Instance{Id: "instance-a"}); !errors.Is(err, ErrInvalidManagementFilter) {
			t.Fatalf("groupJid=%q error=%v", groupJID, err)
		}
	}
}

func TestGroupServiceMissingCachedInviteLinkDoesNotQueryWhatsApp(t *testing.T) {
	reconciledAt := time.Unix(1000, 0)
	repository := &groupServiceReadRepository{record: projection_repository.GroupRecord{Group: projection_model.Group{GroupID: "group@g.us"}}}
	reader := projection_service.NewGroupReader(repository, groupServiceReadState{state: projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}})
	service := &groupService{groupReader: reader, managementReader: inviteLinkManagementReader(projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}, nil, nil)}
	_, meta, err := service.GetGroupInviteLink(context.Background(), &GetGroupInviteLinkStruct{GroupJID: "group@g.us"}, &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"})
	if !errors.Is(err, ErrGroupInviteLinkNotFound) || meta == nil || meta.SyncStatus != projection_model.SyncStatusReady || repository.inviteCalls != 0 {
		t.Fatalf("GetGroupInviteLink() error = %v", err)
	}
}

func TestGroupServiceInviteLinkPreservesStaleAndSyncingMetadata(t *testing.T) {
	reconciledAt := time.Unix(1000, 0)
	inviteLink := "https://chat.whatsapp.com/cached"
	for _, status := range []projection_model.SyncStatus{projection_model.SyncStatusStale, projection_model.SyncStatusSyncing} {
		t.Run(string(status), func(t *testing.T) {
			repository := &groupServiceReadRepository{record: projection_repository.GroupRecord{Group: projection_model.Group{InviteLink: &inviteLink}}}
			reader := projection_service.NewGroupReader(repository, groupServiceReadState{state: projection_model.State{
				SyncStatus: status, SchemaVersion: projection_service.GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
			}})
			service := &groupService{groupReader: reader, managementReader: inviteLinkManagementReader(projection_model.State{
				SyncStatus: status, SchemaVersion: projection_service.GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
			}, &inviteLink, nil)}
			got, meta, err := service.GetGroupInviteLink(context.Background(), &GetGroupInviteLinkStruct{GroupJID: "group@g.us"}, &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"})
			if err != nil || got != inviteLink || meta == nil || meta.SyncStatus != status {
				t.Fatalf("GetGroupInviteLink() = %q, %#v, %v", got, meta, err)
			}
		})
	}
}

func (r *groupServiceReadRepository) List(context.Context, string) ([]projection_repository.GroupRecord, error) {
	r.listCalls++
	return []projection_repository.GroupRecord{r.record}, nil
}

func (r *groupServiceReadRepository) Search(context.Context, string, string, int, *projection_repository.GroupCursor) (*projection_repository.GroupPage, error) {
	r.listCalls++
	return &projection_repository.GroupPage{Items: []projection_repository.GroupRecord{r.record}}, nil
}

func (r *groupServiceReadRepository) Get(context.Context, string, string) (*projection_model.Group, []projection_model.GroupParticipant, error) {
	r.getCalls++
	return &r.record.Group, r.record.Participants, nil
}

func (r *groupServiceReadRepository) GetInviteLink(context.Context, string, string) (*string, error) {
	r.inviteCalls++
	if r.inviteLinkErr != nil {
		return nil, r.inviteLinkErr
	}
	return r.record.Group.InviteLink, nil
}

func TestGroupServiceInviteLinkRevalidatesReadPermission(t *testing.T) {
	reconciledAt := time.Unix(1000, 0)
	inviteLink := "https://chat.whatsapp.com/cached"
	for _, test := range []struct {
		name string
		role projection_model.ParticipantRole
		want error
	}{
		{name: "denied", role: projection_model.ParticipantRoleMember, want: ErrManagementPermissionDenied},
		{name: "unknown", role: projection_model.ParticipantRole("unknown"), want: ErrManagementPermissionUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &groupServiceReadRepository{record: projection_repository.GroupRecord{Group: projection_model.Group{InviteLink: &inviteLink}}}
			reader := projection_service.NewGroupReader(repository, groupServiceReadState{state: projection_model.State{
				SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
			}})
			service := &groupService{groupReader: reader, managementReader: managementCommandReader(test.role, false)}
			_, _, err := service.GetGroupInviteLink(context.Background(), &GetGroupInviteLinkStruct{GroupJID: "123@g.us"}, &instance_model.Instance{Id: uuid.NewString(), Jid: "actor@s.whatsapp.net"})
			if !errors.Is(err, test.want) || repository.inviteCalls != 0 {
				t.Fatalf("error=%v inviteCalls=%d", err, repository.inviteCalls)
			}
		})
	}
}

func TestGroupServiceMissingGroupIsDistinctFromMissingCachedInviteLink(t *testing.T) {
	reconciledAt := time.Unix(1000, 0)
	repository := &groupServiceReadRepository{inviteLinkErr: gorm.ErrRecordNotFound}
	reader := projection_service.NewGroupReader(repository, groupServiceReadState{state: projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}})
	service := &groupService{groupReader: reader, managementReader: inviteLinkManagementReader(projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}, nil, gorm.ErrRecordNotFound)}
	_, meta, err := service.GetGroupInviteLink(context.Background(), &GetGroupInviteLinkStruct{GroupJID: "group@g.us"}, &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"})
	if !errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, ErrGroupInviteLinkNotFound) || meta == nil || repository.inviteCalls != 0 {
		t.Fatalf("GetGroupInviteLink() meta=%#v error=%v", meta, err)
	}
}

type groupServiceReadState struct{ state projection_model.State }

func (s groupServiceReadState) GetServingState(string, string) (*projection_model.State, error) {
	return &s.state, nil
}

type inviteLinkProjectionWriteState struct{ staleCalls int }

func (*inviteLinkProjectionWriteState) RecordEvent(string, string, int64, time.Time) error {
	return nil
}
func (s *inviteLinkProjectionWriteState) MarkStale(string, string, int64) error {
	s.staleCalls++
	return nil
}

func TestConfirmedInviteLinkWriteThroughMustPersistBeforeSuccess(t *testing.T) {
	instanceID := uuid.NewString()
	state := &inviteLinkProjectionWriteState{}
	repository := &groupPhotoProjectionRepositoryStub{}
	writer := projection_service.NewGroupWriter(repository, state)
	service := &groupService{groupWriter: writer}
	if err := service.writeGroupProjectionResult(instanceID, func(ctx context.Context) error {
		return writer.WriteInviteLink(ctx, instanceID, "group@g.us", "https://chat.whatsapp.com/new")
	}); err != nil || len(repository.patches) != 1 || repository.patches[0].InviteLink == nil || *repository.patches[0].InviteLink != "https://chat.whatsapp.com/new" {
		t.Fatalf("patches=%#v stale=%d error=%v", repository.patches, state.staleCalls, err)
	}

	repository.err = errors.New("database unavailable")
	if err := service.writeGroupProjectionResult(instanceID, func(ctx context.Context) error {
		return writer.WriteInviteLink(ctx, instanceID, "group@g.us", "https://chat.whatsapp.com/unknown")
	}); err == nil || state.staleCalls != 1 || len(repository.patches) != 1 {
		t.Fatalf("patches=%#v stale=%d error=%v", repository.patches, state.staleCalls, err)
	}
}

func TestGroupServiceReadyReadsDoNotRequireWhatsAppClient(t *testing.T) {
	reconciledAt := time.Unix(1000, 0)
	name := "Database group"
	repository := &groupServiceReadRepository{record: projection_repository.GroupRecord{Group: projection_model.Group{GroupID: "group@g.us", Name: &name}}}
	reader := projection_service.NewGroupReader(repository, groupServiceReadState{state: projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}})
	service := &groupService{groupReader: reader}
	instance := &instance_model.Instance{Id: "instance-a"}

	groups, listMeta, err := service.ListGroupsRead(context.Background(), instance)
	if err != nil || len(groups) != 1 || listMeta == nil || repository.listCalls != 1 {
		t.Fatalf("ListGroupsRead() = %#v, %#v, %v calls=%d", groups, listMeta, err, repository.listCalls)
	}
	info, infoMeta, err := service.GetGroupInfoRead(context.Background(), &GetGroupInfoStruct{GroupJID: "group@g.us"}, instance)
	if err != nil || info == nil || info.Name != name || infoMeta == nil || repository.getCalls != 1 {
		t.Fatalf("GetGroupInfoRead() = %#v, %#v, %v calls=%d", info, infoMeta, err, repository.getCalls)
	}
}

func TestGroupServiceSearchDoesNotRequireWhatsAppClient(t *testing.T) {
	reconciledAt := time.Unix(1000, 0)
	name := "Database group"
	repository := &groupServiceReadRepository{record: projection_repository.GroupRecord{Group: projection_model.Group{GroupID: "group@g.us", Name: &name}}}
	reader := projection_service.NewGroupReader(repository, groupServiceReadState{state: projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}})
	service := &groupService{groupReader: reader}
	groups, meta, err := service.SearchGroupsRead(context.Background(), &instance_model.Instance{Id: "instance-a"}, "database", 25, "")
	if err != nil || len(groups) != 1 || groups[0].Name != name || meta == nil || repository.listCalls != 1 {
		t.Fatalf("SearchGroupsRead() = %#v, %#v, %v calls=%d", groups, meta, err, repository.listCalls)
	}
}

func TestGroupServiceCachedInviteLinkDoesNotRequireWhatsAppClient(t *testing.T) {
	reconciledAt := time.Unix(1000, 0)
	inviteLink := "https://chat.whatsapp.com/cached"
	repository := &groupServiceReadRepository{record: projection_repository.GroupRecord{Group: projection_model.Group{GroupID: "group@g.us", InviteLink: &inviteLink}}}
	reader := projection_service.NewGroupReader(repository, groupServiceReadState{state: projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}})
	service := &groupService{groupReader: reader, managementReader: inviteLinkManagementReader(projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}, &inviteLink, nil)}
	got, meta, err := service.GetGroupInviteLink(context.Background(), &GetGroupInviteLinkStruct{GroupJID: "group@g.us"}, &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"})
	if err != nil || got != inviteLink || meta == nil || meta.SyncStatus != projection_model.SyncStatusReady || repository.inviteCalls != 0 {
		t.Fatalf("GetGroupInviteLink() = %q, %#v, %v", got, meta, err)
	}
}
