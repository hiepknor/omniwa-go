package projection_service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
)

const groupsProjectionSchemaVersion int64 = 1

type groupSnapshotRepository interface {
	ApplySnapshot(context.Context, *projection_model.Group, []projection_model.GroupParticipant) (bool, error)
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
		return errors.New("group projector dependencies are required")
	}
	if event == nil || event.Resource != groupResource || event.EventType != "joined_group" || event.InstanceID == "" || event.EventKey == "" {
		return errors.New("unsupported group projection event")
	}
	var payload groupEventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return errors.New("invalid normalized group projection payload")
	}
	if payload.GroupID == "" || payload.GroupID != event.EntityKey || payload.Joined == nil {
		return errors.New("group projection payload identity mismatch")
	}
	group := groupFromSnapshot(event, &payload)
	participants := participantsFromSnapshot(&payload)
	if _, err := p.groups.ApplySnapshot(ctx, group, participants); err != nil {
		return err
	}
	return p.state.RecordEvent(event.InstanceID, groupResource, groupsProjectionSchemaVersion, event.OccurredAt)
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
	}
	if payload.Name != nil {
		group.Name = &payload.Name.Name
	}
	if payload.Topic != nil {
		group.Topic = &payload.Topic.Topic
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
