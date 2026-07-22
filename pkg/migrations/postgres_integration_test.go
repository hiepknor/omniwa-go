package migrations_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

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
	eventRepository := projection_repository.NewEventRepository(db)
	event := &projection_model.Event{
		InstanceID: instance.Id, Resource: "groups", EventKey: "event-1", EntityKey: "group-1",
		EventType: "group_info", OccurredAt: time.Now(), Payload: json.RawMessage(`{"id":"group-1"}`),
	}
	inserted, err := eventRepository.Enqueue(context.Background(), event)
	if err != nil || !inserted {
		t.Fatalf("first enqueue = %v, %v", inserted, err)
	}
	duplicate := *event
	inserted, err = eventRepository.Enqueue(context.Background(), &duplicate)
	if err != nil || inserted {
		t.Fatalf("duplicate enqueue = %v, %v", inserted, err)
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
	claimed, err := projection_repository.NewEventRepository(reopened).ClaimPending(context.Background(), 10, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].EventKey != "event-1" {
		t.Fatalf("claimed after reconnect = %#v, %v", claimed, err)
	}
	if err := reopened.Model(&projection_model.Event{}).
		Where("instance_id = ? AND resource = ? AND event_key = ?", instance.Id, "groups", "event-1").
		Update("lease_until", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	reclaimed, err := projection_repository.NewEventRepository(reopened).ClaimPending(context.Background(), 10, time.Minute)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].ClaimToken == nil || claimed[0].ClaimToken == nil || *reclaimed[0].ClaimToken == *claimed[0].ClaimToken {
		t.Fatalf("reclaimed expired lease = %#v, %v", reclaimed, err)
	}
	if err := projection_repository.NewEventRepository(reopened).MarkProcessed(context.Background(), &claimed[0]); !errors.Is(err, projection_repository.ErrEventClaimLost) {
		t.Fatalf("stale worker MarkProcessed() error = %v", err)
	}
}
