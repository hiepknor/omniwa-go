package group_service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	group_repository "github.com/evolution-foundation/evolution-go/pkg/group/repository"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	instance_runtime "github.com/evolution-foundation/evolution-go/pkg/instance/runtime"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	"github.com/evolution-foundation/evolution-go/pkg/netguard"
	"github.com/evolution-foundation/evolution-go/pkg/outbound"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/evolution-foundation/evolution-go/pkg/utils"
	"github.com/evolution-foundation/evolution-go/pkg/waquery"
	whatsmeow_service "github.com/evolution-foundation/evolution-go/pkg/whatsmeow/service"
	"github.com/gin-gonic/gin"
	"github.com/vincent-petithory/dataurl"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

var ErrGroupInviteLinkNotFound = errors.New("cached group invite link is not available")

type GroupService interface {
	ListGroups(ctx context.Context, instance *instance_model.Instance) ([]*types.GroupInfo, error)
	ListGroupsRead(ctx context.Context, instance *instance_model.Instance) ([]*types.GroupInfo, *projection_service.ProjectionReadMeta, error)
	SearchGroupsRead(ctx context.Context, instance *instance_model.Instance, term string, limit int, cursor string) ([]*types.GroupInfo, *projection_service.ProjectionReadMeta, error)
	SearchManagementGroups(ctx context.Context, instance *instance_model.Instance, filters GroupManagementFilters, limit int, cursor string) ([]GroupSummary, *projection_service.ProjectionReadMeta, error)
	GetManagementGroupSummary(ctx context.Context, instance *instance_model.Instance) (*GroupDirectorySummary, *projection_service.ProjectionReadMeta, error)
	GetGroupInfo(ctx context.Context, data *GetGroupInfoStruct, instance *instance_model.Instance) (*types.GroupInfo, error)
	GetGroupInfoRead(ctx context.Context, data *GetGroupInfoStruct, instance *instance_model.Instance) (*types.GroupInfo, *projection_service.ProjectionReadMeta, error)
	GetManagementGroupInfo(ctx context.Context, data *GetGroupInfoStruct, instance *instance_model.Instance) (*GroupDetail, *projection_service.ProjectionReadMeta, error)
	ListManagementGroupMembers(ctx context.Context, instance *instance_model.Instance, groupJID string, filters GroupMemberFilters, limit int, cursor string) ([]GroupMember, *projection_service.ProjectionReadMeta, error)
	ListManagementAudit(ctx context.Context, instanceID, groupJID string, limit int, cursor string) (*ManagementAuditResult, error)
	ExecuteSetGroupName(context.Context, *SetGroupNameStruct, *instance_model.Instance, ManagementCommandMetadata) (*CommandAcknowledgement, error)
	ExecuteSetGroupDescription(context.Context, *SetGroupDescriptionStruct, *instance_model.Instance, ManagementCommandMetadata) (*CommandAcknowledgement, error)
	ExecuteUpdateGroupSettings(context.Context, *UpdateGroupSettingsStruct, *instance_model.Instance, ManagementCommandMetadata) (*CommandAcknowledgement, error)
	ExecuteLeaveGroup(context.Context, *ManagementLeaveGroupRequest, *instance_model.Instance, ManagementCommandMetadata) (*CommandAcknowledgement, error)
	ExecuteResetInviteLink(context.Context, *GetGroupInviteLinkStruct, *instance_model.Instance, ManagementCommandMetadata) (*CommandAcknowledgement, error)
	ExecuteUpdateParticipant(context.Context, *ManagementParticipantRequest, *instance_model.Instance, ManagementCommandMetadata) (*ParticipantCommandResult, error)
	ExecuteCreateGroup(context.Context, *CreateGroupStruct, *instance_model.Instance, ManagementCommandMetadata) (*CreateGroupCommandResult, error)
	ExecuteJoinGroup(context.Context, *JoinGroupStruct, *instance_model.Instance, ManagementCommandMetadata) (*JoinGroupCommandResult, error)
	ExecuteSetGroupPhoto(context.Context, *SetGroupPhotoAssetRequest, *instance_model.Instance, ManagementCommandMetadata) (*CommandAcknowledgement, error)
	GetGroupInviteLink(ctx context.Context, data *GetGroupInviteLinkStruct, instance *instance_model.Instance) (string, *projection_service.ProjectionReadMeta, error)
	SetGroupPhoto(data *SetGroupPhotoStruct, instance *instance_model.Instance) (string, error)
	SetGroupName(data *SetGroupNameStruct, instance *instance_model.Instance) error
	SetGroupDescription(data *SetGroupDescriptionStruct, instance *instance_model.Instance) error
	CreateGroup(ctx context.Context, data *CreateGroupStruct, instance *instance_model.Instance) (gin.H, error)
	UpdateParticipant(data *AddParticipantStruct, instance *instance_model.Instance) error
	UpdateGroupSettings(data *UpdateGroupSettingsStruct, instance *instance_model.Instance) error
	GetGroupRequestParticipants(ctx context.Context, data *GetGroupRequestParticipantsStruct, instance *instance_model.Instance) ([]EnrichedGroupParticipantRequest, error)
	UpdateGroupRequestParticipants(data *UpdateGroupRequestParticipantsStruct, instance *instance_model.Instance) ([]types.GroupParticipant, error)
	GetMyGroups(ctx context.Context, instance *instance_model.Instance) ([]types.GroupInfo, error)
	JoinGroupLink(data *JoinGroupStruct, instance *instance_model.Instance) error
	LeaveGroup(data *LeaveGroupStruct, instance *instance_model.Instance) error
}

type groupService struct {
	clients          instance_runtime.CommandClientProvider
	whatsmeowService whatsmeow_service.WhatsmeowService
	loggerWrapper    *logger_wrapper.LoggerManager
	queryGuard       waquery.Guard
	outboundGuard    outbound.Guard
	groupReader      *projection_service.GroupReader
	managementReader *ManagementReader
	auditReader      *ManagementAuditReader
	commandManager   *ManagementCommandManager
	groupWriter      *projection_service.GroupWriter
	mediaFetcher     netguard.Fetcher
}

const groupProjectionWriteTimeout = 2 * time.Second
const groupPostMutationQueryTimeout = 15 * time.Second

