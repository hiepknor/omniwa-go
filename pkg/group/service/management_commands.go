package group_service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	group_model "github.com/evolution-foundation/evolution-go/pkg/group/model"
	group_repository "github.com/evolution-foundation/evolution-go/pkg/group/repository"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/outbound"
	"github.com/evolution-foundation/evolution-go/pkg/utils"
	"github.com/evolution-foundation/evolution-go/pkg/waquery"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types"
)

var (
	ErrManagementPermissionDenied  = errors.New("group management permission denied")
	ErrManagementPermissionUnknown = errors.New("group management permission unknown")
	ErrManagementProviderNotReady  = errors.New("group management provider is not ready")
)

type ManagementCommandMetadata struct {
	ActorReference string
	RequestID      string
	IdempotencyKey string
}

type CommandAcknowledgement struct {
	CommandID                 string `json:"commandId" format:"uuid"`
	Command                   string `json:"command"`
	GroupJID                  string `json:"groupJid"`
	Status                    string `json:"status" enums:"accepted,completed,partially_completed,unknown"`
	ProjectionRefreshExpected bool   `json:"projectionRefreshExpected"`
}

type ManagementParticipantRequest struct {
	GroupJID     string   `json:"groupJid"`
	Action       string   `json:"action" enums:"add,remove,promote,demote"`
	Participants []string `json:"participants"`
}

type ParticipantOutcome struct {
	Participant string  `json:"participant"`
	Status      string  `json:"status" enums:"succeeded,failed,unknown"`
	Code        *string `json:"code" enums:"already_member,not_member,admin_required,invalid_participant,provider_rejected,unknown_outcome"`
	Message     string  `json:"message"`
}

type ParticipantCommandResult struct {
	CommandID                 string               `json:"commandId" format:"uuid"`
	GroupJID                  string               `json:"groupJid"`
	Action                    string               `json:"action" enums:"add,remove,promote,demote"`
	RequestedCount            int                  `json:"requestedCount"`
	SucceededCount            int                  `json:"succeededCount"`
	FailedCount               int                  `json:"failedCount"`
	UnknownCount              int                  `json:"unknownCount"`
	Status                    string               `json:"status" enums:"completed,partially_completed,failed,unknown"`
	ProjectionRefreshExpected bool                 `json:"projectionRefreshExpected"`
	Outcomes                  []ParticipantOutcome `json:"outcomes"`
}

type CreateGroupCommandResult struct {
	CommandID                 string               `json:"commandId" format:"uuid"`
	GroupJID                  string               `json:"groupJid,omitempty"`
	Name                      string               `json:"name"`
	RequestedCount            int                  `json:"requestedCount"`
	SucceededCount            int                  `json:"succeededCount"`
	FailedCount               int                  `json:"failedCount"`
	UnknownCount              int                  `json:"unknownCount"`
	AcknowledgementStatus     string               `json:"acknowledgementStatus" enums:"completed,partially_completed,failed,unknown"`
	ProjectionRefreshExpected bool                 `json:"projectionRefreshExpected"`
	ParticipantOutcomes       []ParticipantOutcome `json:"participantOutcomes"`
}

type JoinGroupCommandResult struct {
	CommandID                 string  `json:"commandId" format:"uuid"`
	GroupJID                  *string `json:"groupJid,omitempty"`
	Status                    string  `json:"status" enums:"joined,already_member,approval_required,rejected,unknown"`
	Reason                    *string `json:"reason,omitempty"`
	ProjectionRefreshExpected bool    `json:"projectionRefreshExpected"`
}

type managementJoinProviderResult struct {
	GroupJID string
	Status   string
	Reason   string
}

type managementCommandProvider interface {
	PrepareManagementCommand(context.Context, string) error
	SetManagementName(context.Context, string, types.JID, string) error
	SetManagementDescription(context.Context, string, types.JID, string) error
	SetManagementSetting(context.Context, string, types.JID, string) error
	SetManagementPhoto(context.Context, string, types.JID, []byte) (string, error)
	LeaveManagementGroup(context.Context, string, types.JID) error
	ResetManagementInviteLink(context.Context, string, types.JID) error
	UpdateManagementParticipants(context.Context, string, types.JID, []types.JID, string) ([]types.GroupParticipant, error)
	CreateManagementGroup(context.Context, string, string, []types.JID) (*types.GroupInfo, error)
	JoinManagementGroup(context.Context, string, string) (managementJoinProviderResult, error)
}

type ManagementCommandManager struct {
	repository  group_repository.ManagementCommandRepository
	reader      *ManagementReader
	provider    managementCommandProvider
	photoAssets managementPhotoAssets
}

type managementPhotoAssets interface {
	Prepare(context.Context, string, string, string, string) (*PreparedGroupPhoto, error)
	Commit(context.Context, string, string, string, string, string) error
}

