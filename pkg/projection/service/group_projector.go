package projection_service

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
)

const groupsProjectionSchemaVersion int64 = GroupsProjectionSchemaVersion

type groupSnapshotRepository interface {
	ApplySnapshot(context.Context, *projection_model.Group, []projection_model.GroupParticipant) (bool, error)
	ApplyPatch(context.Context, projection_repository.GroupPatch) (bool, error)
	Tombstone(context.Context, string, string, string, time.Time) (bool, error)
}

type projectionEventState interface {
	RecordEvent(instanceID, resource string, schemaVersion int64, occurredAt time.Time) error
}

type GroupProjector struct {
	groups groupSnapshotRepository
	state  projectionEventState
}

func NewGroupProjector(groups groupSnapshotRepository, state projectionEventState) *GroupProjector {
	return &GroupProjector{groups: groups, state: state}
}

func (p *GroupProjector) Handle(ctx context.Context, event *projection_model.Event) error {
	if p == nil || p.groups == nil || p.state == nil {
		return permanentProjectionFailure(errorCodeMisconfigured)
	}
	if event == nil || event.Resource != groupResource || (event.EventType != "joined_group" && event.EventType != "group_info") || event.InstanceID == "" || event.EventKey == "" {
		return permanentProjectionFailure(errorCodeUnsupportedEvent)
	}
	var payload groupEventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return permanentProjectionFailure(errorCodeInvalidPayload)
	}
	if payload.GroupID == "" || payload.GroupID != event.EntityKey || (event.EventType == "joined_group" && payload.Joined == nil) {
		return permanentProjectionFailure(errorCodeIdentityMismatch)
	}
	if event.EventType == "joined_group" {
		group := groupFromSnapshot(event, &payload)
		participants := participantsFromSnapshot(&payload)
		if _, err := p.groups.ApplySnapshot(ctx, group, participants); err != nil {
			return err
		}
	} else if payload.Deleted != nil && payload.Deleted.Deleted {
		if _, err := p.groups.Tombstone(ctx, event.InstanceID, payload.GroupID, event.EventKey, event.OccurredAt); err != nil {
			return err
		}
	} else {
		if _, err := p.groups.ApplyPatch(ctx, patchFromGroupEvent(event, &payload)); err != nil {
			return err
		}
	}
	return p.state.RecordEvent(event.InstanceID, groupResource, groupsProjectionSchemaVersion, event.OccurredAt)
}

func patchFromGroupEvent(event *projection_model.Event, payload *groupEventPayload) projection_repository.GroupPatch {
	patch := projection_repository.GroupPatch{
		InstanceID: event.InstanceID, GroupID: payload.GroupID, EventKey: event.EventKey, OccurredAt: event.OccurredAt,
		Locked: boolPointerValue(payload.Locked), JoinApprovalRequired: boolPointerValue(payload.JoinApprovalRequired),
		ParticipantChanges: participantChangesFromGroupEvent(payload),
	}
	if payload.Name != nil {
		patch.Name = &payload.Name.Name
		patch.NameSetAt = optionalTime(payload.Name.SetAt)
		patch.NameSetBy = optionalString(payload.Name.SetBy)
		patch.NameSetByPhone = optionalString(payload.Name.SetByPN)
	}
	if payload.Topic != nil {
		patch.Topic = &payload.Topic.Topic
		patch.TopicID = optionalString(payload.Topic.ID)
		patch.TopicSetAt = optionalTime(payload.Topic.SetAt)
		patch.TopicSetBy = optionalString(payload.Topic.SetBy)
		patch.TopicSetByPhone = optionalString(payload.Topic.SetByPN)
		patch.TopicDeleted = boolPointer(payload.Topic.Deleted)
	}
	if payload.Announce != nil {
		patch.Announce = boolPointer(payload.Announce.Enabled)
		patch.AnnounceVersion = optionalString(payload.Announce.VersionID)
	}
	if payload.Ephemeral != nil {
		patch.EphemeralEnabled = boolPointer(payload.Ephemeral.Enabled)
		timer := int64(payload.Ephemeral.Timer)
		patch.EphemeralTimer = &timer
	}
	if payload.Suspended {
		patch.Suspended = boolPointer(true)
	} else if payload.Unsuspended {
		patch.Suspended = boolPointer(false)
	}
	if payload.ParticipantVersion != "" {
		patch.ParticipantVersion = &payload.ParticipantVersion
	}
	if payload.NewInviteLink != nil {
		patch.InviteLink = payload.NewInviteLink
	}
	if payload.Link != nil {
		patch.ParentGroupID = &payload.Link.GroupID
		patch.IsDefaultSubgroup = boolPointer(payload.Link.IsDefaultSubgroup)
	} else if payload.Unlink != nil {
		empty := ""
		patch.ParentGroupID = &empty
		patch.IsDefaultSubgroup = boolPointer(false)
	}
	return patch
}