type SimpleGroupInfo struct {
	JID       types.JID `json:"jid"`
	GroupName string    `json:"groupName"`
}

type GroupCollection struct {
	Groups []SimpleGroupInfo
}

type GetGroupInfoStruct struct {
	GroupJID string `json:"groupJid"`
}

type GetGroupInviteLinkStruct struct {
	GroupJID string `json:"groupJid"`
	Reset    bool   `json:"reset"`
}

type SetGroupPhotoStruct struct {
	GroupJID string `json:"groupJid"`
	Image    string `json:"image"`
}

type SetGroupPhotoAssetRequest struct {
	GroupJID     string `json:"groupJid"`
	MediaAssetID string `json:"mediaAssetId" format:"uuid"`
}

type SetGroupNameStruct struct {
	GroupJID string `json:"groupJid"`
	Name     string `json:"name"`
}

type SetGroupDescriptionStruct struct {
	GroupJID    string `json:"groupJid"`
	Description string `json:"description"`
}

type CreateGroupStruct struct {
	GroupName    string   `json:"groupName"`
	Participants []string `json:"participants"`
}

type AddParticipantStruct struct {
	GroupJID     types.JID                   `json:"groupJid"`
	Participants []string                    `json:"participants"`
	Action       whatsmeow.ParticipantChange `json:"action"`
}

type JoinGroupStruct struct {
	Code string `json:"code"`
}

type LeaveGroupStruct struct {
	GroupJID types.JID `json:"groupJid"`
}

// ManagementLeaveGroupRequest is the stable public request used by the
// normalized management contract. The legacy provider-typed DTO remains only
// for the disabled-feature compatibility path.
type ManagementLeaveGroupRequest struct {
	GroupJID string `json:"groupJid"`
}

type UpdateGroupSettingsStruct struct {
	GroupJID string `json:"groupJid"`
	Action   string `json:"action"` // announcement, not_announcement, locked, unlocked
}

type GetGroupRequestParticipantsStruct struct {
	GroupJID string `json:"groupJid"`
}

// Estrutura enriquecida com PushName
type EnrichedGroupParticipantRequest struct {
	JID         types.JID `json:"JID"`
	RequestedAt time.Time `json:"RequestedAt"`
	PushName    string    `json:"PushName"`
}

type UpdateGroupRequestParticipantsStruct struct {
	GroupJID     string   `json:"groupJid"`
	Action       string   `json:"action"` // approve, reject
	Participants []string `json:"participants"`
}

func (g *groupService) ensureClientConnected(instanceId string) (*whatsmeow.Client, error) {
	client := g.clients.Get(instanceId)
	g.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] Checking client connection status - Client exists: %v", instanceId, client != nil)

	if client == nil {
		g.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] No client found, attempting to start new instance", instanceId)
		err := g.whatsmeowService.StartInstance(instanceId)
		if err != nil {
			g.loggerWrapper.GetLogger(instanceId).LogError("[%s] Failed to start instance: %v", instanceId, err)
			return nil, errors.New("no active session found")
		}

		g.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] Instance started, waiting 2 seconds...", instanceId)
		time.Sleep(2 * time.Second)

		client = g.clients.Get(instanceId)
		g.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] Checking new client - Exists: %v, Connected: %v",
			instanceId,
			client != nil,
			client != nil && client.IsConnected())

		if client == nil || !client.IsConnected() {
			g.loggerWrapper.GetLogger(instanceId).LogError("[%s] New client validation failed - Exists: %v, Connected: %v",
				instanceId,
				client != nil,
				client != nil && client.IsConnected())
			return nil, errors.New("no active session found")
		}
	} else if !client.IsConnected() {
		g.loggerWrapper.GetLogger(instanceId).LogError("[%s] Existing client is disconnected - Connected status: %v",
			instanceId,
			client.IsConnected())
		return nil, errors.New("client disconnected")
	}

	g.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] Client successfully validated - Connected: %v", instanceId, client.IsConnected())
	return client, nil
}

func (g *groupService) ListGroups(ctx context.Context, instance *instance_model.Instance) ([]*types.GroupInfo, error) {
	groups, _, err := g.ListGroupsRead(ctx, instance)
	return groups, err
}

func (g *groupService) ListGroupsRead(ctx context.Context, instance *instance_model.Instance) ([]*types.GroupInfo, *projection_service.ProjectionReadMeta, error) {
	if g.groupReader == nil {
		return nil, nil, errors.New("group projection reader is required")
	}
	return g.groupReader.List(ctx, instance.Id)
}

func (g *groupService) SearchGroupsRead(ctx context.Context, instance *instance_model.Instance, term string, limit int, cursor string) ([]*types.GroupInfo, *projection_service.ProjectionReadMeta, error) {
	if g.groupReader == nil || instance == nil {
		return nil, nil, errors.New("group projection reader and instance are required")
	}
	return g.groupReader.Search(ctx, instance.Id, term, limit, cursor)
}

func (g *groupService) SearchManagementGroups(ctx context.Context, instance *instance_model.Instance, filters GroupManagementFilters, limit int, cursor string) ([]GroupSummary, *projection_service.ProjectionReadMeta, error) {
	if g.managementReader == nil {
		return nil, nil, errors.New("group management reader is required")
	}
	return g.managementReader.Search(ctx, instance, filters, limit, cursor)
}

func (g *groupService) GetManagementGroupSummary(ctx context.Context, instance *instance_model.Instance) (*GroupDirectorySummary, *projection_service.ProjectionReadMeta, error) {
	if g.managementReader == nil {
		return nil, nil, errors.New("group management reader is required")
	}
	return g.managementReader.Summary(ctx, instance)
}

func (g *groupService) GetGroupInfo(ctx context.Context, data *GetGroupInfoStruct, instance *instance_model.Instance) (*types.GroupInfo, error) {
	info, _, err := g.GetGroupInfoRead(ctx, data, instance)
	return info, err
}

func (g *groupService) GetGroupInfoRead(ctx context.Context, data *GetGroupInfoStruct, instance *instance_model.Instance) (*types.GroupInfo, *projection_service.ProjectionReadMeta, error) {
	recipient, ok := utils.ParseJID(data.GroupJID)
	if !ok {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] Error validating message fields", instance.Id)
		return nil, nil, errors.New("invalid group jid")
	}
	if g.groupReader == nil {
		return nil, nil, errors.New("group projection reader is required")
	}
	return g.groupReader.Get(ctx, instance.Id, recipient.String())
}