func NewManagementCommandManager(repository group_repository.ManagementCommandRepository, reader *ManagementReader, provider managementCommandProvider, photoAssets ...managementPhotoAssets) *ManagementCommandManager {
	manager := &ManagementCommandManager{repository: repository, reader: reader, provider: provider}
	if len(photoAssets) > 0 {
		manager.photoAssets = photoAssets[0]
	}
	return manager
}

type simpleManagementCommand struct {
	commandType string
	groupJID    string
	payload     any
	audit       map[string]any
	decision    func(GroupActions) ActionDecision
	execute     func(context.Context, string, types.JID) error
}

func (m *ManagementCommandManager) SetName(ctx context.Context, instance *instance_model.Instance, data *SetGroupNameStruct, metadata ManagementCommandMetadata) (*CommandAcknowledgement, error) {
	if data == nil || strings.TrimSpace(data.Name) == "" || len(data.Name) > 100 {
		return nil, ErrInvalidManagementFilter
	}
	return m.executeSimple(ctx, instance, metadata, simpleManagementCommand{
		commandType: "name_updated", groupJID: data.GroupJID, payload: data,
		decision: func(actions GroupActions) ActionDecision { return actions.EditName },
		execute: func(callCtx context.Context, instanceID string, jid types.JID) error {
			return m.provider.SetManagementName(callCtx, instanceID, jid, data.Name)
		},
	})
}

func (m *ManagementCommandManager) SetDescription(ctx context.Context, instance *instance_model.Instance, data *SetGroupDescriptionStruct, metadata ManagementCommandMetadata) (*CommandAcknowledgement, error) {
	if data == nil || len(data.Description) > 2048 {
		return nil, ErrInvalidManagementFilter
	}
	return m.executeSimple(ctx, instance, metadata, simpleManagementCommand{
		commandType: "description_updated", groupJID: data.GroupJID, payload: data,
		decision: func(actions GroupActions) ActionDecision { return actions.EditDescription },
		execute: func(callCtx context.Context, instanceID string, jid types.JID) error {
			return m.provider.SetManagementDescription(callCtx, instanceID, jid, data.Description)
		},
	})
}

func (m *ManagementCommandManager) UpdateSetting(ctx context.Context, instance *instance_model.Instance, data *UpdateGroupSettingsStruct, metadata ManagementCommandMetadata) (*CommandAcknowledgement, error) {
	if data == nil || !oneOf(data.Action, "announcement", "not_announcement", "locked", "unlocked", "approval_on", "approval_off", "admin_add", "all_member_add") {
		return nil, ErrInvalidManagementFilter
	}
	setting := data.Action
	return m.executeSimple(ctx, instance, metadata, simpleManagementCommand{
		commandType: "settings_updated", groupJID: data.GroupJID, payload: data, audit: map[string]any{"setting": setting},
		decision: func(actions GroupActions) ActionDecision { return actions.EditSettings },
		execute: func(callCtx context.Context, instanceID string, jid types.JID) error {
			return m.provider.SetManagementSetting(callCtx, instanceID, jid, setting)
		},
	})
}

func (m *ManagementCommandManager) Leave(ctx context.Context, instance *instance_model.Instance, data *ManagementLeaveGroupRequest, metadata ManagementCommandMetadata) (*CommandAcknowledgement, error) {
	if data == nil || data.GroupJID == "" {
		return nil, ErrInvalidManagementFilter
	}
	return m.executeSimple(ctx, instance, metadata, simpleManagementCommand{
		commandType: "left", groupJID: data.GroupJID, payload: data,
		decision: func(actions GroupActions) ActionDecision { return actions.LeaveGroup },
		execute: func(callCtx context.Context, instanceID string, jid types.JID) error {
			return m.provider.LeaveManagementGroup(callCtx, instanceID, jid)
		},
	})
}

func (m *ManagementCommandManager) ResetInviteLink(ctx context.Context, instance *instance_model.Instance, data *GetGroupInviteLinkStruct, metadata ManagementCommandMetadata) (*CommandAcknowledgement, error) {
	if data == nil || !data.Reset {
		return nil, ErrInvalidManagementFilter
	}
	return m.executeSimple(ctx, instance, metadata, simpleManagementCommand{
		commandType: "invite_link_reset", groupJID: data.GroupJID, payload: map[string]any{"groupJid": data.GroupJID, "reset": true},
		decision: func(actions GroupActions) ActionDecision { return actions.ResetInviteLink },
		execute: func(callCtx context.Context, instanceID string, jid types.JID) error {
			return m.provider.ResetManagementInviteLink(callCtx, instanceID, jid)
		},
	})
}

