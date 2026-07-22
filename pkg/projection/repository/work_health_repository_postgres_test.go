package projection_repository

import (
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

func TestWorkHealthRepositoryAggregatesOnlyUnprocessedInstanceWork(t *testing.T) {
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
	suffix := uuid.NewString()
	instances := []instance_model.Instance{
		{Name: "work-health-a-" + suffix, Token: "work-health-a-token-" + suffix},
		{Name: "work-health-b-" + suffix, Token: "work-health-b-token-" + suffix},
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

	base := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)
	discarded := workHealthEvent(instances[0].Id, "groups", "discarded", projection_model.EventStatusProcessed, base.Add(-2*time.Minute))
	discarded.DiscardedAt = &base
	events := []projection_model.Event{
		workHealthEvent(instances[0].Id, "groups", "pending", projection_model.EventStatusPending, base),
		workHealthEvent(instances[0].Id, "groups", "processing", projection_model.EventStatusProcessing, base.Add(time.Minute)),
		workHealthEvent(instances[0].Id, "groups", "failed", projection_model.EventStatusFailed, base.Add(2*time.Minute)),
		workHealthEvent(instances[0].Id, "groups", "dead", projection_model.EventStatusDeadLetter, base.Add(3*time.Minute)),
		workHealthEvent(instances[0].Id, "groups", "processed", projection_model.EventStatusProcessed, base.Add(-time.Minute)),
		workHealthEvent(instances[0].Id, "contacts", "contact-pending", projection_model.EventStatusPending, base.Add(4*time.Minute)),
		workHealthEvent(instances[1].Id, "groups", "other-pending", projection_model.EventStatusPending, base.Add(-2*time.Minute)),
		discarded,
	}
	if err := db.Create(&events).Error; err != nil {
		t.Fatal(err)
	}

	repository := NewWorkHealthRepository(db)
	group, err := repository.Get(instances[0].Id, "groups")
	if err != nil || group.PendingEvents != 1 || group.ProcessingEvents != 1 || group.FailedEvents != 1 || group.DeadLetterEvents != 1 ||
		group.OldestUnprocessedAt == nil || !group.OldestUnprocessedAt.Equal(base) {
		t.Fatalf("group work health = %#v, %v", group, err)
	}
	resources, err := repository.List(instances[0].Id)
	if err != nil || len(resources) != 2 || resources[0].Resource != "contacts" || resources[1].Resource != "groups" {
		t.Fatalf("instance work health = %#v, %v", resources, err)
	}
	all, err := repository.List("")
	if err != nil {
		t.Fatal(err)
	}
	seenOther := false
	for _, resource := range all {
		if resource.InstanceID == instances[1].Id && resource.Resource == "groups" {
			seenOther = true
		}
	}
	if !seenOther {
		t.Fatalf("global work health omitted second instance: %#v", all)
	}
}

func TestWorkHealthRepositoryValidatesDependenciesAndIdentity(t *testing.T) {
	if _, err := NewWorkHealthRepository(nil).Get("instance-a", "groups"); err == nil {
		t.Fatal("Get() accepted a nil database")
	}
	if _, err := NewWorkHealthRepository(&gorm.DB{}).Get("", "groups"); err == nil {
		t.Fatal("Get() accepted an empty instance ID")
	}
	if _, err := NewWorkHealthRepository(&gorm.DB{}).Get("instance-a", ""); err == nil {
		t.Fatal("Get() accepted an empty resource")
	}
	if _, err := NewWorkHealthRepository(nil).List(""); err == nil {
		t.Fatal("List() accepted a nil database")
	}
}

func workHealthEvent(instanceID, resource, key string, status projection_model.EventStatus, ingestedAt time.Time) projection_model.Event {
	event := projection_model.Event{
		InstanceID: instanceID, Resource: resource, EventKey: key, EntityKey: key, EventType: "test",
		OccurredAt: ingestedAt, IngestedAt: ingestedAt, AvailableAt: ingestedAt, Status: status,
		Payload: json.RawMessage(`{}`), RetryPolicyVersion: projection_model.EventRetryPolicyVersion, MaxAttempts: projection_model.DefaultEventMaxAttempts,
	}
	if status == projection_model.EventStatusDeadLetter {
		errorCode := "invalid_payload"
		failureClass := projection_model.EventFailurePermanent
		event.LastErrorCode = &errorCode
		event.FailureClass = &failureClass
		event.DeadLetteredAt = &ingestedAt
	}
	return event
}