func (g *groupService) GetManagementGroupInfo(ctx context.Context, data *GetGroupInfoStruct, instance *instance_model.Instance) (*GroupDetail, *projection_service.ProjectionReadMeta, error) {
	if data == nil || g.managementReader == nil {
		return nil, nil, errors.New("group management reader and request are required")
	}
	return g.managementReader.Get(ctx, instance, data.GroupJID)
}

func (g *groupService) ListManagementGroupMembers(ctx context.Context, instance *instance_model.Instance, groupJID string, filters GroupMemberFilters, limit int, cursor string) ([]GroupMember, *projection_service.ProjectionReadMeta, error) {
	if g.managementReader == nil {
		return nil, nil, errors.New("group management reader is required")
	}
	return g.managementReader.Members(ctx, instance, groupJID, filters, limit, cursor)
}

func (g *groupService) ListManagementAudit(ctx context.Context, instanceID, groupJID string, limit int, cursor string) (*ManagementAuditResult, error) {
	if g.auditReader == nil {
		return nil, errors.New("group management audit reader is required")
	}
	return g.auditReader.List(ctx, instanceID, groupJID, limit, cursor)
}

func (g *groupService) ExecuteSetGroupName(ctx context.Context, data *SetGroupNameStruct, instance *instance_model.Instance, metadata ManagementCommandMetadata) (*CommandAcknowledgement, error) {
	return g.commandManager.SetName(ctx, instance, data, metadata)
}

func (g *groupService) ExecuteSetGroupDescription(ctx context.Context, data *SetGroupDescriptionStruct, instance *instance_model.Instance, metadata ManagementCommandMetadata) (*CommandAcknowledgement, error) {
	return g.commandManager.SetDescription(ctx, instance, data, metadata)
}

func (g *groupService) ExecuteUpdateGroupSettings(ctx context.Context, data *UpdateGroupSettingsStruct, instance *instance_model.Instance, metadata ManagementCommandMetadata) (*CommandAcknowledgement, error) {
	return g.commandManager.UpdateSetting(ctx, instance, data, metadata)
}

func (g *groupService) ExecuteLeaveGroup(ctx context.Context, data *ManagementLeaveGroupRequest, instance *instance_model.Instance, metadata ManagementCommandMetadata) (*CommandAcknowledgement, error) {
	return g.commandManager.Leave(ctx, instance, data, metadata)
}

func (g *groupService) ExecuteResetInviteLink(ctx context.Context, data *GetGroupInviteLinkStruct, instance *instance_model.Instance, metadata ManagementCommandMetadata) (*CommandAcknowledgement, error) {
	return g.commandManager.ResetInviteLink(ctx, instance, data, metadata)
}

func (g *groupService) ExecuteUpdateParticipant(ctx context.Context, data *ManagementParticipantRequest, instance *instance_model.Instance, metadata ManagementCommandMetadata) (*ParticipantCommandResult, error) {
	return g.commandManager.UpdateParticipants(ctx, instance, data, metadata)
}

func (g *groupService) ExecuteCreateGroup(ctx context.Context, data *CreateGroupStruct, instance *instance_model.Instance, metadata ManagementCommandMetadata) (*CreateGroupCommandResult, error) {
	return g.commandManager.CreateGroup(ctx, instance, data, metadata)
}

func (g *groupService) ExecuteJoinGroup(ctx context.Context, data *JoinGroupStruct, instance *instance_model.Instance, metadata ManagementCommandMetadata) (*JoinGroupCommandResult, error) {
	return g.commandManager.JoinGroup(ctx, instance, data, metadata)
}

func (g *groupService) ExecuteSetGroupPhoto(ctx context.Context, data *SetGroupPhotoAssetRequest, instance *instance_model.Instance, metadata ManagementCommandMetadata) (*CommandAcknowledgement, error) {
	return g.commandManager.SetPhoto(ctx, instance, data, metadata)
}

func (g *groupService) GetGroupInviteLink(ctx context.Context, data *GetGroupInviteLinkStruct, instance *instance_model.Instance) (string, *projection_service.ProjectionReadMeta, error) {
	if g == nil || data == nil || instance == nil || instance.Id == "" {
		return "", nil, ErrInvalidManagementFilter
	}
	recipient, ok := utils.ParseJID(data.GroupJID)
	if !ok || recipient.Server != types.GroupServer {
		return "", nil, ErrInvalidManagementFilter
	}
	if !data.Reset {
		if g.managementReader == nil {
			return "", nil, errors.New("group management projection reader is required")
		}
		inviteLink, found, decision, meta, err := g.managementReader.InviteLink(ctx, instance, recipient.String())
		if err != nil {
			return "", meta, err
		}
		switch decision.State {
		case "allowed":
		case "denied":
			return "", meta, ErrManagementPermissionDenied
		default:
			return "", meta, ErrManagementPermissionUnknown
		}
		if !found {
			return "", meta, ErrGroupInviteLinkNotFound
		}
		return inviteLink, meta, nil
	}
	client, err := g.ensureClientConnected(instance.Id)
	if err != nil {
		return "", nil, err
	}

	var resp string
	if data.Reset {
		// Reset is a mutation, so it must never be single-flighted or consume the
		// information-query budget.
		resp, err = instance_runtime.DoProviderCommandValue(ctx, g.clients, func(commandCtx context.Context) (string, error) {
			return client.GetGroupInviteLink(commandCtx, recipient, true)
		})
		err = g.queryGuard.ObserveError(instance.Id, err)
	}
	if err != nil {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error mute chat: %v", instance.Id, err)
		return "", nil, err
	}
	g.writeGroupProjection(instance.Id, func(writeCtx context.Context) error {
		return g.groupWriter.WriteInviteLink(writeCtx, instance.Id, recipient.String(), resp)
	})

	return resp, nil, nil
}