func (m *ManagementCommandManager) SetPhoto(ctx context.Context, instance *instance_model.Instance, data *SetGroupPhotoAssetRequest, metadata ManagementCommandMetadata) (*CommandAcknowledgement, error) {
	if data == nil {
		return nil, group_repository.ErrInvalidManagementCommand
	}
	jid, ok := utils.ParseJID(data.GroupJID)
	if m == nil || m.repository == nil || m.reader == nil || m.provider == nil || m.photoAssets == nil || ctx == nil || instance == nil ||
		uuid.Validate(instance.Id) != nil || instance.Jid == "" || !ok || jid.Server != types.GroupServer || uuid.Validate(data.MediaAssetID) != nil ||
		strings.TrimSpace(metadata.ActorReference) == "" || len(metadata.IdempotencyKey) > 255 {
		return nil, group_repository.ErrInvalidManagementCommand
	}
	fingerprint, err := managementFingerprint("photo_updated", jid.String(), data)
	if err != nil {
		return nil, err
	}
	var idempotencyHash *string
	if metadata.IdempotencyKey != "" {
		value := managementHash(metadata.IdempotencyKey)
		idempotencyHash = &value
	}
	var requestID *string
	if metadata.RequestID != "" {
		requestID = &metadata.RequestID
	}
	command, created, err := m.repository.Create(ctx, group_repository.CreateManagementCommandInput{
		ID: uuid.NewString(), InstanceID: instance.Id, GroupJID: jid.String(), CommandType: "photo_updated",
		IdempotencyKeyHash: idempotencyHash, RequestFingerprint: fingerprint, RequestID: requestID,
		ActorType: "instance", ActorReferenceHash: managementHash(metadata.ActorReference),
	})
	if err != nil {
		return nil, err
	}
	if !created {
		return acknowledgementFromStored(command)
	}
	detail, _, err := m.reader.Get(ctx, instance, jid.String())
	if err != nil {
		m.completeRejected(ctx, command, "projection_not_ready", nil)
		return nil, err
	}
	if detail.Actions.SetPhoto.State != "allowed" {
		reason := "permission_unknown"
		if detail.Actions.SetPhoto.Reason != nil {
			reason = *detail.Actions.SetPhoto.Reason
		}
		m.completeRejected(ctx, command, reason, nil)
		if detail.Actions.SetPhoto.State == "denied" {
			return nil, ErrManagementPermissionDenied
		}
		return nil, ErrManagementPermissionUnknown
	}
	prepared, err := m.photoAssets.Prepare(ctx, instance.Id, jid.String(), data.MediaAssetID, command.ID)
	if err != nil {
		m.completeRejected(ctx, command, groupPhotoErrorReason(err), nil)
		return nil, err
	}
	if err := m.provider.PrepareManagementCommand(ctx, instance.Id); err != nil {
		m.completeRejected(ctx, command, "provider_disconnected", nil)
		if managementRateLimited(err) {
			return nil, err
		}
		return nil, ErrManagementProviderNotReady
	}
	if _, err := m.repository.MarkExecuting(ctx, instance.Id, command.ID); err != nil {
		return nil, err
	}
	acknowledgement := CommandAcknowledgement{CommandID: command.ID, Command: "photo_updated", GroupJID: jid.String(), Status: "completed", ProjectionRefreshExpected: true}
	pictureID, providerErr := m.provider.SetManagementPhoto(ctx, instance.Id, jid, prepared.Bytes)
	if managementRateLimited(providerErr) {
		m.completeRejected(ctx, command, "rate_limited", nil)
		return nil, providerErr
	}
	if providerErr != nil || pictureID == "" {
		acknowledgement.Status = "unknown"
		_ = m.complete(ctx, command, group_model.ManagementCommandUnknown, acknowledgement, map[string]any{"reason": "unknown_outcome"})
		return &acknowledgement, nil
	}
	if err := m.photoAssets.Commit(ctx, instance.Id, jid.String(), pictureID, prepared.MediaAssetID, command.ID); err != nil {
		acknowledgement.Status = "unknown"
		_ = m.complete(ctx, command, group_model.ManagementCommandUnknown, acknowledgement, map[string]any{"reason": "projection_commit_failed"})
		return &acknowledgement, nil
	}
	if err := m.complete(ctx, command, group_model.ManagementCommandCompleted, acknowledgement, nil); err != nil {
		acknowledgement.Status = "unknown"
	}
	return &acknowledgement, nil
}

func groupPhotoErrorReason(err error) string {
	switch {
	case errors.Is(err, ErrGroupPhotoAssetNotFound):
		return "media_asset_not_found"
	case errors.Is(err, ErrGroupPhotoAssetInvalidType):
		return "media_asset_invalid_type"
	case errors.Is(err, ErrGroupPhotoAssetTooLarge):
		return "media_asset_too_large"
	case errors.Is(err, ErrGroupPhotoAssetIntegrity):
		return "media_asset_integrity_failed"
	case errors.Is(err, ErrGroupPhotoAssetStorage):
		return "media_asset_storage_unavailable"
	default:
		return "media_asset_not_ready"
	}
}

