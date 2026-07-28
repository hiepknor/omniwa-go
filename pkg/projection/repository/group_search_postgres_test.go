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

func TestGroupSearchPostgresIsInstanceScopedLiteralAndCursorStable(t *testing.T) {
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
	instances := []instance_model.Instance{{Name: "group-search-a", Token: "group-search-a-token"}, {Name: "group-search-b", Token: "group-search-b-token"}}
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
	apply := func(instanceID, groupID, name string, at int64) {
		t.Helper()
		occurredAt := time.Unix(at, 0).UTC()
		applied, applyErr := repository.ApplySnapshot(context.Background(), &projection_model.Group{
			InstanceID: instanceID, GroupID: groupID, Name: &name, SourceOccurredAt: occurredAt, SourceEventKey: groupID,
		}, []projection_model.GroupParticipant{{ParticipantID: "member@s.whatsapp.net", Role: projection_model.ParticipantRoleMember}})
		if applyErr != nil || !applied {
			t.Fatalf("apply group %s = %v, %v", groupID, applied, applyErr)
		}
	}
	apply(instances[0].Id, "100@g.us", "Alpha Team", 100)
	apply(instances[0].Id, "200@g.us", "Alpine Team", 101)
	apply(instances[0].Id, "300@g.us", "Beta", 102)
	apply(instances[0].Id, "400@g.us", "%literal", 103)
	apply(instances[1].Id, "500@g.us", "Alpha Other", 104)

	first, err := repository.Search(context.Background(), instances[0].Id, "AL", 1, nil)
	if err != nil || len(first.Items) != 1 || first.Items[0].Group.GroupID != "100@g.us" || len(first.Items[0].Participants) != 1 || first.NextCursor == nil {
		t.Fatalf("first search page = %#v, %v", first, err)
	}
	apply(instances[0].Id, "050@g.us", "Alpha New", 105)
	second, err := repository.Search(context.Background(), instances[0].Id, "al", 1, first.NextCursor)
	if err != nil || len(second.Items) != 1 || second.Items[0].Group.GroupID != "200@g.us" || second.NextCursor != nil {
		t.Fatalf("second search page = %#v, %v", second, err)
	}
	literal, err := repository.Search(context.Background(), instances[0].Id, "%", 10, nil)
	if err != nil || len(literal.Items) != 1 || literal.Items[0].Group.GroupID != "400@g.us" {
		t.Fatalf("literal wildcard search = %#v, %v", literal, err)
	}
	other, err := repository.Search(context.Background(), instances[1].Id, "alpha", 10, nil)
	if err != nil || len(other.Items) != 1 || other.Items[0].Group.GroupID != "500@g.us" {
		t.Fatalf("instance-scoped search = %#v, %v", other, err)
	}
	publicID := first.Items[0].Participants[0].PublicID
	if uuid.Validate(publicID) != nil {
		t.Fatalf("generated participant public ID = %q", publicID)
	}
	apply(instances[0].Id, "100@g.us", "Alpha Team Updated", 106)
	_, participants, err := repository.Get(context.Background(), instances[0].Id, "100@g.us")
	if err != nil || len(participants) != 1 || participants[0].PublicID != publicID {
		t.Fatalf("participant public ID changed across snapshot: before=%q after=%+v err=%v", publicID, participants, err)
	}
}