func (g *groupService) SetGroupPhoto(data *SetGroupPhotoStruct, instance *instance_model.Instance) (string, error) {
	client, err := g.ensureClientConnected(instance.Id)
	if err != nil {
		return "", err
	}

	recipient, ok := utils.ParseJID(data.GroupJID)
	if !ok {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] Error validating message fields", instance.Id)
		return "", errors.New("invalid group jid")
	}

	var fileData []byte

	if strings.HasPrefix(data.Image, "http://") || strings.HasPrefix(data.Image, "https://") {
		fileData, err = g.mediaFetcher.Fetch(context.Background(), data.Image)
		if err != nil {
			g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] Could not download image from URL", instance.Id)
			return "", fmt.Errorf("failed to fetch image from URL: %v", err)
		}

	} else if strings.HasPrefix(data.Image, "data:image/jpeg;base64,") || strings.HasPrefix(data.Image, "data:image/png;base64,") {
		dataURL, err := dataurl.DecodeString(data.Image)
		if err != nil {
			g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] Could not decode base64 encoded data from payload", instance.Id)
			return "", err
		}
		fileData = dataURL.Data
	} else {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] Image data should start with \"data:image/jpeg;base64,\" or be a valid URL", instance.Id)
		return "", errors.New("image data should be a valid URL or start with \"data:image/jpeg;base64,\"")
	}

	pictureID, err := instance_runtime.DoProviderCommandValue(context.Background(), g.clients, func(commandCtx context.Context) (string, error) {
		return client.SetGroupPhoto(commandCtx, recipient, fileData)
	})
	if err != nil {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] Error setting group photo: %v", instance.Id, err)
		return "", err
	}

	return pictureID, nil
}

func (g *groupService) SetGroupName(data *SetGroupNameStruct, instance *instance_model.Instance) error {
	client, err := g.ensureClientConnected(instance.Id)
	if err != nil {
		return err
	}

	recipient, ok := utils.ParseJID(data.GroupJID)
	if !ok {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] Error validating message fields", instance.Id)
		return errors.New("invalid group jid")
	}

	g.loggerWrapper.GetLogger(instance.Id).LogInfo("[%s] Attempting to set group name for %s", instance.Id, recipient.String())

	err = instance_runtime.DoProviderCommand(context.Background(), g.clients, func(commandCtx context.Context) error {
		return client.SetGroupName(commandCtx, recipient, data.Name)
	})
	if err != nil {
		// Log mais detalhado para erro 409
		if strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "conflict") {
			g.loggerWrapper.GetLogger(instance.Id).LogWarn("[%s] WhatsApp returned 409 conflict when setting name. This usually means: rate limit, duplicate content, or insufficient permissions", instance.Id)
		}
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error setting group name: %v", instance.Id, err)
		return err
	}
	g.writeGroupProjection(instance.Id, func(writeCtx context.Context) error {
		return g.groupWriter.WriteName(writeCtx, instance.Id, recipient.String(), data.Name)
	})

	g.loggerWrapper.GetLogger(instance.Id).LogInfo("[%s] Group name set successfully", instance.Id)
	return nil
}

func (g *groupService) SetGroupDescription(data *SetGroupDescriptionStruct, instance *instance_model.Instance) error {
	client, err := g.ensureClientConnected(instance.Id)
	if err != nil {
		return err
	}

	recipient, ok := utils.ParseJID(data.GroupJID)
	if !ok {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] Error validating message fields", instance.Id)
		return errors.New("invalid group jid")
	}

	g.loggerWrapper.GetLogger(instance.Id).LogInfo("[%s] Attempting to set group description for %s", instance.Id, recipient.String())

	// Use SetGroupTopic instead of SetGroupDescription (proper WhatsApp method)
	// Empty strings for previousID and newID will be auto-filled by the library
	err = instance_runtime.DoProviderCommand(context.Background(), g.clients, func(commandCtx context.Context) error {
		return client.SetGroupTopic(commandCtx, recipient, "", "", data.Description)
	})
	if err != nil {
		// Log mais detalhado para erro 409
		if strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "conflict") {
			g.loggerWrapper.GetLogger(instance.Id).LogWarn("[%s] WhatsApp returned 409 conflict when setting description. This usually means: rate limit, duplicate content, or insufficient permissions", instance.Id)
		}
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error setting group description: %v", instance.Id, err)
		return err
	}
	g.writeGroupProjection(instance.Id, func(writeCtx context.Context) error {
		return g.groupWriter.WriteTopic(writeCtx, instance.Id, recipient.String(), data.Description)
	})

	g.loggerWrapper.GetLogger(instance.Id).LogInfo("[%s] Group description set successfully", instance.Id)
	return nil
}

func (g *groupService) CreateGroup(ctx context.Context, data *CreateGroupStruct, instance *instance_model.Instance) (gin.H, error) {
	client, err := g.ensureClientConnected(instance.Id)
	if err != nil {
		return nil, err
	}

	var participants []types.JID
	for _, participant := range data.Participants {
		recipient, ok := utils.ParseJID(participant)
		participants = append(participants, recipient)
		if !ok {
			g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] Error validating message fields", instance.Id)
			return nil, errors.New("invalid phone number")
		}
	}

	resp, err := instance_runtime.DoProviderCommandValue(ctx, g.clients, func(commandCtx context.Context) (*types.GroupInfo, error) {
		return client.CreateGroup(commandCtx, whatsmeow.ReqCreateGroup{
			Name:         data.GroupName,
			Participants: participants,
		})
	})
	if err != nil {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error create group: %v", instance.Id, err)
		return nil, g.queryGuard.ObserveError(instance.Id, err)
	}

	var failed []types.JID
	var added []types.JID
	for _, participant := range resp.Participants {
		if participant.Error != 0 {
			failed = append(failed, participant.JID)
		} else {
			added = append(added, participant.JID)
		}
	}

	infoResp, err := waquery.Do(ctx, g.queryGuard, instance.Id, waquery.OperationGroupInfo, resp.JID.String(), func(queryCtx context.Context) (*types.GroupInfo, error) {
		return client.GetGroupInfo(queryCtx, resp.JID)
	})
	if err != nil {
		// The group already exists. A best-effort enrichment failure must not
		// report the successful mutation as failed or invite unsafe retries.
		g.loggerWrapper.GetLogger(instance.Id).LogWarn("[%s] group created but post-action info query failed: %v", instance.Id, err)
	} else {
		added = added[:0]
		for _, participant := range infoResp.Participants {
			added = append(added, participant.JID)
		}
	}
	confirmedInfo := resp
	if infoResp != nil {
		confirmedInfo = infoResp
	}
	g.writeGroupProjection(instance.Id, func(writeCtx context.Context) error {
		return g.groupWriter.WriteInfo(writeCtx, instance.Id, confirmedInfo)
	})

	response := gin.H{
		"jid":    resp.JID,
		"name":   resp.Name,
		"owner":  resp.OwnerJID,
		"added":  added,
		"failed": failed,
	}

	return response, nil
}

