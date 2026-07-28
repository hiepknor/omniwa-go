package group_service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	group_model "github.com/evolution-foundation/evolution-go/pkg/group/model"
	group_repository "github.com/evolution-foundation/evolution-go/pkg/group/repository"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/outbound"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types"
)

type managementCommandRepositoryStub struct {
	command       *group_model.ManagementCommand
	created       bool
	createCalls   int
	executeCalls  int
	completeCalls int
	completedAs   group_model.ManagementCommandStatus
}

func (s *managementCommandRepositoryStub) Create(_ context.Context, input group_repository.CreateManagementCommandInput) (*group_model.ManagementCommand, bool, error) {
	s.createCalls++
	if s.command == nil {
		var groupJID *string
		if input.GroupJID != "" {
			value := input.GroupJID
			groupJID = &value
		}
		s.command = &group_model.ManagementCommand{ID: input.ID, InstanceID: input.InstanceID, GroupJID: groupJID, CommandType: input.CommandType, Status: group_model.ManagementCommandRequested, SafeOutcome: json.RawMessage(`{}`)}
	}
	return s.command, s.created, nil
}
func (s *managementCommandRepositoryStub) Get(context.Context, string, string) (*group_model.ManagementCommand, error) {
	return s.command, nil
}
func (s *managementCommandRepositoryStub) GetByIdempotencyHash(context.Context, string, string) (*group_model.ManagementCommand, error) {
	return s.command, nil
}
func (s *managementCommandRepositoryStub) MarkExecuting(context.Context, string, string) (*group_model.ManagementCommand, error) {
	s.executeCalls++
	s.command.Status = group_model.ManagementCommandExecuting
	return s.command, nil
}
func (s *managementCommandRepositoryStub) Complete(_ context.Context, _, _ string, input group_repository.CompleteManagementCommandInput) (*group_model.ManagementCommand, error) {
	s.completeCalls++
	s.completedAs = input.Status
	s.command.Status, s.command.SafeOutcome = input.Status, input.SafeOutcome
	return s.command, nil
}
func (s *managementCommandRepositoryStub) RecoverStaleExecuting(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}
func (s *managementCommandRepositoryStub) ListAudit(context.Context, string, string, int, *group_repository.ManagementAuditCursor) (*group_repository.ManagementAuditPage, error) {
	return nil, nil
}
func (s *managementCommandRepositoryStub) ListPublicAudit(context.Context, string, string, int, *group_repository.ManagementAuditCursor) (*group_repository.ManagementPublicAuditPage, error) {
	return nil, nil
}

type managementCommandProviderStub struct {
	prepareCalls       int
	prepareErr         error
	nameCalls          int
	nameErr            error
	participantCalls   int
	participantResults []types.GroupParticipant
	participantErr     error
	createGroup        *types.GroupInfo
	createErr          error
	joinResult         managementJoinProviderResult
	joinErr            error
	photoCalls         int
	photoID            string
	photoErr           error
}

func (s *managementCommandProviderStub) PrepareManagementCommand(context.Context, string) error {
	s.prepareCalls++
	return s.prepareErr
}
func (s *managementCommandProviderStub) SetManagementName(context.Context, string, types.JID, string) error {
	s.nameCalls++
	return s.nameErr
}
func (*managementCommandProviderStub) SetManagementDescription(context.Context, string, types.JID, string) error {
	return nil
}
func (*managementCommandProviderStub) SetManagementSetting(context.Context, string, types.JID, string) error {
	return nil
}
func (s *managementCommandProviderStub) SetManagementPhoto(context.Context, string, types.JID, []byte) (string, error) {
	s.photoCalls++
	return s.photoID, s.photoErr
}
func (*managementCommandProviderStub) LeaveManagementGroup(context.Context, string, types.JID) error {
	return nil
}
func (*managementCommandProviderStub) ResetManagementInviteLink(context.Context, string, types.JID) error {
	return nil
}
func (s *managementCommandProviderStub) UpdateManagementParticipants(context.Context, string, types.JID, []types.JID, string) ([]types.GroupParticipant, error) {
	s.participantCalls++
	return s.participantResults, s.participantErr
}
func (s *managementCommandProviderStub) CreateManagementGroup(context.Context, string, string, []types.JID) (*types.GroupInfo, error) {
	return s.createGroup, s.createErr
}
func (s *managementCommandProviderStub) JoinManagementGroup(context.Context, string, string) (managementJoinProviderResult, error) {
	return s.joinResult, s.joinErr
}