func TestGroupManagementSearchPostgresResolvesAliasesAndKeepsTenantScope(t *testing.T) {
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
		{Name: "group-management-a", Token: uuid.NewString(), Jid: "111@s.whatsapp.net"},
		{Name: "group-management-b", Token: uuid.NewString(), Jid: "222@s.whatsapp.net"},
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
	now := time.Now().UTC()
	contactID := uuid.NewString()
	contact := projection_model.Contact{
		InstanceID: instances[0].Id, ContactID: contactID, PreferredJID: instances[0].Jid, Found: true,
		SourceOccurredAt: now, SourceEventKey: "self", FieldVersions: json.RawMessage(`{}`), LastSyncedAt: now,
	}
	if err := db.Create(&contact).Error; err != nil {
		t.Fatal(err)
	}
	identities := []projection_model.ContactIdentity{
		{InstanceID: instances[0].Id, Kind: projection_model.ContactIdentityKindJID, Value: instances[0].Jid, ContactID: contactID, SourceOccurredAt: now, SourceEventKey: "self-jid", LastSyncedAt: now},
		{InstanceID: instances[0].Id, Kind: projection_model.ContactIdentityKindLID, Value: "actor@lid", ContactID: contactID, SourceOccurredAt: now, SourceEventKey: "self-lid", LastSyncedAt: now},
	}
	if err := db.Create(&identities).Error; err != nil {
		t.Fatal(err)
	}
	repository := NewGroupRepository(db)
	active, announce, isParent := false, true, false
	name := "Alias Group"
	owner := "actor@lid"
	applied, err := repository.ApplySnapshot(context.Background(), &projection_model.Group{
		InstanceID: instances[0].Id, GroupID: "120363009999@g.us", Name: &name, OwnerJID: &owner,
		Suspended: &active, Announce: &announce, IsParent: &isParent, SourceOccurredAt: now, SourceEventKey: "group-a",
	}, []projection_model.GroupParticipant{{ParticipantID: "actor@lid", Role: projection_model.ParticipantRoleAdmin}})
	if err != nil || !applied {
		t.Fatalf("apply group = %t, %v", applied, err)
	}
	otherName := "Other Tenant"
	applied, err = repository.ApplySnapshot(context.Background(), &projection_model.Group{
		InstanceID: instances[1].Id, GroupID: "120363008888@g.us", Name: &otherName,
		Suspended: &active, Announce: &announce, IsParent: &isParent, SourceOccurredAt: now, SourceEventKey: "group-b",
	}, []projection_model.GroupParticipant{{ParticipantID: "actor@lid", Role: projection_model.ParticipantRoleAdmin}})
	if err != nil || !applied {
		t.Fatalf("apply other group = %t, %v", applied, err)
	}
	page, err := repository.SearchManagement(context.Background(), instances[0].Id, instances[0].Jid, GroupManagementFilter{
		Type: "group", MyRole: "owner", State: "active", SendMode: "admins_only", MembershipState: "joined",
	}, 50, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Group.GroupID != "120363009999@g.us" || page.Items[0].ActorParticipant == nil || page.Items[0].ActorParticipant.ParticipantID != "actor@lid" {
		t.Fatalf("management page = %#v", page)
	}
	detail, err := repository.GetManagement(context.Background(), instances[0].Id, instances[0].Jid, "120363009999@g.us")
	if err != nil || detail.OwnerPublicID == nil || uuid.Validate(*detail.OwnerPublicID) != nil || detail.AdminCount == nil || *detail.AdminCount != 1 {
		t.Fatalf("management detail = %#v, %v", detail, err)
	}
	if err := db.Model(&projection_model.Group{}).Where("instance_id = ? AND group_id = ?", instances[0].Id, "120363009999@g.us").
		Update("actor_membership_state", projection_model.GroupActorMembershipLeft).Error; err != nil {
		t.Fatal(err)
	}
	ownerPage, err := repository.SearchManagement(context.Background(), instances[0].Id, instances[0].Jid, GroupManagementFilter{MyRole: "owner"}, 50, nil)
	if err != nil || len(ownerPage.Items) != 0 {
		t.Fatalf("stale owner after explicit leave = %#v, %v", ownerPage, err)
	}
	notMemberPage, err := repository.SearchManagement(context.Background(), instances[0].Id, instances[0].Jid, GroupManagementFilter{MyRole: "not_member", MembershipState: "left"}, 50, nil)
	if err != nil || len(notMemberPage.Items) != 1 || notMemberPage.Items[0].Group.GroupID != "120363009999@g.us" {
		t.Fatalf("explicit left filter = %#v, %v", notMemberPage, err)
	}
}
