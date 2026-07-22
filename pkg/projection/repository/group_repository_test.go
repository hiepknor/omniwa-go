package projection_repository

import (
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
)

func TestValidateGroupSnapshotRejectsInvalidAndDuplicateParticipants(t *testing.T) {
	group := &projection_model.Group{InstanceID: "instance-a", GroupID: "group@g.us", SourceOccurredAt: time.Now(), SourceEventKey: "event-1"}
	participants := []projection_model.GroupParticipant{{ParticipantID: "user@s.whatsapp.net", Role: projection_model.ParticipantRoleMember}}
	if err := validateGroupSnapshot(group, participants); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
	participants = append(participants, participants[0])
	if err := validateGroupSnapshot(group, participants); err == nil {
		t.Fatal("duplicate participant accepted")
	}
	participants = []projection_model.GroupParticipant{{ParticipantID: "user@s.whatsapp.net", Role: "owner"}}
	if err := validateGroupSnapshot(group, participants); err == nil {
		t.Fatal("invalid participant role accepted")
	}
}