func managementCommandReader(role projection_model.ParticipantRole, owner bool) *ManagementReader {
	suspended, announce, isParent := false, false, false
	membership := projection_model.GroupActorMembershipJoined
	record := &projection_repository.GroupManagementRecord{
		Group:            projection_model.Group{GroupID: "123@g.us", Suspended: &suspended, Announce: &announce, IsParent: &isParent, MembershipState: &membership},
		ActorParticipant: &projection_model.GroupParticipant{ParticipantID: "actor@s.whatsapp.net", Role: role}, ActorIsOwner: owner,
	}
	return NewManagementReader(&managementReadRepositoryStub{record: record}, managementReadStateStub{state: readyManagementState()})
}

func TestManagementCommandPersistsBeforeSingleProviderAdmission(t *testing.T) {
	repository := &managementCommandRepositoryStub{created: true}
	provider := &managementCommandProviderStub{}
	manager := NewManagementCommandManager(repository, managementCommandReader(projection_model.ParticipantRoleSuperAdmin, true), provider)
	acknowledgement, err := manager.SetName(context.Background(), &instance_model.Instance{Id: uuid.NewString(), Jid: "actor@s.whatsapp.net"}, &SetGroupNameStruct{GroupJID: "123@g.us", Name: "New name"}, ManagementCommandMetadata{ActorReference: "secret-instance-token", RequestID: "request-1234567890"})
	if err != nil || acknowledgement.Status != "completed" || repository.createCalls != 1 || repository.executeCalls != 1 || repository.completeCalls != 1 || repository.completedAs != group_model.ManagementCommandCompleted || provider.prepareCalls != 1 || provider.nameCalls != 1 {
		t.Fatalf("ack=%#v err=%v repository=%#v provider=%#v", acknowledgement, err, repository, provider)
	}
}

func TestManagementCommandUnknownOutcomeIsNeverRetried(t *testing.T) {
	repository := &managementCommandRepositoryStub{created: true}
	provider := &managementCommandProviderStub{nameErr: errors.New("transport closed after admission")}
	manager := NewManagementCommandManager(repository, managementCommandReader(projection_model.ParticipantRoleSuperAdmin, true), provider)
	acknowledgement, err := manager.SetName(context.Background(), &instance_model.Instance{Id: uuid.NewString(), Jid: "actor@s.whatsapp.net"}, &SetGroupNameStruct{GroupJID: "123@g.us", Name: "New name"}, ManagementCommandMetadata{ActorReference: "secret-instance-token"})
	if err != nil || acknowledgement.Status != "unknown" || provider.nameCalls != 1 || repository.completedAs != group_model.ManagementCommandUnknown {
		t.Fatalf("ack=%#v err=%v repository=%#v provider=%#v", acknowledgement, err, repository, provider)
	}
}

func TestManagementCommandRevalidatesPermissionBeforeProviderAdmission(t *testing.T) {
	repository := &managementCommandRepositoryStub{created: true}
	provider := &managementCommandProviderStub{}
	manager := NewManagementCommandManager(repository, managementCommandReader(projection_model.ParticipantRoleMember, false), provider)
	_, err := manager.SetName(context.Background(), &instance_model.Instance{Id: uuid.NewString(), Jid: "actor@s.whatsapp.net"}, &SetGroupNameStruct{GroupJID: "123@g.us", Name: "New name"}, ManagementCommandMetadata{ActorReference: "secret-instance-token"})
	if !errors.Is(err, ErrManagementPermissionDenied) || provider.prepareCalls != 0 || provider.nameCalls != 0 || repository.executeCalls != 0 || repository.completedAs != group_model.ManagementCommandFailed {
		t.Fatalf("error=%v repository=%#v provider=%#v", err, repository, provider)
	}
}