func participantChangesFromGroupEvent(payload *groupEventPayload) []projection_repository.GroupParticipantPatch {
	changes := make(map[string]projection_repository.GroupParticipantPatch)
	for _, participantID := range payload.JoinedParticipants {
		changes[participantID] = projection_repository.GroupParticipantPatch{ParticipantID: participantID, Role: projection_model.ParticipantRoleMember}
	}
	for _, participantID := range payload.PromotedParticipants {
		changes[participantID] = projection_repository.GroupParticipantPatch{ParticipantID: participantID, Role: projection_model.ParticipantRoleAdmin}
	}
	for _, participantID := range payload.DemotedParticipants {
		changes[participantID] = projection_repository.GroupParticipantPatch{ParticipantID: participantID, Role: projection_model.ParticipantRoleMember}
	}
	for _, participantID := range payload.LeftParticipants {
		changes[participantID] = projection_repository.GroupParticipantPatch{ParticipantID: participantID, Role: projection_model.ParticipantRoleMember, Tombstone: true}
	}
	result := make([]projection_repository.GroupParticipantPatch, 0, len(changes))
	for _, change := range changes {
		result = append(result, change)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ParticipantID < result[right].ParticipantID })
	return result
}

func groupFromSnapshot(event *projection_model.Event, payload *groupEventPayload) *projection_model.Group {
	group := &projection_model.Group{
		InstanceID: event.InstanceID, GroupID: payload.GroupID, SourceOccurredAt: event.OccurredAt, SourceEventKey: event.EventKey,
		OwnerJID: optionalString(payload.Owner), OwnerPhoneJID: optionalString(payload.OwnerPN),
		Locked: boolPointerValue(payload.Locked), Announce: optionalAnnounce(payload.Announce),
		JoinApprovalRequired: boolPointerValue(payload.JoinApprovalRequired), Suspended: boolPointer(payload.Suspended),
		ParticipantVersion: optionalString(payload.ParticipantVersion), AddressingMode: optionalString(payload.AddressingMode),
		MemberAddMode: optionalString(payload.MemberAddMode), ParentGroupID: optionalString(payload.LinkedParentID),
		IsParent: boolPointer(payload.IsParent), IsDefaultSubgroup: boolPointer(payload.IsDefaultSubgroup),
		Incognito: boolPointer(payload.Incognito), CreatorCountryCode: optionalString(payload.CreatorCountryCode),
		ParticipantCount: intPointer(payload.ParticipantCount), DefaultApprovalMode: optionalString(payload.DefaultMembershipApproval),
	}
	if payload.Name != nil {
		group.Name = &payload.Name.Name
		group.NameSetAt = optionalTime(payload.Name.SetAt)
		group.NameSetBy = optionalString(payload.Name.SetBy)
		group.NameSetByPhone = optionalString(payload.Name.SetByPN)
	}
	if payload.Topic != nil {
		group.Topic = &payload.Topic.Topic
		group.TopicID = optionalString(payload.Topic.ID)
		group.TopicSetAt = optionalTime(payload.Topic.SetAt)
		group.TopicSetBy = optionalString(payload.Topic.SetBy)
		group.TopicSetByPhone = optionalString(payload.Topic.SetByPN)
		group.TopicDeleted = boolPointer(payload.Topic.Deleted)
	}
	if payload.Announce != nil {
		group.AnnounceVersion = optionalString(payload.Announce.VersionID)
	}
	if payload.Ephemeral != nil {
		group.EphemeralEnabled = boolPointer(payload.Ephemeral.Enabled)
		timer := int64(payload.Ephemeral.Timer)
		group.EphemeralTimer = &timer
	}
	if !payload.CreatedAt.IsZero() {
		createdAt := payload.CreatedAt.UTC()
		group.ProviderCreatedAt = &createdAt
	}
	return group
}

func participantsFromSnapshot(payload *groupEventPayload) []projection_model.GroupParticipant {
	participants := make([]projection_model.GroupParticipant, 0, len(payload.Participants))
	for _, item := range payload.Participants {
		if item.ID == "" || item.ErrorCode != 0 {
			continue
		}
		role := projection_model.ParticipantRoleMember
		if item.SuperAdmin {
			role = projection_model.ParticipantRoleSuperAdmin
		} else if item.Admin {
			role = projection_model.ParticipantRoleAdmin
		}
		participants = append(participants, projection_model.GroupParticipant{
			ParticipantID: item.ID, PhoneNumberJID: optionalString(item.PhoneNumber), LID: optionalString(item.LID),
			DisplayName: optionalString(item.DisplayName), Role: role,
		})
	}
	return participants
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func boolPointer(value bool) *bool { return &value }

func intPointer(value int) *int { return &value }

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	return &value
}

func boolPointerValue(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func optionalAnnounce(value *groupAnnouncePayload) *bool {
	if value == nil {
		return nil
	}
	return boolPointer(value.Enabled)
}