func (m *ManagementCommandManager) UpdateParticipants(ctx context.Context, instance *instance_model.Instance, data *ManagementParticipantRequest, metadata ManagementCommandMetadata) (*ParticipantCommandResult, error) {
	if m == nil || data == nil || !oneOf(data.Action, "add", "remove", "promote", "demote") || len(data.Participants) < 1 || len(data.Participants) > 100 {
		return nil, ErrInvalidManagementFilter
	}
	jid, ok := utils.ParseJID(data.GroupJID)
	if !ok || jid.Server != types.GroupServer || instance == nil || uuid.Validate(instance.Id) != nil || instance.Jid == "" || strings.TrimSpace(metadata.ActorReference) == "" || len(metadata.IdempotencyKey) > 255 {
		return nil, group_repository.ErrInvalidManagementCommand
	}
	seen := make(map[string]struct{}, len(data.Participants))
	providerParticipants := make([]types.JID, len(data.Participants))
	for index, reference := range data.Participants {
		if _, duplicate := seen[reference]; duplicate || reference == "" {
			return nil, ErrInvalidManagementFilter
		}
		seen[reference] = struct{}{}
		if data.Action == "add" {
			participant, err := types.ParseJID(reference)
			if err != nil || participant.Server != types.DefaultUserServer || participant.String() != reference {
				return nil, ErrInvalidManagementFilter
			}
			providerParticipants[index] = participant
			continue
		}
		if uuid.Validate(reference) != nil {
			return nil, ErrInvalidManagementFilter
		}
		group, member, err := m.reader.groups.GetManagementMember(ctx, instance.Id, normalizedActorIdentity(instance.Jid), jid.String(), reference)
		if err != nil {
			return nil, err
		}
		actions := memberActions(managementSummary(*group), member.Role, member.IsActor, m.reader.now().UTC())
		decision := map[string]ActionDecision{"remove": actions.Remove, "promote": actions.Promote, "demote": actions.Demote}[data.Action]
		if decision.State == "denied" {
			return nil, ErrManagementPermissionDenied
		}
		if decision.State != "allowed" {
			return nil, ErrManagementPermissionUnknown
		}
		participant, err := types.ParseJID(member.Participant.ParticipantID)
		if err != nil || participant.IsEmpty() {
			return nil, errors.New("projected group participant identity is invalid")
		}
		providerParticipants[index] = participant
	}
	detail, _, err := m.reader.Get(ctx, instance, jid.String())
	if err != nil {
		return nil, err
	}
	baseDecision := map[string]ActionDecision{
		"add": detail.Actions.AddMembers, "remove": detail.Actions.RemoveMembers,
		"promote": detail.Actions.PromoteMembers, "demote": detail.Actions.DemoteMembers,
	}[data.Action]
	if baseDecision.State == "denied" {
		return nil, ErrManagementPermissionDenied
	}
	if baseDecision.State != "allowed" {
		return nil, ErrManagementPermissionUnknown
	}
	commandType := participantCommandType(data.Action)
	fingerprint, err := managementFingerprint(commandType, jid.String(), data)
	if err != nil {
		return nil, err
	}
	actorHash := managementHash(metadata.ActorReference)
	var idempotencyHash *string
	if metadata.IdempotencyKey != "" {
		value := managementHash(metadata.IdempotencyKey)
		idempotencyHash = &value
	}
	var requestID *string
	if metadata.RequestID != "" {
		requestID = &metadata.RequestID
	}
	command, created, err := m.repository.Create(ctx, group_repository.CreateManagementCommandInput{
		ID: uuid.NewString(), InstanceID: instance.Id, GroupJID: jid.String(), CommandType: commandType,
		IdempotencyKeyHash: idempotencyHash, RequestFingerprint: fingerprint, RequestID: requestID, ActorType: "instance", ActorReferenceHash: actorHash,
	})
	if err != nil {
		return nil, err
	}
	if !created {
		var result ParticipantCommandResult
		if command.Status == group_model.ManagementCommandRequested || command.Status == group_model.ManagementCommandExecuting {
			return &ParticipantCommandResult{CommandID: command.ID, GroupJID: jid.String(), Action: data.Action, RequestedCount: len(data.Participants), Status: "unknown", Outcomes: []ParticipantOutcome{}}, nil
		}
		if json.Unmarshal(command.SafeOutcome, &result) != nil {
			return nil, group_repository.ErrManagementCommandConflict
		}
		return &result, nil
	}
	if err := m.provider.PrepareManagementCommand(ctx, instance.Id); err != nil {
		m.completeRejected(ctx, command, "provider_disconnected", map[string]any{"participantCount": len(data.Participants)})
		if managementRateLimited(err) {
			return nil, err
		}
		return nil, ErrManagementProviderNotReady
	}
	if _, err := m.repository.MarkExecuting(ctx, instance.Id, command.ID); err != nil {
		return nil, err
	}
	providerResults, providerErr := m.provider.UpdateManagementParticipants(ctx, instance.Id, jid, providerParticipants, data.Action)
	if managementRateLimited(providerErr) {
		m.completeRejected(ctx, command, "rate_limited", map[string]any{"participantCount": len(data.Participants)})
		return nil, providerErr
	}
	result := ParticipantCommandResult{CommandID: command.ID, GroupJID: jid.String(), Action: data.Action, RequestedCount: len(data.Participants), ProjectionRefreshExpected: true, Outcomes: make([]ParticipantOutcome, len(data.Participants))}
	status := group_model.ManagementCommandCompleted
	if providerErr != nil {
		result.Status, result.UnknownCount = "unknown", len(data.Participants)
		status = group_model.ManagementCommandUnknown
		for index, reference := range data.Participants {
			result.Outcomes[index] = ParticipantOutcome{Participant: publicManagementParticipantReference(instance.Id, reference), Status: "unknown", Code: managementStringPointer("unknown_outcome"), Message: "provider outcome is unknown"}
		}
	} else {
		byJID := make(map[string]types.GroupParticipant, len(providerResults))
		for _, providerResult := range providerResults {
			byJID[providerResult.JID.String()] = providerResult
		}
		for index, providerParticipant := range providerParticipants {
			providerResult, found := byJID[providerParticipant.String()]
			if !found {
				result.Outcomes[index] = ParticipantOutcome{Participant: publicManagementParticipantReference(instance.Id, data.Participants[index]), Status: "unknown", Code: managementStringPointer("unknown_outcome"), Message: "provider outcome is unknown"}
				result.UnknownCount++
				continue
			}
			outcome := ParticipantOutcome{Participant: publicManagementParticipantReference(instance.Id, data.Participants[index]), Status: "succeeded", Message: "participant command completed"}
			if providerResult.Error != 0 {
				outcome.Status, outcome.Code, outcome.Message = "failed", managementStringPointer("provider_rejected"), "provider rejected participant command"
				result.FailedCount++
			} else {
				result.SucceededCount++
			}
			result.Outcomes[index] = outcome
		}
		switch {
		case result.UnknownCount > 0:
			result.Status, status = "unknown", group_model.ManagementCommandUnknown
		case result.FailedCount == 0:
			result.Status = "completed"
		case result.SucceededCount == 0:
			result.Status, status = "failed", group_model.ManagementCommandFailed
		default:
			result.Status, status = "partially_completed", group_model.ManagementCommandPartiallyCompleted
		}
	}
	outcome, _ := json.Marshal(result)
	summary, _ := json.Marshal(map[string]any{"participantCount": result.RequestedCount, "failureCount": result.FailedCount + result.UnknownCount})
	if _, err := m.repository.Complete(ctx, instance.Id, command.ID, group_repository.CompleteManagementCommandInput{Status: status, SafeOutcome: outcome, AuditSummary: summary}); err != nil {
		result.Status = "unknown"
	}
	return &result, nil
}