func TestManagementCommandReturnsRateLimitWithoutProviderAdmission(t *testing.T) {
	repository := &managementCommandRepositoryStub{created: true}
	provider := &managementCommandProviderStub{prepareErr: &outbound.RateLimitError{RetryAfter: time.Second}}
	manager := NewManagementCommandManager(repository, managementCommandReader(projection_model.ParticipantRoleAdmin, false), provider)

	_, err := manager.SetName(context.Background(), &instance_model.Instance{Id: uuid.NewString(), Jid: "actor@s.whatsapp.net"}, &SetGroupNameStruct{GroupJID: "123@g.us", Name: "updated"}, ManagementCommandMetadata{ActorReference: "secret-instance-token"})
	if _, limited := outbound.RetryAfter(err); !limited {
		t.Fatalf("SetName() error = %v, want outbound rate limit", err)
	}
	if provider.nameCalls != 0 || repository.executeCalls != 0 || repository.completedAs != group_model.ManagementCommandFailed {
		t.Fatalf("calls/status = provider:%d executing:%d status:%s", provider.nameCalls, repository.executeCalls, repository.completedAs)
	}
}

func TestManagementParticipantCommandPreservesPartialOutcomes(t *testing.T) {
	repository := &managementCommandRepositoryStub{created: true}
	first, _ := types.ParseJID("15550000001@s.whatsapp.net")
	second, _ := types.ParseJID("15550000002@s.whatsapp.net")
	provider := &managementCommandProviderStub{participantResults: []types.GroupParticipant{{JID: first}, {JID: second, Error: 403}}}
	manager := NewManagementCommandManager(repository, managementCommandReader(projection_model.ParticipantRoleSuperAdmin, true), provider)
	result, err := manager.UpdateParticipants(context.Background(), &instance_model.Instance{Id: uuid.NewString(), Jid: "actor@s.whatsapp.net"}, &ManagementParticipantRequest{
		GroupJID: "123@g.us", Action: "add", Participants: []string{first.String(), second.String()},
	}, ManagementCommandMetadata{ActorReference: "secret-instance-token"})
	if err != nil || result.Status != "partially_completed" || result.SucceededCount != 1 || result.FailedCount != 1 || len(result.Outcomes) != 2 || result.Outcomes[0].Status != "succeeded" || valueOrEmpty(result.Outcomes[1].Code) != "provider_rejected" || repository.completedAs != group_model.ManagementCommandPartiallyCompleted || provider.participantCalls != 1 {
		t.Fatalf("result=%#v error=%v repository=%#v provider=%#v", result, err, repository, provider)
	}
	if strings.Contains(string(repository.command.SafeOutcome), first.String()) || !strings.HasPrefix(result.Outcomes[0].Participant, "participant_") {
		t.Fatalf("safe outcome exposed provider participant identity: %s", repository.command.SafeOutcome)
	}
}

func TestManagementParticipantProviderErrorProducesUnknownOutcomes(t *testing.T) {
	repository := &managementCommandRepositoryStub{created: true}
	participant, _ := types.ParseJID("15550000001@s.whatsapp.net")
	provider := &managementCommandProviderStub{participantErr: errors.New("connection closed")}
	manager := NewManagementCommandManager(repository, managementCommandReader(projection_model.ParticipantRoleSuperAdmin, true), provider)
	result, err := manager.UpdateParticipants(context.Background(), &instance_model.Instance{Id: uuid.NewString(), Jid: "actor@s.whatsapp.net"}, &ManagementParticipantRequest{
		GroupJID: "123@g.us", Action: "add", Participants: []string{participant.String()},
	}, ManagementCommandMetadata{ActorReference: "secret-instance-token"})
	if err != nil || result.Status != "unknown" || result.UnknownCount != 1 || valueOrEmpty(result.Outcomes[0].Code) != "unknown_outcome" || repository.completedAs != group_model.ManagementCommandUnknown || provider.participantCalls != 1 {
		t.Fatalf("result=%#v error=%v repository=%#v provider=%#v", result, err, repository, provider)
	}
}

