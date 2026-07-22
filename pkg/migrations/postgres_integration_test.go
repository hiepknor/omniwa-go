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

	groupRepository := projection_repository.NewGroupRepository(reopened)
	newName := "Current group"
	newer := time.Unix(500, 0)
	applied, err := groupRepository.ApplySnapshot(context.Background(), &projection_model.Group{
		InstanceID: instance.Id, GroupID: "group@g.us", Name: &newName, SourceOccurredAt: newer, SourceEventKey: "event-500",
	}, []projection_model.GroupParticipant{{ParticipantID: "user-a@s.whatsapp.net", Role: projection_model.ParticipantRoleAdmin}})
	if err != nil || !applied {
		t.Fatalf("new group snapshot = %v, %v", applied, err)
	}
	oldName := "Stale group"
	applied, err = groupRepository.ApplySnapshot(context.Background(), &projection_model.Group{
		InstanceID: instance.Id, GroupID: "group@g.us", Name: &oldName, SourceOccurredAt: time.Unix(400, 0), SourceEventKey: "event-400",
	}, []projection_model.GroupParticipant{{ParticipantID: "stale-user@s.whatsapp.net", Role: projection_model.ParticipantRoleMember}})
	if err != nil || applied {
		t.Fatalf("stale group snapshot = %v, %v", applied, err)
	}
	applied, err = groupRepository.ApplySnapshot(context.Background(), &projection_model.Group{
		InstanceID: instance.Id, GroupID: "group@g.us", Name: &oldName, SourceOccurredAt: newer, SourceEventKey: "event-499",
	}, nil)
	if err != nil || applied {
		t.Fatalf("lower-key timestamp tie = %v, %v", applied, err)
	}
	storedGroup, storedParticipants, err := groupRepository.Get(context.Background(), instance.Id, "group@g.us")
	if err != nil || storedGroup.Name == nil || *storedGroup.Name != newName || len(storedParticipants) != 1 || storedParticipants[0].ParticipantID != "user-a@s.whatsapp.net" {
		t.Fatalf("stored group after stale snapshot = %#v, %#v, %v", storedGroup, storedParticipants, err)
	}
	reconciledName := "Reconciled group"
	applied, err = groupRepository.ApplySnapshot(context.Background(), &projection_model.Group{
		InstanceID: instance.Id, GroupID: "group@g.us", Name: &reconciledName, SourceOccurredAt: time.Unix(600, 0), SourceEventKey: "event-600",
	}, []projection_model.GroupParticipant{{ParticipantID: "user-b@s.whatsapp.net", Role: projection_model.ParticipantRoleSuperAdmin}})
	if err != nil || !applied {
		t.Fatalf("reconciled group snapshot = %v, %v", applied, err)
	}
	_, storedParticipants, err = groupRepository.Get(context.Background(), instance.Id, "group@g.us")
	if err != nil || len(storedParticipants) != 1 || storedParticipants[0].ParticipantID != "user-b@s.whatsapp.net" {
		t.Fatalf("participants after replacement = %#v, %v", storedParticipants, err)
	}
	applied, err = groupRepository.Tombstone(context.Background(), instance.Id, "group@g.us", "delete-550", time.Unix(550, 0))
	if err != nil || applied {
		t.Fatalf("stale tombstone = %v, %v", applied, err)
	}
	applied, err = groupRepository.Tombstone(context.Background(), instance.Id, "group@g.us", "delete-700", time.Unix(700, 0))
	if err != nil || !applied {
		t.Fatalf("new tombstone = %v, %v", applied, err)
	}
	if _, _, err := groupRepository.Get(context.Background(), instance.Id, "group@g.us"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("tombstoned group remained readable: %v", err)
	}
}