func (m *ManagementCommandManager) CreateGroup(ctx context.Context, instance *instance_model.Instance, data *CreateGroupStruct, metadata ManagementCommandMetadata) (*CreateGroupCommandResult, error) {
	if m == nil || data == nil || strings.TrimSpace(data.GroupName) == "" || len(data.GroupName) > 100 || len(data.Participants) < 1 || len(data.Participants) > 100 ||
		instance == nil || uuid.Validate(instance.Id) != nil || strings.TrimSpace(metadata.ActorReference) == "" || len(metadata.IdempotencyKey) > 255 {
		return nil, ErrInvalidManagementFilter
	}
	participants := make([]types.JID, len(data.Participants))
	seen := make(map[string]struct{}, len(data.Participants))
	for index, reference := range data.Participants {
		jid, err := types.ParseJID(reference)
		if err != nil || jid.Server != types.DefaultUserServer || jid.String() != reference {
			return nil, ErrInvalidManagementFilter
		}
		if _, duplicate := seen[reference]; duplicate {
			return nil, ErrInvalidManagementFilter
		}
		seen[reference] = struct{}{}
		participants[index] = jid
	}
	command, created, err := m.createUnresolved(ctx, instance, metadata, "created", data)
	if err != nil {
		return nil, err
	}
	if !created {
		if command.Status == group_model.ManagementCommandRequested || command.Status == group_model.ManagementCommandExecuting {
			return &CreateGroupCommandResult{
				CommandID: command.ID, Name: strings.TrimSpace(data.GroupName), RequestedCount: len(data.Participants),
				AcknowledgementStatus: "unknown", ParticipantOutcomes: []ParticipantOutcome{},
			}, nil
		}
		var result CreateGroupCommandResult
		if json.Unmarshal(command.SafeOutcome, &result) != nil {
			return nil, group_repository.ErrManagementCommandConflict
		}
		return &result, nil
	}
	if err := m.provider.PrepareManagementCommand(ctx, instance.Id); err != nil {
		m.completeRejected(ctx, command, "provider_disconnected", map[string]any{"participantCount": len(participants)})
		if managementRateLimited(err) {
			return nil, err
		}
		return nil, ErrManagementProviderNotReady
	}
	if _, err := m.repository.MarkExecuting(ctx, instance.Id, command.ID); err != nil {
		return nil, err
	}
	providerGroup, providerErr := m.provider.CreateManagementGroup(ctx, instance.Id, data.GroupName, participants)
	if managementRateLimited(providerErr) {
		m.completeRejected(ctx, command, "rate_limited", map[string]any{"participantCount": len(participants)})
		return nil, providerErr
	}
	result := CreateGroupCommandResult{CommandID: command.ID, Name: strings.TrimSpace(data.GroupName), RequestedCount: len(participants), ParticipantOutcomes: make([]ParticipantOutcome, len(participants))}
	status := group_model.ManagementCommandUnknown
	if providerErr != nil || providerGroup == nil || providerGroup.JID.IsEmpty() {
		result.AcknowledgementStatus, result.UnknownCount = "unknown", len(participants)
		for index, reference := range data.Participants {
			result.ParticipantOutcomes[index] = ParticipantOutcome{Participant: publicManagementParticipantReference(instance.Id, reference), Status: "unknown", Code: managementStringPointer("unknown_outcome"), Message: "provider outcome is unknown"}
		}
	} else {
		result.GroupJID, result.Name, result.ProjectionRefreshExpected = providerGroup.JID.String(), providerGroup.Name, true
		byJID := make(map[string]types.GroupParticipant, len(providerGroup.Participants))
		for _, providerParticipant := range providerGroup.Participants {
			byJID[providerParticipant.JID.String()] = providerParticipant
		}
		for index, participant := range participants {
			providerParticipant, found := byJID[participant.String()]
			outcome := ParticipantOutcome{Participant: publicManagementParticipantReference(instance.Id, data.Participants[index])}
			switch {
			case !found:
				outcome.Status, outcome.Code, outcome.Message = "unknown", managementStringPointer("unknown_outcome"), "provider outcome is unknown"
				result.UnknownCount++
			case providerParticipant.Error != 0:
				outcome.Status, outcome.Code, outcome.Message = "failed", managementStringPointer("provider_rejected"), "provider rejected participant"
				result.FailedCount++
			default:
				outcome.Status, outcome.Message = "succeeded", "participant added"
				result.SucceededCount++
			}
			result.ParticipantOutcomes[index] = outcome
		}
		switch {
		case result.UnknownCount > 0:
			result.AcknowledgementStatus, status = "unknown", group_model.ManagementCommandUnknown
		case result.FailedCount == 0:
			result.AcknowledgementStatus, status = "completed", group_model.ManagementCommandCompleted
		case result.SucceededCount == 0:
			result.AcknowledgementStatus, status = "failed", group_model.ManagementCommandFailed
		default:
			result.AcknowledgementStatus, status = "partially_completed", group_model.ManagementCommandPartiallyCompleted
		}
	}
	outcome, _ := json.Marshal(result)
	summary, _ := json.Marshal(map[string]any{"participantCount": result.RequestedCount, "failureCount": result.FailedCount + result.UnknownCount})
	completion := group_repository.CompleteManagementCommandInput{Status: status, SafeOutcome: outcome, AuditSummary: summary, ResolvedGroupJID: result.GroupJID}
	if _, err := m.repository.Complete(ctx, instance.Id, command.ID, completion); err != nil {
		result.AcknowledgementStatus = "unknown"
	}
	return &result, nil
}