func (g *groupService) UpdateParticipant(data *AddParticipantStruct, instance *instance_model.Instance) error {
	client, err := g.ensureClientConnected(instance.Id)
	if err != nil {
		return err
	}

	var participants []types.JID
	for _, participant := range data.Participants {
		recipient, ok := utils.ParseJID(participant)
		participants = append(participants, recipient)
		if !ok {
			g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] Error validating message fields", instance.Id)
			return errors.New("invalid phone number")
		}
	}

	results, err := instance_runtime.DoProviderCommandValue(context.Background(), g.clients, func(commandCtx context.Context) ([]types.GroupParticipant, error) {
		return client.UpdateGroupParticipants(commandCtx, data.GroupJID, participants, data.Action)
	})
	if err != nil {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error create group: %v", instance.Id, err)
		return err
	}
	g.writeGroupProjection(instance.Id, func(writeCtx context.Context) error {
		return g.groupWriter.WriteParticipants(writeCtx, instance.Id, data.GroupJID.String(), string(data.Action), results)
	})

	return nil
}

func (g *groupService) GetMyGroups(ctx context.Context, instance *instance_model.Instance) ([]types.GroupInfo, error) {
	client, err := g.ensureClientConnected(instance.Id)
	if err != nil {
		return nil, err
	}

	resp, err := waquery.Do(ctx, g.queryGuard, instance.Id, waquery.OperationGroupsList, "", client.GetJoinedGroups)
	if err != nil {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error create group: %v", instance.Id, err)
		return nil, err
	}

	var jid string = client.Store.ID.String()
	var jidClear = strings.Split(jid, ".")[0]
	jidOfAdmin, ok := utils.ParseJID(jidClear)
	if !ok {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] Error validating message fields", instance.Id)
		return nil, errors.New("invalid phone number")
	}
	var adminGroups []types.GroupInfo
	for _, group := range resp {
		if group.OwnerJID == jidOfAdmin {
			adminGroups = append(adminGroups, *group)
			_ = adminGroups
		}
	}

	return adminGroups, nil
}

func (g *groupService) JoinGroupLink(data *JoinGroupStruct, instance *instance_model.Instance) error {
	client, err := g.ensureClientConnected(instance.Id)
	if err != nil {
		return err
	}

	joinedGroup, err := instance_runtime.DoProviderCommandValue(context.Background(), g.clients, func(commandCtx context.Context) (types.JID, error) {
		return client.JoinGroupWithLink(commandCtx, data.Code)
	})
	if err != nil {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error create group: %v", instance.Id, err)
		return err
	}
	queryCtx, cancel := context.WithTimeout(context.Background(), groupPostMutationQueryTimeout)
	defer cancel()
	info, queryErr := waquery.Do(queryCtx, g.queryGuard, instance.Id, waquery.OperationGroupInfo, joinedGroup.String(), func(ctx context.Context) (*types.GroupInfo, error) {
		return client.GetGroupInfo(ctx, joinedGroup)
	})
	if queryErr != nil {
		g.loggerWrapper.GetLogger(instance.Id).LogWarn("component=projection action=write_through instance_id=%s resource=groups operation=join result=deferred error_code=post_mutation_query_failed", instance.Id)
	} else {
		g.writeGroupProjection(instance.Id, func(writeCtx context.Context) error {
			return g.groupWriter.WriteInfo(writeCtx, instance.Id, info)
		})
	}

	return nil
}

func (g *groupService) LeaveGroup(data *LeaveGroupStruct, instance *instance_model.Instance) error {
	client, err := g.ensureClientConnected(instance.Id)
	if err != nil {
		return err
	}

	err = instance_runtime.DoProviderCommand(context.Background(), g.clients, func(commandCtx context.Context) error {
		return client.LeaveGroup(commandCtx, data.GroupJID)
	})
	if err != nil {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error leave group: %v", instance.Id, err)
		return err
	}
	g.writeGroupProjection(instance.Id, func(writeCtx context.Context) error {
		return g.groupWriter.Tombstone(writeCtx, instance.Id, data.GroupJID.String())
	})

	return nil
}

