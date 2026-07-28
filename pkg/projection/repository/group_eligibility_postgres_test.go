package projection_repository

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestGetForEligibilityLoadsOnlyMatchingInstanceIdentity(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&instance_model.Instance{}); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Run(db); err != nil {
		t.Fatal(err)
	}
	instance := instance_model.Instance{Name: "eligibility-identity-" + uuid.NewString(), Token: uuid.NewString(), Jid: "5511999999999@s.whatsapp.net"}
	if err := db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Delete(&instance).Error })
	now := time.Now().UTC()
	announce, suspended := false, false
	groups := []projection_model.Group{
		{InstanceID: instance.Id, GroupID: "120363000001@g.us", Announce: &announce, Suspended: &suspended, SourceOccurredAt: now, SourceEventKey: "event-1", FieldVersions: json.RawMessage(`{}`), LastSyncedAt: now},
		{InstanceID: instance.Id, GroupID: "120363000002@g.us", Announce: &announce, Suspended: &suspended, SourceOccurredAt: now, SourceEventKey: "event-2", FieldVersions: json.RawMessage(`{}`), LastSyncedAt: now},
		{InstanceID: instance.Id, GroupID: "120363000003@g.us", Announce: &announce, Suspended: &suspended, SourceOccurredAt: now, SourceEventKey: "event-3", FieldVersions: json.RawMessage(`{}`), LastSyncedAt: now},
	}
	if err := db.Create(&groups).Error; err != nil {
		t.Fatal(err)
	}
	phoneIdentity, lidIdentity := "5511999999999@s.whatsapp.net", "9999999999999@lid"
	participants := []projection_model.GroupParticipant{
		{InstanceID: instance.Id, GroupID: groups[0].GroupID, ParticipantID: phoneIdentity, Role: projection_model.ParticipantRoleMember, SourceOccurredAt: now, SourceEventKey: "participant-1", LastSyncedAt: now},
		{InstanceID: instance.Id, GroupID: groups[1].GroupID, ParticipantID: lidIdentity, PhoneNumberJID: &phoneIdentity, Role: projection_model.ParticipantRoleMember, SourceOccurredAt: now, SourceEventKey: "participant-2", LastSyncedAt: now},
		{InstanceID: instance.Id, GroupID: groups[2].GroupID, ParticipantID: "opaque:3", LID: &lidIdentity, Role: projection_model.ParticipantRoleMember, SourceOccurredAt: now, SourceEventKey: "participant-3", LastSyncedAt: now},
		{InstanceID: instance.Id, GroupID: groups[0].GroupID, ParticipantID: "unrelated@s.whatsapp.net", Role: projection_model.ParticipantRoleAdmin, SourceOccurredAt: now, SourceEventKey: "participant-4", LastSyncedAt: now},
	}
	if err := db.Create(&participants).Error; err != nil {
		t.Fatal(err)
	}
	repository := NewGroupRepository(db)
	for _, test := range []struct {
		identity string
		groupID  string
	}{
		{identity: phoneIdentity, groupID: groups[0].GroupID},
		{identity: phoneIdentity, groupID: groups[1].GroupID},
		{identity: lidIdentity, groupID: groups[2].GroupID},
	} {
		records, err := repository.GetForEligibility(context.Background(), instance.Id, test.identity, []string{test.groupID})
		if err != nil || len(records) != 1 || len(records[0].Participants) != 1 {
			t.Fatalf("identity=%s records=%+v err=%v", test.identity, records, err)
		}
	}
	records, err := repository.GetForEligibility(context.Background(), instance.Id, phoneIdentity, []string{groups[0].GroupID, groups[1].GroupID, groups[2].GroupID})
	if err != nil || len(records) != 3 || len(records[0].Participants) != 1 || len(records[1].Participants) != 1 || len(records[2].Participants) != 0 {
		t.Fatalf("bounded records=%+v err=%v", records, err)
	}
}