func (m *ManagementCommandManager) JoinGroup(ctx context.Context, instance *instance_model.Instance, data *JoinGroupStruct, metadata ManagementCommandMetadata) (*JoinGroupCommandResult, error) {
	if m == nil || data == nil || strings.TrimSpace(data.Code) == "" || len(data.Code) > 2048 || instance == nil || uuid.Validate(instance.Id) != nil || strings.TrimSpace(metadata.ActorReference) == "" || len(metadata.IdempotencyKey) > 255 {
		return nil, ErrInvalidManagementFilter
	}
	command, created, err := m.createUnresolved(ctx, instance, metadata, "joined", data)
	if err != nil {
		return nil, err
	}
	if !created {
		if command.Status == group_model.ManagementCommandRequested || command.Status == group_model.ManagementCommandExecuting {
			reason := "command_in_progress"
			return &JoinGroupCommandResult{CommandID: command.ID, Status: "unknown", Reason: &reason}, nil
		}
		var result JoinGroupCommandResult
		if json.Unmarshal(command.SafeOutcome, &result) != nil {
			return nil, group_repository.ErrManagementCommandConflict
		}
		return &result, nil
	}
	if err := m.provider.PrepareManagementCommand(ctx, instance.Id); err != nil {
		m.completeRejected(ctx, command, "provider_disconnected", nil)
		if managementRateLimited(err) {
			return nil, err
		}
		return nil, ErrManagementProviderNotReady
	}
	if _, err := m.repository.MarkExecuting(ctx, instance.Id, command.ID); err != nil {
		return nil, err
	}
	providerResult, providerErr := m.provider.JoinManagementGroup(ctx, instance.Id, data.Code)
	if managementRateLimited(providerErr) {
		m.completeRejected(ctx, command, "rate_limited", nil)
		return nil, providerErr
	}
	result := JoinGroupCommandResult{CommandID: command.ID, Status: "unknown"}
	status := group_model.ManagementCommandUnknown
	if providerErr == nil {
		result.Status, result.ProjectionRefreshExpected = providerResult.Status, providerResult.GroupJID != ""
		if providerResult.GroupJID != "" {
			groupJID := providerResult.GroupJID
			result.GroupJID = &groupJID
		}
		if providerResult.Reason != "" {
			reason := providerResult.Reason
			result.Reason = &reason
		}
		switch result.Status {
		case "joined", "already_member":
			status = group_model.ManagementCommandCompleted
		case "approval_required":
			status = group_model.ManagementCommandPartiallyCompleted
		case "rejected":
			status = group_model.ManagementCommandFailed
		case "unknown":
		default:
			result.Status = "unknown"
			reason := "unknown_outcome"
			result.Reason = &reason
		}
	} else {
		reason := "unknown_outcome"
		result.Reason = &reason
	}
	outcome, _ := json.Marshal(result)
	summary, _ := json.Marshal(map[string]any{"reason": valueOrEmpty(result.Reason)})
	resolved := ""
	if result.GroupJID != nil {
		resolved = *result.GroupJID
	}
	if _, err := m.repository.Complete(ctx, instance.Id, command.ID, group_repository.CompleteManagementCommandInput{Status: status, SafeOutcome: outcome, AuditSummary: summary, ResolvedGroupJID: resolved}); err != nil {
		result.Status = "unknown"
	}
	return &result, nil
}

