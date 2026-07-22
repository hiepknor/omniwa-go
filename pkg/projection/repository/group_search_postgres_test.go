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
}
