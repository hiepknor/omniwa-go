package projection_repository

import (
	"context"
	"os"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestContactSearchPostgresIsInstanceScopedLiteralAndCursorStable(t *testing.T) {
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
		{Name: "contact-search-a", Token: "contact-search-a-token"},
		{Name: "contact-search-b", Token: "contact-search-b-token"},
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

	repository := NewContactRepository(db)
	apply := func(instanceID, jid, fullName string, at int64) {
		t.Helper()
		if _, applied, applyErr := repository.Apply(context.Background(), ContactPatch{
			InstanceID: instanceID,
			Identities: []ContactIdentityRef{{Kind: projection_model.ContactIdentityKindJID, Value: jid}},
			Aspect:     ContactAspectDetails, OccurredAt: time.Unix(at, 0).UTC(), EventKey: jid, FullName: &fullName,
		}); applyErr != nil || !applied {
			t.Fatalf("apply contact %s = %v, %v", jid, applied, applyErr)
		}
	}
	apply(instances[0].Id, "alice@s.whatsapp.net", "Alice Adams", 100)
	apply(instances[0].Id, "alicia@s.whatsapp.net", "Alicia Stone", 101)
	apply(instances[0].Id, "bob@s.whatsapp.net", "Bob", 102)
	apply(instances[0].Id, "literal@s.whatsapp.net", "%literal", 103)
	apply(instances[1].Id, "alice-other@s.whatsapp.net", "Alice Other", 104)
	apply(instances[0].Id, "same-name-a@s.whatsapp.net", "Same Name", 106)
	apply(instances[0].Id, "same-name-b@s.whatsapp.net", "Same Name", 107)
	sameNames, err := repository.Search(context.Background(), instances[0].Id, "same name", 10, nil)
	if err != nil || sameNames.Total != 2 || len(sameNames.Items) != 2 || sameNames.Items[0].ContactID == sameNames.Items[1].ContactID {
		t.Fatalf("same display names merged = %#v, %v", sameNames, err)
	}
	sharedAlias := "shared@s.whatsapp.net"
	apply(instances[0].Id, sharedAlias, "Shared A", 108)
	apply(instances[1].Id, sharedAlias, "Shared B", 109)
	sharedA, err := repository.Search(context.Background(), instances[0].Id, sharedAlias, 10, nil)
	if err != nil || len(sharedA.Items) != 1 {
		t.Fatalf("instance A shared alias = %#v, %v", sharedA, err)
	}
	sharedB, err := repository.Search(context.Background(), instances[1].Id, sharedAlias, 10, nil)
	if err != nil || len(sharedB.Items) != 1 || sharedA.Items[0].ContactID == sharedB.Items[0].ContactID {
		t.Fatalf("cross-instance alias merged = %#v/%#v, %v", sharedA, sharedB, err)
	}

	first, err := repository.Search(context.Background(), instances[0].Id, "ALI", 1, nil)
	if err != nil || len(first.Items) != 1 || first.Items[0].PreferredJID != "alice@s.whatsapp.net" || first.NextCursor == nil || first.Total != 2 {
		t.Fatalf("first search page = %#v, %v", first, err)
	}
	apply(instances[0].Id, "aaron@s.whatsapp.net", "Aaron", 105)
	second, err := repository.Search(context.Background(), instances[0].Id, "ali", 1, first.NextCursor)
	if err != nil || len(second.Items) != 1 || second.Items[0].PreferredJID != "alicia@s.whatsapp.net" || second.NextCursor != nil || second.Total != first.Total {
		t.Fatalf("second search page = %#v, %v", second, err)
	}

	literal, err := repository.Search(context.Background(), instances[0].Id, "%", 10, nil)
	if err != nil || len(literal.Items) != 1 || literal.Items[0].PreferredJID != "literal@s.whatsapp.net" {
		t.Fatalf("literal wildcard search = %#v, %v", literal, err)
	}
	other, err := repository.Search(context.Background(), instances[1].Id, "ali", 10, nil)
	if err != nil || len(other.Items) != 1 || other.Items[0].PreferredJID != "alice-other@s.whatsapp.net" {
		t.Fatalf("instance-scoped search = %#v, %v", other, err)
	}

	phone, lid := "15551239999@s.whatsapp.net", "900000009999@lid"
	canonical, _, err := repository.Apply(context.Background(), ContactPatch{
		InstanceID: instances[0].Id,
		Identities: []ContactIdentityRef{
			{Kind: projection_model.ContactIdentityKindJID, Value: phone},
			{Kind: projection_model.ContactIdentityKindPhoneJID, Value: phone},
			{Kind: projection_model.ContactIdentityKindJID, Value: lid},
			{Kind: projection_model.ContactIdentityKindLID, Value: lid},
		},
		Aspect: ContactAspectDetails, OccurredAt: time.Unix(110, 0).UTC(), EventKey: "canonical-aliases", PhoneJID: &phone, LID: &lid,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{canonical.ContactID, phone, lid} {
		page, searchErr := repository.Search(context.Background(), instances[0].Id, query, 10, nil)
		if searchErr != nil || len(page.Items) != 1 || page.Items[0].ContactID != canonical.ContactID || len(page.Items[0].Aliases) != 4 {
			t.Fatalf("canonical alias search %q = %#v, %v", query, page, searchErr)
		}
	}
}
