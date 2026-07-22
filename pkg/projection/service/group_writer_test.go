package projection_service

import (
	"context"
	"strings"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"go.mau.fi/whatsmeow/types"
)

type groupWriterRepositoryStub struct {
	group        *projection_model.Group
	participants []projection_model.GroupParticipant
	patches      []projection_repository.GroupPatch
	tombstoneID  string
}

func (s *groupWriterRepositoryStub) ApplySnapshot(_ context.Context, group *projection_model.Group, participants []projection_model.GroupParticipant) (bool, error) {
	s.group, s.participants = group, participants
	return true, nil
}

func (s *groupWriterRepositoryStub) ApplyPatch(_ context.Context, patch projection_repository.GroupPatch) (bool, error) {
	s.patches = append(s.patches, patch)
	return true, nil
}

func (s *groupWriterRepositoryStub) Tombstone(_ context.Context, _, groupID, _ string, _ time.Time) (bool, error) {
	s.tombstoneID = groupID
	return true, nil
}

type groupWriterStateStub struct {
	recorded int
	stale    int
}

func (s *groupWriterStateStub) RecordEvent(string, string, int64, time.Time) error {
	s.recorded++
	return nil
}
func (s *groupWriterStateStub) MarkStale(string, string, int64) error { s.stale++; return nil }

func TestGroupWriterWritesConfirmedSnapshot(t *testing.T) {
	repository := &groupWriterRepositoryStub{}
	state := &groupWriterStateStub{}
	writer := NewGroupWriter(repository, state)
	writer.now = func() time.Time { return time.Unix(1000, 0) }
	info := &types.GroupInfo{
		JID: types.NewJID("group", types.GroupServer), GroupName: types.GroupName{Name: "Created"},
		Participants: []types.GroupParticipant{{JID: types.NewJID("admin", types.DefaultUserServer), IsAdmin: true}},
	}
	if err := writer.WriteInfo(context.Background(), "instance-a", info); err != nil {
		t.Fatal(err)
	}
	if repository.group == nil || repository.group.Name == nil || *repository.group.Name != "Created" ||
		!repository.group.SourceOccurredAt.Equal(time.Unix(1000, 0)) || !strings.HasPrefix(repository.group.SourceEventKey, "mutation:") ||
		len(repository.participants) != 1 || repository.participants[0].Role != projection_model.ParticipantRoleAdmin || state.recorded != 1 {
		t.Fatalf("write-through snapshot = %#v, %#v state=%#v", repository.group, repository.participants, state)
	}
}

func TestGroupWriterWritesMutationPatchesAndTombstone(t *testing.T) {
	repository := &groupWriterRepositoryStub{}
	state := &groupWriterStateStub{}
	writer := NewGroupWriter(repository, state)
	writer.now = func() time.Time { return time.Unix(1000, 0) }
	ctx := context.Background()
	if err := writer.WriteName(ctx, "instance-a", "group@g.us", "Renamed"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteTopic(ctx, "instance-a", "group@g.us", ""); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteSetting(ctx, "instance-a", "group@g.us", "member_add", true); err != nil {
		t.Fatal(err)
	}
	participants := []types.GroupParticipant{
		{JID: types.NewJID("ok", types.DefaultUserServer)},
		{JID: types.NewJID("failed", types.DefaultUserServer), Error: 403},
	}
	if err := writer.WriteParticipants(ctx, "instance-a", "group@g.us", "promote", participants); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteInviteLink(ctx, "instance-a", "group@g.us", "https://chat.whatsapp.com/new"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Tombstone(ctx, "instance-a", "group@g.us"); err != nil {
		t.Fatal(err)
	}
	if len(repository.patches) != 5 || repository.patches[0].Name == nil || *repository.patches[0].Name != "Renamed" ||
		repository.patches[1].TopicDeleted == nil || !*repository.patches[1].TopicDeleted ||
		repository.patches[2].MemberAddMode == nil || *repository.patches[2].MemberAddMode != "all_member_add" ||
		len(repository.patches[3].ParticipantChanges) != 1 || repository.patches[3].ParticipantChanges[0].Role != projection_model.ParticipantRoleAdmin ||
		repository.patches[4].InviteLink == nil || repository.tombstoneID != "group@g.us" || state.recorded != 6 {
		t.Fatalf("write-through patches = %#v tombstone=%q state=%#v", repository.patches, repository.tombstoneID, state)
	}
	if err := writer.MarkStale("instance-a"); err != nil || state.stale != 1 {
		t.Fatalf("MarkStale() state=%#v error=%v", state, err)
	}
}
