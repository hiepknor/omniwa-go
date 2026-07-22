package group_service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	instance_runtime "github.com/evolution-foundation/evolution-go/pkg/instance/runtime"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	"github.com/evolution-foundation/evolution-go/pkg/netguard"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/evolution-foundation/evolution-go/pkg/utils"
	"github.com/evolution-foundation/evolution-go/pkg/waquery"
	whatsmeow_service "github.com/evolution-foundation/evolution-go/pkg/whatsmeow/service"
	"github.com/gin-gonic/gin"
	"github.com/vincent-petithory/dataurl"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"gorm.io/gorm"
)

type GroupService interface {
	ListGroups(ctx context.Context, instance *instance_model.Instance) ([]*types.GroupInfo, error)
	ListGroupsRead(ctx context.Context, instance *instance_model.Instance) ([]*types.GroupInfo, *projection_service.ProjectionReadMeta, error)
	SearchGroupsRead(ctx context.Context, instance *instance_model.Instance, term string, limit int, cursor string) ([]*types.GroupInfo, *projection_service.ProjectionReadMeta, error)
	GetGroupInfo(ctx context.Context, data *GetGroupInfoStruct, instance *instance_model.Instance) (*types.GroupInfo, error)
	GetGroupInfoRead(ctx context.Context, data *GetGroupInfoStruct, instance *instance_model.Instance) (*types.GroupInfo, *projection_service.ProjectionReadMeta, error)
	GetGroupInviteLink(ctx context.Context, data *GetGroupInviteLinkStruct, instance *instance_model.Instance) (string, error)
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
	clients          instance_runtime.ClientProvider
	whatsmeowService whatsmeow_service.WhatsmeowService
	loggerWrapper    *logger_wrapper.LoggerManager
	queryGuard       waquery.Guard
	groupReader      *projection_service.GroupReader
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

func (g *groupService) GetGroupInviteLink(ctx context.Context, data *GetGroupInviteLinkStruct, instance *instance_model.Instance) (string, error) {
	recipient, ok := utils.ParseJID(data.GroupJID)
	if !ok {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] Error validating message fields", instance.Id)
		return "", errors.New("invalid group jid")
	}
	if !data.Reset {
		if g.groupReader == nil {
			return "", errors.New("group projection reader is required")
		}
		inviteLink, _, found, err := g.groupReader.InviteLink(ctx, instance.Id, recipient.String())
		if err != nil {
			return "", err
		}
		if !found {
			return "", gorm.ErrRecordNotFound
		}
		return inviteLink, nil
	}
	client, err := g.ensureClientConnected(instance.Id)
	if err != nil {
		return "", err
	}

	var resp string
	if data.Reset {
		// Reset is a mutation, so it must never be single-flighted or consume the
		// information-query budget.
		resp, err = client.GetGroupInviteLink(ctx, recipient, true)
		err = g.queryGuard.ObserveError(instance.Id, err)
	}
	if err != nil {
		g.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error mute chat: %v", instance.Id, err)
		return "", err
	}
	g.writeGroupProjection(instance.Id, func(writeCtx context.Context) error {
		return g.groupWriter.WriteInviteLink(writeCtx, instance.Id, recipient.String(), resp)
	})

	return resp, nil
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

	pictureID, err := client.SetGroupPhoto(context.Background(), recipient, fileData)
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

	err = client.SetGroupName(context.Background(), recipient, data.Name)
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
	err = client.SetGroupTopic(context.Background(), recipient, "", "", data.Description)
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

	resp, err := client.CreateGroup(ctx, whatsmeow.ReqCreateGroup{
		Name:         data.GroupName,
		Participants: participants,
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

	results, err := client.UpdateGroupParticipants(context.Background(), data.GroupJID, participants, data.Action)
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

	joinedGroup, err := client.JoinGroupWithLink(context.Background(), data.Code)
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

	err = client.LeaveGroup(context.Background(), data.GroupJID)
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

	// Apply settings based on action
	switch data.Action {
	case "announcement":
		err = client.SetGroupAnnounce(context.Background(), recipient, true)
	case "not_announcement":
		err = client.SetGroupAnnounce(context.Background(), recipient, false)
	case "locked":
		err = client.SetGroupLocked(context.Background(), recipient, true)
	case "unlocked":
		err = client.SetGroupLocked(context.Background(), recipient, false)
	case "approval_on":
		err = client.SetGroupJoinApprovalMode(context.Background(), recipient, true)
	case "approval_off":
		err = client.SetGroupJoinApprovalMode(context.Background(), recipient, false)
	case "admin_add":
		err = client.SetGroupMemberAddMode(context.Background(), recipient, "admin_add")
	case "all_member_add":
		err = client.SetGroupMemberAddMode(context.Background(), recipient, "all_member_add")
	}

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
	if g.groupWriter == nil || write == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), groupProjectionWriteTimeout)
	defer cancel()
	if err := write(ctx); err != nil {
		g.loggerWrapper.GetLogger(instanceID).LogError("component=projection action=write_through instance_id=%s resource=groups result=failed error_code=projection_write_failed", instanceID)
		if staleErr := g.groupWriter.MarkStale(instanceID); staleErr != nil {
			g.loggerWrapper.GetLogger(instanceID).LogError("component=projection action=mark_stale instance_id=%s resource=groups result=failed error_code=projection_state_write_failed", instanceID)
		}
	}
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

	results, err := client.UpdateGroupRequestParticipants(context.Background(), recipient, participants, action)
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
	clients instance_runtime.ClientProvider,
	whatsmeowService whatsmeow_service.WhatsmeowService,
	queryGuard waquery.Guard,
	groupReader *projection_service.GroupReader,
	groupWriter *projection_service.GroupWriter,
	mediaFetcher netguard.Fetcher,
	loggerWrapper *logger_wrapper.LoggerManager,
) GroupService {
	return &groupService{
		clients:          clients,
		whatsmeowService: whatsmeowService,
		queryGuard:       queryGuard,
		groupReader:      groupReader,
		groupWriter:      groupWriter,
		mediaFetcher:     mediaFetcher,
		loggerWrapper:    loggerWrapper,
	}
}
