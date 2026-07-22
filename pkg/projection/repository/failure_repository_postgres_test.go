package projection_repository

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestFailureRepositoryListsAndAtomicallyAuditsOperations(t *testing.T) {
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
		{Name: "failure-ops-a-" + suffix, Token: "failure-ops-a-token-" + suffix},
		{Name: "failure-ops-b-" + suffix, Token: "failure-ops-b-token-" + suffix},
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

	base := time.Now().UTC().Truncate(time.Microsecond)
	events := []projection_model.Event{
		workHealthEvent(instances[0].Id, "groups", "event-old", projection_model.EventStatusDeadLetter, base.Add(-time.Minute)),
		workHealthEvent(instances[0].Id, "groups", "event-new", projection_model.EventStatusDeadLetter, base),
		workHealthEvent(instances[1].Id, "groups", "event-other", projection_model.EventStatusDeadLetter, base.Add(time.Minute)),
	}
	if err := db.Create(&events).Error; err != nil {
		t.Fatal(err)
	}
	repository := NewFailureRepository(db)
	first, err := repository.ListDeadLetters(context.Background(), instances[0].Id, "groups", 1, nil)
	if err != nil || len(first.Items) != 1 || first.Items[0].EventKey != "event-new" || first.NextCursor == nil {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	second, err := repository.ListDeadLetters(context.Background(), instances[0].Id, "groups", 1, first.NextCursor)
	if err != nil || len(second.Items) != 1 || second.Items[0].EventKey != "event-old" || second.NextCursor != nil {
		t.Fatalf("second page = %#v, %v", second, err)
	}

	actorHash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	replayAt := base.Add(2 * time.Minute)
	if err := repository.ApplyOperation(context.Background(), FailureOperation{
		InstanceID: instances[0].Id, Resource: "groups", EventKey: "event-old", Action: projection_model.FailureActionReplay,
		Reason: "projector fix deployed", ActorReferenceHash: actorHash, RequestID: "request-identity-0001", OccurredAt: replayAt,
	}); err != nil {
		t.Fatalf("replay: %v", err)
	}
	var replayed projection_model.Event
	if err := db.First(&replayed, "instance_id = ? AND resource = ? AND event_key = ?", instances[0].Id, "groups", "event-old").Error; err != nil ||
		replayed.Status != projection_model.EventStatusPending || replayed.RetryCount != 0 || replayed.DeadLetteredAt != nil || replayed.LastErrorCode != nil || !replayed.AvailableAt.Equal(replayAt) {
		t.Fatalf("replayed event = %#v, %v", replayed, err)
	}

	concurrentAt := base.Add(3 * time.Minute)
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- repository.ApplyOperation(context.Background(), FailureOperation{
				InstanceID: instances[0].Id, Resource: "groups", EventKey: "event-new", Action: projection_model.FailureActionDiscard,
				Reason: "confirmed obsolete event", ActorReferenceHash: actorHash, RequestID: "request-identity-0002", OccurredAt: concurrentAt,
			})
		}()
	}
	wait.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for result := range results {
		switch {
		case result == nil:
			succeeded++
		case errors.Is(result, ErrProjectionFailureNotActionable):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent operation error: %v", result)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent results succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	var discarded projection_model.Event
	if err := db.First(&discarded, "instance_id = ? AND resource = ? AND event_key = ?", instances[0].Id, "groups", "event-new").Error; err != nil ||
		discarded.Status != projection_model.EventStatusProcessed || discarded.DiscardedAt == nil || discarded.ProcessedAt != nil || discarded.DeadLetteredAt != nil {
		t.Fatalf("discarded event = %#v, %v", discarded, err)
	}
	var audits []projection_model.FailureAudit
	if err := db.Order("occurred_at ASC").Find(&audits, "instance_id = ?", instances[0].Id).Error; err != nil || len(audits) != 2 ||
		audits[0].Action != projection_model.FailureActionReplay || audits[1].Action != projection_model.FailureActionDiscard || audits[1].ActorReferenceHash != actorHash || audits[1].RequestID != "request-identity-0002" {
		t.Fatalf("failure audits = %#v, %v", audits, err)
	}
	remaining, err := repository.ListDeadLetters(context.Background(), instances[0].Id, "groups", 10, nil)
	if err != nil || len(remaining.Items) != 0 {
		t.Fatalf("terminal failures remained actionable = %#v, %v", remaining, err)
	}
}
