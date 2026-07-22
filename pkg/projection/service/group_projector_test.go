package projection_service

import (
	"context"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type captureGroupSnapshots struct {
	group        *projection_model.Group
	participants []projection_model.GroupParticipant
	patch        *projection_repository.GroupPatch
	tombstoned   bool
}

func (c *captureGroupSnapshots) ApplyPatch(_ context.Context, patch projection_repository.GroupPatch) (bool, error) {
	c.patch = &patch
	return true, nil
}

func (c *captureGroupSnapshots) Tombstone(context.Context, string, string, string, time.Time) (bool, error) {
	c.tombstoned = true
	return true, nil
}

func (c *captureGroupSnapshots) ApplySnapshot(_ context.Context, group *projection_model.Group, participants []projection_model.GroupParticipant) (bool, error) {
	c.group = group
	c.participants = participants
	return true, nil
}

type captureProjectionState struct {
	instanceID string
	resource   string
	version    int64
	occurredAt time.Time
}

func (c *captureProjectionState) RecordEvent(instanceID, resource string, version int64, occurredAt time.Time) error {
	c.instanceID, c.resource, c.version, c.occurredAt = instanceID, resource, version, occurredAt
	return nil
}

func TestGroupProjectorAppliesJoinedSnapshotAndRecordsState(t *testing.T) {
	occurredAt := time.Unix(500, 0)
	nameSetAt := time.Unix(400, 0)
	topicSetAt := time.Unix(450, 0)
	raw := &events.JoinedGroup{GroupInfo: types.GroupInfo{
		JID: types.NewJID("group", types.GroupServer), GroupCreated: occurredAt,
		GroupName:          types.GroupName{Name: "Test group", NameSetAt: nameSetAt, NameSetBy: types.NewJID("owner", types.DefaultUserServer)},
		GroupTopic:         types.GroupTopic{Topic: "Description", TopicID: "topic-1", TopicSetAt: topicSetAt, TopicDeleted: true},
		GroupAnnounce:      types.GroupAnnounce{IsAnnounce: true, AnnounceVersionID: "announce-v2"},
		GroupIncognito:     types.GroupIncognito{IsIncognito: true},
		CreatorCountryCode: "84", ParticipantCount: 3, GroupParent: types.GroupParent{DefaultMembershipApprovalMode: "request_required"},
		Participants: []types.GroupParticipant{
			{JID: types.NewJID("admin", types.DefaultUserServer), IsAdmin: true},
			{JID: types.NewJID("owner", types.DefaultUserServer), IsSuperAdmin: true},
			{JID: types.NewJID("failed", types.DefaultUserServer), Error: 403},
		},
	}}
	event, _, err := NormalizeGroupEvent("instance-a", raw)
	if err != nil {
		t.Fatal(err)
	}
	groups := &captureGroupSnapshots{}
	state := &captureProjectionState{}
	projector := NewGroupProjector(groups, state)
	if err := projector.Handle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if groups.group == nil || groups.group.GroupID != "group@g.us" || groups.group.SourceEventKey != event.EventKey || groups.group.Name == nil || *groups.group.Name != "Test group" {
		t.Fatalf("unexpected projected group: %#v", groups.group)
	}
	if groups.group.NameSetAt == nil || !groups.group.NameSetAt.Equal(nameSetAt) || groups.group.NameSetBy == nil || *groups.group.NameSetBy != "owner@s.whatsapp.net" ||
		groups.group.TopicID == nil || *groups.group.TopicID != "topic-1" || groups.group.TopicSetAt == nil || !groups.group.TopicSetAt.Equal(topicSetAt) ||
		groups.group.TopicDeleted == nil || !*groups.group.TopicDeleted || groups.group.AnnounceVersion == nil || *groups.group.AnnounceVersion != "announce-v2" ||
		groups.group.Incognito == nil || !*groups.group.Incognito || groups.group.CreatorCountryCode == nil || *groups.group.CreatorCountryCode != "84" ||
		groups.group.ParticipantCount == nil || *groups.group.ParticipantCount != 3 || groups.group.DefaultApprovalMode == nil || *groups.group.DefaultApprovalMode != "request_required" {
		t.Fatalf("projected group lost read-model metadata: %#v", groups.group)
	}
	if len(groups.participants) != 2 || groups.participants[0].Role != projection_model.ParticipantRoleAdmin || groups.participants[1].Role != projection_model.ParticipantRoleSuperAdmin {
		t.Fatalf("unexpected projected participants: %#v", groups.participants)
	}
	if state.instanceID != "instance-a" || state.resource != groupResource || state.version != groupsProjectionSchemaVersion || !state.occurredAt.Equal(occurredAt) {
		t.Fatalf("projection state was not recorded: %#v", state)
	}
}

func TestGroupProjectorMapsGroupDelta(t *testing.T) {
	nameSetAt := time.Unix(650, 0)
	name := &types.GroupName{Name: "Renamed", NameSetAt: nameSetAt, NameSetBy: types.NewJID("admin", types.DefaultUserServer)}
	topicSetAt := time.Unix(660, 0)
	topic := &types.GroupTopic{Topic: "Updated", TopicID: "topic-2", TopicSetAt: topicSetAt, TopicDeleted: false}
	announce := &types.GroupAnnounce{IsAnnounce: true, AnnounceVersionID: "announce-v3"}
	raw := &events.GroupInfo{
		JID: types.NewJID("group", types.GroupServer), Timestamp: time.Unix(700, 0), Name: name, Topic: topic, Announce: announce,
		Join:    []types.JID{types.NewJID("user", types.DefaultUserServer)},
		Promote: []types.JID{types.NewJID("user", types.DefaultUserServer)},
	}
	event, _, err := NormalizeGroupEvent("instance-a", raw)
	if err != nil {
		t.Fatal(err)
	}
	groups := &captureGroupSnapshots{}
	projector := NewGroupProjector(groups, &captureProjectionState{})
	if err := projector.Handle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if groups.patch == nil || groups.patch.Name == nil || *groups.patch.Name != "Renamed" || len(groups.patch.ParticipantChanges) != 1 || groups.patch.ParticipantChanges[0].Role != projection_model.ParticipantRoleAdmin {
		t.Fatalf("unexpected group patch: %#v", groups.patch)
	}
	if groups.patch.NameSetAt == nil || !groups.patch.NameSetAt.Equal(nameSetAt) || groups.patch.NameSetBy == nil || *groups.patch.NameSetBy != "admin@s.whatsapp.net" ||
		groups.patch.TopicID == nil || *groups.patch.TopicID != "topic-2" || groups.patch.TopicSetAt == nil || !groups.patch.TopicSetAt.Equal(topicSetAt) ||
		groups.patch.TopicDeleted == nil || *groups.patch.TopicDeleted || groups.patch.AnnounceVersion == nil || *groups.patch.AnnounceVersion != "announce-v3" {
		t.Fatalf("group patch lost read-model metadata: %#v", groups.patch)
	}
}
