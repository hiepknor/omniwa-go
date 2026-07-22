package group_service

import (
	"context"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
)

type groupServiceReadRepository struct {
	record    projection_repository.GroupRecord
	listCalls int
	getCalls  int
}

func (r *groupServiceReadRepository) List(context.Context, string) ([]projection_repository.GroupRecord, error) {
	r.listCalls++
	return []projection_repository.GroupRecord{r.record}, nil
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
