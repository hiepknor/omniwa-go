package projection_repository

import (
	"context"
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

func TestGroupMembersPostgresPaginationRoleSearchAliasesAndTenantScope(t *testing.T) {
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
	instances := []instance_model.Instance{
		{Name: "group-members-a", Token: uuid.NewString(), Jid: "111@s.whatsapp.net"},
		{Name: "group-members-b", Token: uuid.NewString(), Jid: "222@s.whatsapp.net"},
	}
	for index := range instances {
		if err := db.Create(&instances[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for index := range instances {
			_ = db.Delete(&instances[index]).Error
		}
	})
	repository := NewGroupRepository(db)
	now := time.Now().UTC()
	suspended, announce, isParent := false, false, false
	membership := projection_model.GroupActorMembershipJoined
	name, owner := "Members", "actor@lid"
	actorPhone := instances[0].Jid
	actorName, aliceName, bobName, superName := "Owner", "Alice", "Bob", "Super"
	participants := []projection_model.GroupParticipant{
		{ParticipantID: "actor@lid", PhoneNumberJID: &actorPhone, DisplayName: &actorName, Role: projection_model.ParticipantRoleSuperAdmin},
		{ParticipantID: "alice@s.whatsapp.net", DisplayName: &aliceName, Role: projection_model.ParticipantRoleMember},
		{ParticipantID: "bob@s.whatsapp.net", DisplayName: &bobName, Role: projection_model.ParticipantRoleAdmin},
		{ParticipantID: "super@s.whatsapp.net", DisplayName: &superName, Role: projection_model.ParticipantRoleSuperAdmin},
	}
	applied, err := repository.ApplySnapshot(context.Background(), &projection_model.Group{
		InstanceID: instances[0].Id, GroupID: "123@g.us", Name: &name, OwnerJID: &owner, Suspended: &suspended,
		Announce: &announce, IsParent: &isParent, MembershipState: &membership, SourceOccurredAt: now, SourceEventKey: "members-a",
	}, participants)
	if err != nil || !applied {
		t.Fatalf("apply members group = %t, %v", applied, err)
	}
	otherName := "Other"
	applied, err = repository.ApplySnapshot(context.Background(), &projection_model.Group{
		InstanceID: instances[1].Id, GroupID: "123@g.us", Name: &otherName, Suspended: &suspended,
		Announce: &announce, IsParent: &isParent, MembershipState: &membership, SourceOccurredAt: now, SourceEventKey: "members-b",
	}, []projection_model.GroupParticipant{{ParticipantID: "other@s.whatsapp.net", DisplayName: &aliceName, Role: projection_model.ParticipantRoleMember}})
	if err != nil || !applied {
		t.Fatalf("apply other tenant group = %t, %v", applied, err)
	}
	group, page, err := repository.ListManagementMembers(context.Background(), instances[0].Id, instances[0].Jid, "123@g.us", GroupMemberFilter{Term: "ALI", Role: "member"}, 50, nil)
	if err != nil {
		t.Fatal(err)
	}
	if group == nil || !group.ActorIsOwner || len(page.Items) != 1 || page.Items[0].Participant.DisplayName == nil || *page.Items[0].Participant.DisplayName != "Alice" || page.Items[0].Role != "member" || page.Items[0].IsActor || uuid.Validate(page.Items[0].Participant.PublicID) != nil {
		t.Fatalf("member search group=%#v page=%#v", group, page)
	}
	_, ownerPage, err := repository.ListManagementMembers(context.Background(), instances[0].Id, instances[0].Jid, "123@g.us", GroupMemberFilter{Role: "owner"}, 50, nil)
	if err != nil || len(ownerPage.Items) != 1 || ownerPage.Items[0].Role != "owner" || !ownerPage.Items[0].IsActor {
		t.Fatalf("owner page=%#v err=%v", ownerPage, err)
	}
	_, first, err := repository.ListManagementMembers(context.Background(), instances[0].Id, instances[0].Jid, "123@g.us", GroupMemberFilter{}, 1, nil)
	if err != nil || len(first.Items) != 1 || first.NextCursor == nil {
		t.Fatalf("first page=%#v err=%v", first, err)
	}
	_, second, err := repository.ListManagementMembers(context.Background(), instances[0].Id, instances[0].Jid, "123@g.us", GroupMemberFilter{}, 1, first.NextCursor)
	if err != nil || len(second.Items) != 1 || second.Items[0].Participant.PublicID == first.Items[0].Participant.PublicID {
		t.Fatalf("second page=%#v err=%v", second, err)
	}
	_, otherPage, err := repository.ListManagementMembers(context.Background(), instances[1].Id, instances[1].Jid, "123@g.us", GroupMemberFilter{}, 50, nil)
	if err != nil || len(otherPage.Items) != 1 || *otherPage.Items[0].Participant.DisplayName != "Alice" || otherPage.Items[0].Participant.ParticipantID != "other@s.whatsapp.net" {
		t.Fatalf("other tenant page=%#v err=%v", otherPage, err)
	}
}
