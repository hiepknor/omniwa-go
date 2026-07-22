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
	"gorm.io/gorm"
)

type groupServiceReadRepository struct {
	record    projection_repository.GroupRecord
	listCalls int
	getCalls  int
}

func TestGroupServiceNotReadyReadsNeverFallbackToWhatsApp(t *testing.T) {
	repository := &groupServiceReadRepository{}
	reader := projection_service.NewGroupReader(repository, groupServiceReadState{state: projection_model.State{
		SyncStatus: projection_model.SyncStatusNotStarted, SchemaVersion: projection_service.GroupsProjectionSchemaVersion,
	}})
	service := &groupService{groupReader: reader, clientPointer: nil}
	instance := &instance_model.Instance{Id: "instance-a"}
	if _, _, err := service.ListGroupsRead(context.Background(), instance); !errors.Is(err, projection_service.ErrGroupsProjectionNotReady) {
		t.Fatalf("ListGroupsRead() error = %v", err)
	}
	if _, _, err := service.GetGroupInfoRead(context.Background(), &GetGroupInfoStruct{GroupJID: "group@g.us"}, instance); !errors.Is(err, projection_service.ErrGroupsProjectionNotReady) {
		t.Fatalf("GetGroupInfoRead() error = %v", err)
	}
	if _, err := service.GetGroupInviteLink(context.Background(), &GetGroupInviteLinkStruct{GroupJID: "group@g.us"}, instance); !errors.Is(err, projection_service.ErrGroupsProjectionNotReady) {
		t.Fatalf("GetGroupInviteLink() error = %v", err)
	}
}

func TestGroupServiceMissingCachedInviteLinkDoesNotQueryWhatsApp(t *testing.T) {
	reconciledAt := time.Unix(1000, 0)
	repository := &groupServiceReadRepository{record: projection_repository.GroupRecord{Group: projection_model.Group{GroupID: "group@g.us"}}}
	reader := projection_service.NewGroupReader(repository, groupServiceReadState{state: projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}})
	service := &groupService{groupReader: reader, clientPointer: nil}
	_, err := service.GetGroupInviteLink(context.Background(), &GetGroupInviteLinkStruct{GroupJID: "group@g.us"}, &instance_model.Instance{Id: "instance-a"})
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("GetGroupInviteLink() error = %v", err)
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
	return r.record.Group.InviteLink, nil
}

type groupServiceReadState struct{ state projection_model.State }

func (s groupServiceReadState) Get(string, string) (*projection_model.State, error) {
	return &s.state, nil
}

func TestGroupServiceReadyReadsDoNotRequireWhatsAppClient(t *testing.T) {
	reconciledAt := time.Unix(1000, 0)
	name := "Database group"
	repository := &groupServiceReadRepository{record: projection_repository.GroupRecord{Group: projection_model.Group{GroupID: "group@g.us", Name: &name}}}
	reader := projection_service.NewGroupReader(repository, groupServiceReadState{state: projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}})
	service := &groupService{groupReader: reader, clientPointer: nil}
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
	service := &groupService{groupReader: reader, clientPointer: nil}
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
	service := &groupService{groupReader: reader, clientPointer: nil}
	got, err := service.GetGroupInviteLink(context.Background(), &GetGroupInviteLinkStruct{GroupJID: "group@g.us"}, &instance_model.Instance{Id: "instance-a"})
	if err != nil || got != inviteLink {
		t.Fatalf("GetGroupInviteLink() = %q, %v", got, err)
	}
}