func (g *groupService) UpdateGroupSettings(data *UpdateGroupSettingsStruct, instance *instance_model.Instance) error {
	client, err := g.ensureClientConnected(instance.Id)
	if err != nil {
		return err
	}
	recipient, ok := utils.ParseJID(data.GroupJID)
	if !ok {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] Error validating group jid", instance.Id)
		return errors.New("invalid group jid")
	}

	// Validate action
	validActions := map[string]bool{
		"announcement":     true,
		"not_announcement": true,
		"locked":           true,
		"unlocked":         true,
		"approval_on":      true,
		"approval_off":     true,
		"admin_add":        true,
		"all_member_add":   true,
	}

	if !validActions[data.Action] {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] Invalid action: %s", instance.Id, data.Action)
		return errors.New("invalid action. Valid actions: announcement, not_announcement, locked, unlocked, approval_on, approval_off, admin_add, all_member_add")
	}

	// Apply settings based on action through one fenced command admission.
	err = instance_runtime.DoProviderCommand(context.Background(), g.clients, func(commandCtx context.Context) error {
		switch data.Action {
		case "announcement":
			return client.SetGroupAnnounce(commandCtx, recipient, true)
		case "not_announcement":
			return client.SetGroupAnnounce(commandCtx, recipient, false)
		case "locked":
			return client.SetGroupLocked(commandCtx, recipient, true)
		case "unlocked":
			return client.SetGroupLocked(commandCtx, recipient, false)
		case "approval_on":
			return client.SetGroupJoinApprovalMode(commandCtx, recipient, true)
		case "approval_off":
			return client.SetGroupJoinApprovalMode(commandCtx, recipient, false)
		case "admin_add":
			return client.SetGroupMemberAddMode(commandCtx, recipient, "admin_add")
		case "all_member_add":
			return client.SetGroupMemberAddMode(commandCtx, recipient, "all_member_add")
		default:
			return ErrInvalidManagementFilter
		}
	})

	if err != nil {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error updating group settings: %v", instance.Id, err)
		return err
	}
	g.writeGroupProjection(instance.Id, func(writeCtx context.Context) error {
		switch data.Action {
		case "announcement":
			return g.groupWriter.WriteSetting(writeCtx, instance.Id, recipient.String(), "announce", true)
		case "not_announcement":
			return g.groupWriter.WriteSetting(writeCtx, instance.Id, recipient.String(), "announce", false)
		case "locked":
			return g.groupWriter.WriteSetting(writeCtx, instance.Id, recipient.String(), "locked", true)
		case "unlocked":
			return g.groupWriter.WriteSetting(writeCtx, instance.Id, recipient.String(), "locked", false)
		case "approval_on":
			return g.groupWriter.WriteSetting(writeCtx, instance.Id, recipient.String(), "join_approval", true)
		case "approval_off":
			return g.groupWriter.WriteSetting(writeCtx, instance.Id, recipient.String(), "join_approval", false)
		case "admin_add":
			return g.groupWriter.WriteSetting(writeCtx, instance.Id, recipient.String(), "member_add", false)
		case "all_member_add":
			return g.groupWriter.WriteSetting(writeCtx, instance.Id, recipient.String(), "member_add", true)
		default:
			return nil
		}
	})

	g.loggerWrapper.GetLogger(instance.Id).LogInfo("[%s] Group settings updated successfully: %s", instance.Id, data.Action)
	return nil
}

func (g *groupService) writeGroupProjection(instanceID string, write func(context.Context) error) {
	_ = g.writeGroupProjectionResult(instanceID, write)
}