func (m *ManagementCommandManager) createUnresolved(ctx context.Context, instance *instance_model.Instance, metadata ManagementCommandMetadata, commandType string, payload any) (*group_model.ManagementCommand, bool, error) {
	fingerprint, err := managementFingerprint(commandType, "", payload)
	if err != nil {
		return nil, false, err
	}
	var idempotencyHash *string
	if metadata.IdempotencyKey != "" {
		value := managementHash(metadata.IdempotencyKey)
		idempotencyHash = &value
	}
	var requestID *string
	if metadata.RequestID != "" {
		requestID = &metadata.RequestID
	}
	return m.repository.Create(ctx, group_repository.CreateManagementCommandInput{
		ID: uuid.NewString(), InstanceID: instance.Id, CommandType: commandType, IdempotencyKeyHash: idempotencyHash,
		RequestFingerprint: fingerprint, RequestID: requestID, ActorType: "instance", ActorReferenceHash: managementHash(metadata.ActorReference),
	})
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func managementStringPointer(value string) *string {
	return &value
}

func publicManagementParticipantReference(instanceID, participant string) string {
	if uuid.Validate(participant) == nil {
		return participant
	}
	return "participant_" + managementHash(instanceID+"\x00"+participant)
}

func participantCommandType(action string) string {
	return map[string]string{"add": "participant_added", "remove": "participant_removed", "promote": "participant_promoted", "demote": "participant_demoted"}[action]
}

func (m *ManagementCommandManager) executeSimple(ctx context.Context, instance *instance_model.Instance, metadata ManagementCommandMetadata, input simpleManagementCommand) (*CommandAcknowledgement, error) {
	jid, ok := utils.ParseJID(input.groupJID)
	if m == nil || m.repository == nil || m.reader == nil || m.provider == nil || ctx == nil || instance == nil || uuid.Validate(instance.Id) != nil || instance.Jid == "" ||
		!ok || jid.Server != types.GroupServer || strings.TrimSpace(metadata.ActorReference) == "" || len(metadata.IdempotencyKey) > 255 {
		return nil, group_repository.ErrInvalidManagementCommand
	}
	fingerprint, err := managementFingerprint(input.commandType, jid.String(), input.payload)
	if err != nil {
		return nil, err
	}
	actorHash := managementHash(metadata.ActorReference)
	var idempotencyHash *string
	if metadata.IdempotencyKey != "" {
		value := managementHash(metadata.IdempotencyKey)
		idempotencyHash = &value
	}
	var requestID *string
	if metadata.RequestID != "" {
		value := metadata.RequestID
		requestID = &value
	}
	command, created, err := m.repository.Create(ctx, group_repository.CreateManagementCommandInput{
		ID: uuid.NewString(), InstanceID: instance.Id, GroupJID: jid.String(), CommandType: input.commandType,
		IdempotencyKeyHash: idempotencyHash, RequestFingerprint: fingerprint, RequestID: requestID,
		ActorType: "instance", ActorReferenceHash: actorHash,
	})
	if err != nil {
		return nil, err
	}
	if !created {
		return acknowledgementFromStored(command)
	}
	detail, _, err := m.reader.Get(ctx, instance, jid.String())
	if err != nil {
		m.completeRejected(ctx, command, "projection_not_ready", input.audit)
		return nil, err
	}
	decision := input.decision(detail.Actions)
	if decision.State != "allowed" {
		reason := "permission_unknown"
		if decision.Reason != nil {
			reason = *decision.Reason
		}
		m.completeRejected(ctx, command, reason, input.audit)
		if decision.State == "denied" {
			return nil, ErrManagementPermissionDenied
		}
		return nil, ErrManagementPermissionUnknown
	}
	if err := m.provider.PrepareManagementCommand(ctx, instance.Id); err != nil {
		m.completeRejected(ctx, command, "provider_disconnected", input.audit)
		if managementRateLimited(err) {
			return nil, err
		}
		return nil, ErrManagementProviderNotReady
	}
	if _, err := m.repository.MarkExecuting(ctx, instance.Id, command.ID); err != nil {
		return nil, err
	}
	acknowledgement := CommandAcknowledgement{CommandID: command.ID, Command: input.commandType, GroupJID: jid.String(), Status: "completed", ProjectionRefreshExpected: true}
	if err := input.execute(ctx, instance.Id, jid); err != nil {
		if managementRateLimited(err) {
			m.completeRejected(ctx, command, "rate_limited", input.audit)
			return nil, err
		}
		acknowledgement.Status = "unknown"
		_ = m.complete(ctx, command, group_model.ManagementCommandUnknown, acknowledgement, mergeManagementAudit(input.audit, map[string]any{"reason": "unknown_outcome"}))
		return &acknowledgement, nil
	}
	if err := m.complete(ctx, command, group_model.ManagementCommandCompleted, acknowledgement, input.audit); err != nil {
		acknowledgement.Status = "unknown"
		return &acknowledgement, nil
	}
	return &acknowledgement, nil
}

func managementRateLimited(err error) bool {
	if err == nil {
		return false
	}
	if _, limited := outbound.RetryAfter(err); limited {
		return true
	}
	var queryLimit *waquery.RateLimitError
	return errors.As(err, &queryLimit)
}

func (m *ManagementCommandManager) completeRejected(ctx context.Context, command *group_model.ManagementCommand, reason string, audit map[string]any) {
	ack := CommandAcknowledgement{CommandID: command.ID, Command: command.CommandType, GroupJID: commandGroupJID(command), Status: "unknown", ProjectionRefreshExpected: false}
	_ = m.complete(ctx, command, group_model.ManagementCommandFailed, ack, mergeManagementAudit(audit, map[string]any{"reason": reason}))
}

func (m *ManagementCommandManager) complete(ctx context.Context, command *group_model.ManagementCommand, status group_model.ManagementCommandStatus, acknowledgement CommandAcknowledgement, audit map[string]any) error {
	outcome, err := json.Marshal(acknowledgement)
	if err != nil {
		return err
	}
	if audit == nil {
		audit = map[string]any{}
	}
	summary, err := json.Marshal(audit)
	if err != nil {
		return err
	}
	_, err = m.repository.Complete(ctx, command.InstanceID, command.ID, group_repository.CompleteManagementCommandInput{Status: status, SafeOutcome: outcome, AuditSummary: summary})
	return err
}

func acknowledgementFromStored(command *group_model.ManagementCommand) (*CommandAcknowledgement, error) {
	if command == nil {
		return nil, group_repository.ErrManagementCommandNotFound
	}
	if command.Status == group_model.ManagementCommandRequested || command.Status == group_model.ManagementCommandExecuting {
		return &CommandAcknowledgement{CommandID: command.ID, Command: command.CommandType, GroupJID: commandGroupJID(command), Status: "accepted", ProjectionRefreshExpected: false}, nil
	}
	if command.Status == group_model.ManagementCommandFailed {
		return nil, group_repository.ErrManagementCommandConflict
	}
	var acknowledgement CommandAcknowledgement
	if json.Unmarshal(command.SafeOutcome, &acknowledgement) != nil || uuid.Validate(acknowledgement.CommandID) != nil {
		return nil, group_repository.ErrInvalidManagementCommand
	}
	return &acknowledgement, nil
}

func commandGroupJID(command *group_model.ManagementCommand) string {
	if command == nil || command.GroupJID == nil {
		return ""
	}
	return *command.GroupJID
}

func managementFingerprint(commandType, groupJID string, payload any) (string, error) {
	encoded, err := json.Marshal(struct {
		Command  string `json:"command"`
		GroupJID string `json:"groupJid"`
		Payload  any    `json:"payload"`
	}{commandType, groupJID, payload})
	if err != nil {
		return "", err
	}
	return managementHash(string(encoded)), nil
}

func managementHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func mergeManagementAudit(base, extra map[string]any) map[string]any {
	result := make(map[string]any, len(base)+len(extra))
	for key, value := range base {
		result[key] = value
	}
	for key, value := range extra {
		result[key] = value
	}
	return result
}