func TestCreateGroupCommandReturnsTypedPartialParticipantOutcomes(t *testing.T) {
	repository := &managementCommandRepositoryStub{created: true}
	first, _ := types.ParseJID("15550000001@s.whatsapp.net")
	second, _ := types.ParseJID("15550000002@s.whatsapp.net")
	groupJID, _ := types.ParseJID("123@g.us")
	provider := &managementCommandProviderStub{createGroup: &types.GroupInfo{JID: groupJID, GroupName: types.GroupName{Name: "Branches"}, Participants: []types.GroupParticipant{{JID: first}, {JID: second, Error: 403}}}}
	manager := NewManagementCommandManager(repository, managementCommandReader(projection_model.ParticipantRoleSuperAdmin, true), provider)
	result, err := manager.CreateGroup(context.Background(), &instance_model.Instance{Id: uuid.NewString(), Jid: "actor@s.whatsapp.net"}, &CreateGroupStruct{GroupName: "Branches", Participants: []string{first.String(), second.String()}}, ManagementCommandMetadata{ActorReference: "secret-instance-token"})
	if err != nil || result.GroupJID != groupJID.String() || result.AcknowledgementStatus != "partially_completed" || result.SucceededCount != 1 || result.FailedCount != 1 || repository.completedAs != group_model.ManagementCommandPartiallyCompleted {
		t.Fatalf("result=%#v error=%v repository=%#v", result, err, repository)
	}
	if strings.Contains(string(repository.command.SafeOutcome), first.String()) || !strings.HasPrefix(result.ParticipantOutcomes[0].Participant, "participant_") {
		t.Fatalf("safe create outcome exposed provider participant identity: %s", repository.command.SafeOutcome)
	}
}

func TestJoinGroupCommandKeepsUnconfirmedMembershipUnknown(t *testing.T) {
	repository := &managementCommandRepositoryStub{created: true}
	provider := &managementCommandProviderStub{joinResult: managementJoinProviderResult{GroupJID: "123@g.us", Status: "unknown", Reason: "membership_not_confirmed"}}
	manager := NewManagementCommandManager(repository, managementCommandReader(projection_model.ParticipantRoleSuperAdmin, true), provider)
	result, err := manager.JoinGroup(context.Background(), &instance_model.Instance{Id: uuid.NewString(), Jid: "actor@s.whatsapp.net"}, &JoinGroupStruct{Code: "https://chat.whatsapp.com/safe-test-code"}, ManagementCommandMetadata{ActorReference: "secret-instance-token"})
	if err != nil || result.Status != "unknown" || result.GroupJID == nil || *result.GroupJID != "123@g.us" || result.Reason == nil || *result.Reason != "membership_not_confirmed" || repository.completedAs != group_model.ManagementCommandUnknown {
		t.Fatalf("result=%#v error=%v repository=%#v", result, err, repository)
	}
}

func TestJoinGroupCommandMapsTypedProviderOutcomes(t *testing.T) {
	tests := []struct {
		name           string
		providerStatus string
		commandStatus  group_model.ManagementCommandStatus
	}{
		{name: "already member", providerStatus: "already_member", commandStatus: group_model.ManagementCommandCompleted},
		{name: "approval required", providerStatus: "approval_required", commandStatus: group_model.ManagementCommandPartiallyCompleted},
		{name: "rejected", providerStatus: "rejected", commandStatus: group_model.ManagementCommandFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &managementCommandRepositoryStub{created: true}
			provider := &managementCommandProviderStub{joinResult: managementJoinProviderResult{GroupJID: "123@g.us", Status: test.providerStatus, Reason: "public_safe_reason"}}
			manager := NewManagementCommandManager(repository, managementCommandReader(projection_model.ParticipantRoleSuperAdmin, true), provider)
			result, err := manager.JoinGroup(context.Background(), &instance_model.Instance{Id: uuid.NewString(), Jid: "actor@s.whatsapp.net"}, &JoinGroupStruct{Code: "https://chat.whatsapp.com/safe-test-code"}, ManagementCommandMetadata{ActorReference: "secret-instance-token"})
			if err != nil || result.Status != test.providerStatus || repository.completedAs != test.commandStatus {
				t.Fatalf("result=%#v error=%v commandStatus=%s", result, err, repository.completedAs)
			}
		})
	}
}