func (g *groupService) writeGroupProjectionResult(instanceID string, write func(context.Context) error) error {
	if g.groupWriter == nil || write == nil {
		return errors.New("group projection writer is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), groupProjectionWriteTimeout)
	defer cancel()
	if err := write(ctx); err != nil {
		if g.loggerWrapper != nil {
			g.loggerWrapper.GetLogger(instanceID).LogError("component=projection action=write_through instance_id=%s resource=groups result=failed error_code=projection_write_failed", instanceID)
		}
		if staleErr := g.groupWriter.MarkStale(instanceID); staleErr != nil {
			if g.loggerWrapper != nil {
				g.loggerWrapper.GetLogger(instanceID).LogError("component=projection action=mark_stale instance_id=%s resource=groups result=failed error_code=projection_state_write_failed", instanceID)
			}
		}
		return errors.New("group projection write-through failed")
	}
	return nil
}

func (g *groupService) PrepareManagementCommand(ctx context.Context, instanceID string) error {
	if g.outboundGuard == nil {
		return errors.New("group management outbound guard is required")
	}
	if err := g.outboundGuard.Wait(ctx, instanceID, 1); err != nil {
		return err
	}
	_, err := g.ensureClientConnected(instanceID)
	return err
}

func (g *groupService) observeManagementError(instanceID string, err error) error {
	if observer, ok := g.queryGuard.(interface{ ObserveError(string, error) error }); ok {
		return observer.ObserveError(instanceID, err)
	}
	return err
}

func (g *groupService) managementClient(instanceID string) (*whatsmeow.Client, error) {
	client := g.clients.Get(instanceID)
	if client == nil || !client.IsConnected() {
		return nil, ErrManagementProviderNotReady
	}
	return client, nil
}

func (g *groupService) SetManagementName(ctx context.Context, instanceID string, groupJID types.JID, name string) error {
	client, err := g.managementClient(instanceID)
	if err != nil {
		return err
	}
	if err := instance_runtime.DoProviderCommand(ctx, g.clients, func(commandCtx context.Context) error {
		return client.SetGroupName(commandCtx, groupJID, name)
	}); err != nil {
		return g.observeManagementError(instanceID, err)
	}
	g.writeGroupProjection(instanceID, func(writeCtx context.Context) error {
		return g.groupWriter.WriteName(writeCtx, instanceID, groupJID.String(), name)
	})
	return nil
}

func (g *groupService) SetManagementDescription(ctx context.Context, instanceID string, groupJID types.JID, description string) error {
	client, err := g.managementClient(instanceID)
	if err != nil {
		return err
	}
	if err := instance_runtime.DoProviderCommand(ctx, g.clients, func(commandCtx context.Context) error {
		return client.SetGroupTopic(commandCtx, groupJID, "", "", description)
	}); err != nil {
		return g.observeManagementError(instanceID, err)
	}
	g.writeGroupProjection(instanceID, func(writeCtx context.Context) error {
		return g.groupWriter.WriteTopic(writeCtx, instanceID, groupJID.String(), description)
	})
	return nil
}

func (g *groupService) SetManagementSetting(ctx context.Context, instanceID string, groupJID types.JID, action string) error {
	client, err := g.managementClient(instanceID)
	if err != nil {
		return g.observeManagementError(instanceID, err)
	}
	var projectionSetting string
	var enabled bool
	err = instance_runtime.DoProviderCommand(ctx, g.clients, func(commandCtx context.Context) error {
		switch action {
		case "announcement":
			projectionSetting, enabled = "announce", true
			return client.SetGroupAnnounce(commandCtx, groupJID, true)
		case "not_announcement":
			projectionSetting, enabled = "announce", false
			return client.SetGroupAnnounce(commandCtx, groupJID, false)
		case "locked":
			projectionSetting, enabled = "locked", true
			return client.SetGroupLocked(commandCtx, groupJID, true)
		case "unlocked":
			projectionSetting, enabled = "locked", false
			return client.SetGroupLocked(commandCtx, groupJID, false)
		case "approval_on":
			projectionSetting, enabled = "join_approval", true
			return client.SetGroupJoinApprovalMode(commandCtx, groupJID, true)
		case "approval_off":
			projectionSetting, enabled = "join_approval", false
			return client.SetGroupJoinApprovalMode(commandCtx, groupJID, false)
		case "admin_add":
			projectionSetting, enabled = "member_add", false
			return client.SetGroupMemberAddMode(commandCtx, groupJID, "admin_add")
		case "all_member_add":
			projectionSetting, enabled = "member_add", true
			return client.SetGroupMemberAddMode(commandCtx, groupJID, "all_member_add")
		default:
			return ErrInvalidManagementFilter
		}
	})
	if err != nil {
		return err
	}
	g.writeGroupProjection(instanceID, func(writeCtx context.Context) error {
		return g.groupWriter.WriteSetting(writeCtx, instanceID, groupJID.String(), projectionSetting, enabled)
	})
	return nil
}

func (g *groupService) LeaveManagementGroup(ctx context.Context, instanceID string, groupJID types.JID) error {
	client, err := g.managementClient(instanceID)
	if err != nil {
		return err
	}
	if err := instance_runtime.DoProviderCommand(ctx, g.clients, func(commandCtx context.Context) error {
		return client.LeaveGroup(commandCtx, groupJID)
	}); err != nil {
		return g.observeManagementError(instanceID, err)
	}
	g.writeGroupProjection(instanceID, func(writeCtx context.Context) error {
		return g.groupWriter.Tombstone(writeCtx, instanceID, groupJID.String())
	})
	return nil
}

func (g *groupService) ResetManagementInviteLink(ctx context.Context, instanceID string, groupJID types.JID) error {
	client, err := g.managementClient(instanceID)
	if err != nil {
		return g.observeManagementError(instanceID, err)
	}
	inviteLink, err := instance_runtime.DoProviderCommandValue(ctx, g.clients, func(commandCtx context.Context) (string, error) {
		return client.GetGroupInviteLink(commandCtx, groupJID, true)
	})
	if err != nil {
		return err
	}
	return g.writeGroupProjectionResult(instanceID, func(writeCtx context.Context) error {
		return g.groupWriter.WriteInviteLink(writeCtx, instanceID, groupJID.String(), inviteLink)
	})
}

func (g *groupService) SetManagementPhoto(ctx context.Context, instanceID string, groupJID types.JID, photo []byte) (string, error) {
	client, err := g.managementClient(instanceID)
	if err != nil {
		return "", err
	}
	pictureID, err := instance_runtime.DoProviderCommandValue(ctx, g.clients, func(commandCtx context.Context) (string, error) {
		return client.SetGroupPhoto(commandCtx, groupJID, photo)
	})
	if err != nil {
		return "", g.observeManagementError(instanceID, err)
	}
	return pictureID, nil
}

func (g *groupService) UpdateManagementParticipants(ctx context.Context, instanceID string, groupJID types.JID, participants []types.JID, action string) ([]types.GroupParticipant, error) {
	client, err := g.managementClient(instanceID)
	if err != nil {
		return nil, g.observeManagementError(instanceID, err)
	}
	results, err := instance_runtime.DoProviderCommandValue(ctx, g.clients, func(commandCtx context.Context) ([]types.GroupParticipant, error) {
		return client.UpdateGroupParticipants(commandCtx, groupJID, participants, whatsmeow.ParticipantChange(action))
	})
	if err != nil {
		return nil, g.observeManagementError(instanceID, err)
	}
	g.writeGroupProjection(instanceID, func(writeCtx context.Context) error {
		return g.groupWriter.WriteParticipants(writeCtx, instanceID, groupJID.String(), action, results)
	})
	return results, nil
}

func (g *groupService) CreateManagementGroup(ctx context.Context, instanceID, name string, participants []types.JID) (*types.GroupInfo, error) {
	client, err := g.managementClient(instanceID)
	if err != nil {
		return nil, err
	}
	group, err := instance_runtime.DoProviderCommandValue(ctx, g.clients, func(commandCtx context.Context) (*types.GroupInfo, error) {
		return client.CreateGroup(commandCtx, whatsmeow.ReqCreateGroup{Name: name, Participants: participants})
	})
	if err != nil {
		return nil, err
	}
	g.writeGroupProjection(instanceID, func(writeCtx context.Context) error { return g.groupWriter.WriteInfo(writeCtx, instanceID, group) })
	return group, nil
}

func (g *groupService) JoinManagementGroup(ctx context.Context, instanceID, code string) (managementJoinProviderResult, error) {
	client, err := g.managementClient(instanceID)
	if err != nil {
		return managementJoinProviderResult{}, g.observeManagementError(instanceID, err)
	}
	joinedGroup, err := instance_runtime.DoProviderCommandValue(ctx, g.clients, func(commandCtx context.Context) (types.JID, error) {
		return client.JoinGroupWithLink(commandCtx, code)
	})
	if errors.Is(err, whatsmeow.ErrInviteLinkInvalid) {
		return managementJoinProviderResult{Status: "rejected", Reason: "invalid_invite_link"}, nil
	}
	if errors.Is(err, whatsmeow.ErrInviteLinkRevoked) {
		return managementJoinProviderResult{Status: "rejected", Reason: "revoked_invite_link"}, nil
	}
	if err != nil {
		return managementJoinProviderResult{}, err
	}
	result := managementJoinProviderResult{GroupJID: joinedGroup.String(), Status: "unknown", Reason: "membership_not_confirmed"}
	queryCtx, cancel := context.WithTimeout(context.Background(), groupPostMutationQueryTimeout)
	defer cancel()
	info, queryErr := waquery.Do(queryCtx, g.queryGuard, instanceID, waquery.OperationGroupInfo, joinedGroup.String(), func(queryCtx context.Context) (*types.GroupInfo, error) {
		return client.GetGroupInfo(queryCtx, joinedGroup)
	})
	if queryErr == nil && info != nil {
		result.Status, result.Reason = "joined", ""
		g.writeGroupProjection(instanceID, func(writeCtx context.Context) error { return g.groupWriter.WriteInfo(writeCtx, instanceID, info) })
	}
	return result, nil
}

func (g *groupService) GetGroupRequestParticipants(ctx context.Context, data *GetGroupRequestParticipantsStruct, instance *instance_model.Instance) ([]EnrichedGroupParticipantRequest, error) {
	client, err := g.ensureClientConnected(instance.Id)
	if err != nil {
		return nil, err
	}

	recipient, ok := utils.ParseJID(data.GroupJID)
	if !ok {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] Error validating group jid", instance.Id)
		return nil, errors.New("invalid group jid")
	}

	requests, err := waquery.Do(ctx, g.queryGuard, instance.Id, waquery.OperationGroupJoinRequests, recipient.String(), func(queryCtx context.Context) ([]types.GroupParticipantRequest, error) {
		return client.GetGroupRequestParticipants(queryCtx, recipient)
	})
	if err != nil {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error getting group request participants: %v", instance.Id, err)
		return nil, err
	}

	g.loggerWrapper.GetLogger(instance.Id).LogInfo("[%s] Retrieved %d pending group requests", instance.Id, len(requests))

	// Enriquecer com informações de usuário (PushName)
	enrichedRequests := make([]EnrichedGroupParticipantRequest, len(requests))
	jidsToFetch := make([]types.JID, 0, len(requests))

	for _, req := range requests {
		if req.JID.User != "" {
			jidsToFetch = append(jidsToFetch, req.JID)
		}
	}

	// Buscar informações de usuário em lote
	userInfoMap := make(map[types.JID]types.UserInfo)
	if len(jidsToFetch) > 0 {
		resources := make([]string, len(jidsToFetch))
		for i, jid := range jidsToFetch {
			resources[i] = jid.String()
		}
		userInfoMap, err = waquery.Do(ctx, g.queryGuard, instance.Id, waquery.OperationUserInfo, waquery.ResourceKey(resources...), func(queryCtx context.Context) (map[types.JID]types.UserInfo, error) {
			return client.GetUserInfo(queryCtx, jidsToFetch)
		})
		if err != nil {
			g.loggerWrapper.GetLogger(instance.Id).LogWarn("[%s] Could not fetch user info: %v", instance.Id, err)
			// Continuar sem pushName se falhar
		}
	}

	// Montar resposta enriquecida
	for i, req := range requests {
		enrichedRequests[i] = EnrichedGroupParticipantRequest{
			JID:         req.JID,
			RequestedAt: req.RequestedAt,
			PushName:    "",
		}

		// Tentar obter PushName
		lookupJID := req.JID

		if userInfo, found := userInfoMap[lookupJID]; found {
			// VerifiedName é ponteiro, verificar se não é nil
			if userInfo.VerifiedName != nil && userInfo.VerifiedName.Details.GetVerifiedName() != "" {
				enrichedRequests[i].PushName = userInfo.VerifiedName.Details.GetVerifiedName()
			}
		}

		// Tentar obter do store de contatos se não tiver VerifiedName
		if enrichedRequests[i].PushName == "" && client.Store.Contacts != nil {
			if contactInfo, err := client.Store.Contacts.GetContact(context.Background(), lookupJID); err == nil && contactInfo.PushName != "" {
				enrichedRequests[i].PushName = contactInfo.PushName
			} else if contactInfo.FullName != "" {
				enrichedRequests[i].PushName = contactInfo.FullName
			}
		}
	}

	return enrichedRequests, nil
}

func (g *groupService) UpdateGroupRequestParticipants(data *UpdateGroupRequestParticipantsStruct, instance *instance_model.Instance) ([]types.GroupParticipant, error) {
	client, err := g.ensureClientConnected(instance.Id)
	if err != nil {
		return nil, err
	}

	recipient, ok := utils.ParseJID(data.GroupJID)
	if !ok {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] Error validating group jid", instance.Id)
		return nil, errors.New("invalid group jid")
	}

	// Validate action
	var action whatsmeow.ParticipantRequestChange
	switch data.Action {
	case "approve":
		action = whatsmeow.ParticipantChangeApprove
	case "reject":
		action = whatsmeow.ParticipantChangeReject
	default:
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] Invalid action: %s", instance.Id, data.Action)
		return nil, errors.New("invalid action. Valid actions: approve, reject")
	}

	// Parse participants JIDs
	var participants []types.JID
	for _, participant := range data.Participants {
		participantJID, ok := utils.ParseJID(participant)
		if !ok {
			g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] Error validating participant jid: %s", instance.Id, participant)
			return nil, errors.New("invalid participant jid: " + participant)
		}
		participants = append(participants, participantJID)
	}

	results, err := instance_runtime.DoProviderCommandValue(context.Background(), g.clients, func(commandCtx context.Context) ([]types.GroupParticipant, error) {
		return client.UpdateGroupRequestParticipants(commandCtx, recipient, participants, action)
	})
	if err != nil {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error updating group request participants: %v", instance.Id, err)
		return nil, err
	}
	if data.Action == "approve" {
		g.writeGroupProjection(instance.Id, func(writeCtx context.Context) error {
			return g.groupWriter.WriteParticipants(writeCtx, instance.Id, recipient.String(), "add", results)
		})
	}

	g.loggerWrapper.GetLogger(instance.Id).LogInfo("[%s] Successfully %sd %d participants", instance.Id, data.Action, len(participants))
	return results, nil
}

func NewGroupService(
	clients instance_runtime.CommandClientProvider,
	whatsmeowService whatsmeow_service.WhatsmeowService,
	queryGuard waquery.Guard,
	outboundGuard outbound.Guard,
	groupReader *projection_service.GroupReader,
	managementReader *ManagementReader,
	groupWriter *projection_service.GroupWriter,
	managementCommands group_repository.ManagementCommandRepository,
	groupPhotoAssets *GroupPhotoAssetService,
	mediaFetcher netguard.Fetcher,
	loggerWrapper *logger_wrapper.LoggerManager,
) GroupService {
	service := &groupService{
		clients:          clients,
		whatsmeowService: whatsmeowService,
		queryGuard:       queryGuard,
		outboundGuard:    outboundGuard,
		groupReader:      groupReader,
		managementReader: managementReader,
		auditReader:      NewManagementAuditReader(managementCommands),
		groupWriter:      groupWriter,
		mediaFetcher:     mediaFetcher,
		loggerWrapper:    loggerWrapper,
	}
	service.commandManager = NewManagementCommandManager(managementCommands, managementReader, service, groupPhotoAssets)
	return service
}
