package projection_service

import (
	"context"
	"errors"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type groupWriteRepository interface {
	ApplySnapshot(context.Context, *projection_model.Group, []projection_model.GroupParticipant) (bool, error)
	ApplyPatch(context.Context, projection_repository.GroupPatch) (bool, error)
	Tombstone(context.Context, string, string, string, time.Time) (bool, error)
}

type groupWriteState interface {
	RecordEvent(instanceID, resource string, schemaVersion int64, occurredAt time.Time) error
	MarkStale(instanceID, resource string, schemaVersion int64) error
}

type GroupWriter struct {
	groups groupWriteRepository
	state  groupWriteState
	now    func() time.Time
}

func NewGroupWriter(groups groupWriteRepository, state groupWriteState) *GroupWriter {
	return &GroupWriter{groups: groups, state: state, now: time.Now}
}

func (w *GroupWriter) WriteInfo(ctx context.Context, instanceID string, info *types.GroupInfo) error {
	if err := w.validate(instanceID); err != nil || info == nil || info.JID.IsEmpty() {
		if err != nil {
			return err
		}
		return errors.New("confirmed group info is required")
	}
	version := w.version()
	payload := normalizeJoinedGroup(&events.JoinedGroup{GroupInfo: *info})
	event := &projection_model.Event{InstanceID: instanceID, EntityKey: info.JID.String(), EventKey: version.key, Resource: groupResource, EventType: "mutation", OccurredAt: version.at}
	if _, err := w.groups.ApplySnapshot(ctx, groupFromSnapshot(event, &payload), participantsFromSnapshot(&payload)); err != nil {
		return err
	}
	return w.record(instanceID, version.at)
}

func (w *GroupWriter) WriteName(ctx context.Context, instanceID, groupID, name string) error {
	version, err := w.patch(instanceID, groupID)
	if err != nil {
		return err
	}
	version.patch.Name = &name
	version.patch.NameSetAt = &version.at
	return w.applyPatch(ctx, version)
}

func (w *GroupWriter) WriteTopic(ctx context.Context, instanceID, groupID, topic string) error {
	version, err := w.patch(instanceID, groupID)
	if err != nil {
		return err
	}
	version.patch.Topic = &topic
	version.patch.TopicSetAt = &version.at
	deleted := topic == ""
	version.patch.TopicDeleted = &deleted
	return w.applyPatch(ctx, version)
}

func (w *GroupWriter) WriteSetting(ctx context.Context, instanceID, groupID, setting string, enabled bool) error {
	version, err := w.patch(instanceID, groupID)
	if err != nil {
		return err
	}
	switch setting {
	case "announce":
		version.patch.Announce = &enabled
	case "locked":
		version.patch.Locked = &enabled
	case "join_approval":
		version.patch.JoinApprovalRequired = &enabled
	case "member_add":
		mode := "admin_add"
		if enabled {
			mode = "all_member_add"
		}
		version.patch.MemberAddMode = &mode
	default:
		return errors.New("unsupported group projection setting")
	}
	return w.applyPatch(ctx, version)
}

func (w *GroupWriter) WriteParticipants(ctx context.Context, instanceID, groupID, action string, participants []types.GroupParticipant) error {
	version, err := w.patch(instanceID, groupID)
	if err != nil {
		return err
	}
	for _, participant := range participants {
		if participant.Error != 0 || participant.JID.IsEmpty() {
			continue
		}
		change := projection_repository.GroupParticipantPatch{ParticipantID: participant.JID.String(), Role: projection_model.ParticipantRoleMember}
		switch action {
		case "add", "demote":
		case "promote":
			change.Role = projection_model.ParticipantRoleAdmin
		case "remove":
			change.Tombstone = true
		default:
			return errors.New("unsupported group participant mutation")
		}
		version.patch.ParticipantChanges = append(version.patch.ParticipantChanges, change)
	}
	if len(version.patch.ParticipantChanges) == 0 {
		return nil
	}
	return w.applyPatch(ctx, version)
}

func (w *GroupWriter) WriteInviteLink(ctx context.Context, instanceID, groupID, inviteLink string) error {
	version, err := w.patch(instanceID, groupID)
	if err != nil {
		return err
	}
	version.patch.InviteLink = &inviteLink
	return w.applyPatch(ctx, version)
}

func (w *GroupWriter) Tombstone(ctx context.Context, instanceID, groupID string) error {
	if err := w.validate(instanceID); err != nil || groupID == "" {
		if err != nil {
			return err
		}
		return errors.New("group identity is required")
	}
	version := w.version()
	if _, err := w.groups.Tombstone(ctx, instanceID, groupID, version.key, version.at); err != nil {
		return err
	}
	return w.record(instanceID, version.at)
}

func (w *GroupWriter) MarkStale(instanceID string) error {
	if err := w.validate(instanceID); err != nil {
		return err
	}
	return w.state.MarkStale(instanceID, groupResource, GroupsProjectionSchemaVersion)
}

type mutationVersion struct {
	at    time.Time
	key   string
	patch projection_repository.GroupPatch
}

func (w *GroupWriter) patch(instanceID, groupID string) (mutationVersion, error) {
	if err := w.validate(instanceID); err != nil || groupID == "" {
		if err != nil {
			return mutationVersion{}, err
		}
		return mutationVersion{}, errors.New("group identity is required")
	}
	version := w.version()
	version.patch = projection_repository.GroupPatch{InstanceID: instanceID, GroupID: groupID, EventKey: version.key, OccurredAt: version.at}
	return version, nil
}

func (w *GroupWriter) applyPatch(ctx context.Context, version mutationVersion) error {
	if _, err := w.groups.ApplyPatch(ctx, version.patch); err != nil {
		return err
	}
	return w.record(version.patch.InstanceID, version.at)
}

func (w *GroupWriter) record(instanceID string, occurredAt time.Time) error {
	return w.state.RecordEvent(instanceID, groupResource, GroupsProjectionSchemaVersion, occurredAt)
}

func (w *GroupWriter) validate(instanceID string) error {
	if w == nil || w.groups == nil || w.state == nil || w.now == nil || instanceID == "" {
		return errors.New("group projection writer dependencies and instance identity are required")
	}
	return nil
}

func (w *GroupWriter) version() mutationVersion {
	return mutationVersion{at: w.now().UTC(), key: "mutation:" + uuid.NewString()}
}