type managementPhotoAssetsStub struct {
	prepared     *PreparedGroupPhoto
	prepareErr   error
	prepareCalls int
	commitCalls  int
	commitErr    error
}

func (s *managementPhotoAssetsStub) Prepare(context.Context, string, string, string, string) (*PreparedGroupPhoto, error) {
	s.prepareCalls++
	return s.prepared, s.prepareErr
}

func (s *managementPhotoAssetsStub) Commit(context.Context, string, string, string, string, string) error {
	s.commitCalls++
	return s.commitErr
}

func TestGroupPhotoCommandUsesPreparedAssetOnceAndCommitsProjection(t *testing.T) {
	repository := &managementCommandRepositoryStub{created: true}
	provider := &managementCommandProviderStub{photoID: "provider-picture-id"}
	assets := &managementPhotoAssetsStub{prepared: &PreparedGroupPhoto{MediaAssetID: uuid.NewString(), Bytes: []byte("normalized-jpeg")}}
	manager := NewManagementCommandManager(repository, managementCommandReader(projection_model.ParticipantRoleAdmin, false), provider, assets)
	result, err := manager.SetPhoto(context.Background(), &instance_model.Instance{Id: uuid.NewString(), Jid: "actor@s.whatsapp.net"}, &SetGroupPhotoAssetRequest{GroupJID: "123@g.us", MediaAssetID: assets.prepared.MediaAssetID}, ManagementCommandMetadata{ActorReference: "secret-instance-token"})
	if err != nil || result.Status != "completed" || provider.photoCalls != 1 || assets.prepareCalls != 1 || assets.commitCalls != 1 || repository.completedAs != group_model.ManagementCommandCompleted {
		t.Fatalf("result=%#v error=%v provider=%#v assets=%#v repository=%#v", result, err, provider, assets, repository)
	}
}

func TestGroupPhotoCommandRejectsInvalidAssetBeforeProviderAdmission(t *testing.T) {
	repository := &managementCommandRepositoryStub{created: true}
	provider := &managementCommandProviderStub{}
	assets := &managementPhotoAssetsStub{prepareErr: ErrGroupPhotoAssetInvalidType}
	manager := NewManagementCommandManager(repository, managementCommandReader(projection_model.ParticipantRoleAdmin, false), provider, assets)
	_, err := manager.SetPhoto(context.Background(), &instance_model.Instance{Id: uuid.NewString(), Jid: "actor@s.whatsapp.net"}, &SetGroupPhotoAssetRequest{GroupJID: "123@g.us", MediaAssetID: uuid.NewString()}, ManagementCommandMetadata{ActorReference: "secret-instance-token"})
	if !errors.Is(err, ErrGroupPhotoAssetInvalidType) || provider.prepareCalls != 0 || provider.photoCalls != 0 || repository.executeCalls != 0 || repository.completedAs != group_model.ManagementCommandFailed {
		t.Fatalf("error=%v provider=%#v repository=%#v", err, provider, repository)
	}
}

func TestGroupPhotoCommandNeverRetriesUnknownProviderOutcome(t *testing.T) {
	repository := &managementCommandRepositoryStub{created: true}
	provider := &managementCommandProviderStub{photoErr: errors.New("transport closed after admission")}
	assets := &managementPhotoAssetsStub{prepared: &PreparedGroupPhoto{MediaAssetID: uuid.NewString(), Bytes: []byte("normalized-jpeg")}}
	manager := NewManagementCommandManager(repository, managementCommandReader(projection_model.ParticipantRoleAdmin, false), provider, assets)
	result, err := manager.SetPhoto(context.Background(), &instance_model.Instance{Id: uuid.NewString(), Jid: "actor@s.whatsapp.net"}, &SetGroupPhotoAssetRequest{GroupJID: "123@g.us", MediaAssetID: assets.prepared.MediaAssetID}, ManagementCommandMetadata{ActorReference: "secret-instance-token"})
	if err != nil || result.Status != "unknown" || provider.photoCalls != 1 || assets.commitCalls != 0 || repository.completedAs != group_model.ManagementCommandUnknown {
		t.Fatalf("result=%#v error=%v provider=%#v assets=%#v repository=%#v", result, err, provider, assets, repository)
	}
}
