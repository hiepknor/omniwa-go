package migrations_test

import (
	"os"
	"testing"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresMigrationIsIdempotentAndStateSurvivesReconnect(t *testing.T) {
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
	if err := migrations.Run(db); err != nil {
		t.Fatalf("second migration run failed: %v", err)
	}

	instance := instance_model.Instance{Name: "migration-test", Token: "migration-test-token"}
	if err := db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	repository := projection_repository.NewStateRepository(db)
	state := &projection_model.State{InstanceID: instance.Id, Resource: "groups", SyncStatus: projection_model.SyncStatusNotStarted, SchemaVersion: 1}
	if err := repository.Upsert(state); err != nil {
		t.Fatal(err)
	}

	raw, _ := db.DB()
	_ = raw.Close()
	reopened, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := projection_repository.NewStateRepository(reopened).Get(instance.Id, "groups")
	if err != nil {
		t.Fatal(err)
	}
	if stored.SyncStatus != projection_model.SyncStatusNotStarted || stored.SchemaVersion != 1 {
		t.Fatalf("unexpected stored state: %#v", stored)
	}
}
