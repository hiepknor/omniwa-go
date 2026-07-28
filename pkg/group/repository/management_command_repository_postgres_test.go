package group_repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	group_model "github.com/evolution-foundation/evolution-go/pkg/group/model"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestManagementCommandRepositoryIsScopedIdempotentAndAudited(t *testing.T) {
	db := managementPostgres(t)
	suffix := uuid.NewString()
	instances := []instance_model.Instance{
		{Name: "group-management-a-" + suffix, Token: "group-management-token-a-" + suffix},
		{Name: "group-management-b-" + suffix, Token: "group-management-token-b-" + suffix},
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

	repository := NewManagementCommandRepository(db)
	idempotencyHash := managementHash("idempotency-key")
	input := CreateManagementCommandInput{
		ID: uuid.NewString(), InstanceID: instances[0].Id, GroupJID: "120363000001@g.us", CommandType: "group_name_updated",
		IdempotencyKeyHash: &idempotencyHash, RequestFingerprint: managementHash("request-a"),
		ActorType: "instance", ActorReferenceHash: managementHash("actor-a"),
	}
	created, wasCreated, err := repository.Create(context.Background(), input)
	if err != nil || !wasCreated || created.Status != group_model.ManagementCommandRequested {
		t.Fatalf("create = %#v/%t/%v", created, wasCreated, err)
	}

	replay := input
	replay.ID = uuid.NewString()
	replayed, wasCreated, err := repository.Create(context.Background(), replay)
	if err != nil || wasCreated || replayed.ID != created.ID {
		t.Fatalf("idempotent replay = %#v/%t/%v", replayed, wasCreated, err)
	}
	replay.RequestFingerprint = managementHash("different-request")
	if _, _, err := repository.Create(context.Background(), replay); !errors.Is(err, ErrManagementIdempotencyConflict) {
		t.Fatalf("different idempotent request = %v", err)
	}
	if _, err := repository.Get(context.Background(), instances[1].Id, created.ID); !errors.Is(err, ErrManagementCommandNotFound) {
		t.Fatalf("cross-instance read = %v", err)
	}

	crossInstance := input
	crossInstance.ID = uuid.NewString()
	crossInstance.InstanceID = instances[1].Id
	if _, inserted, err := repository.Create(context.Background(), crossInstance); err != nil || !inserted {
		t.Fatalf("cross-instance idempotency scope = %t/%v", inserted, err)
	}

	executing, err := repository.MarkExecuting(context.Background(), instances[0].Id, created.ID)
	if err != nil || executing.Status != group_model.ManagementCommandExecuting || executing.ExecutionStartedAt == nil {
		t.Fatalf("mark executing = %#v/%v", executing, err)
	}
	completed, err := repository.Complete(context.Background(), instances[0].Id, created.ID, CompleteManagementCommandInput{
		Status:       group_model.ManagementCommandPartiallyCompleted,
		SafeOutcome:  json.RawMessage(`{"succeededCount":1,"failedCount":1,"privateResult":"not-audited"}`),
		AuditSummary: json.RawMessage(`{"participantCount":2,"failureCount":1}`),
	})
	if err != nil || completed.Status != group_model.ManagementCommandPartiallyCompleted || completed.CompletedAt == nil {
		t.Fatalf("complete = %#v/%v", completed, err)
	}
	if _, err := repository.Complete(context.Background(), instances[0].Id, created.ID, CompleteManagementCommandInput{
		Status: group_model.ManagementCommandCompleted, SafeOutcome: json.RawMessage(`{}`), AuditSummary: json.RawMessage(`{}`),
	}); !errors.Is(err, ErrManagementCommandConflict) {
		t.Fatalf("second completion = %v", err)
	}

	audit, err := repository.ListAudit(context.Background(), instances[0].Id, input.GroupJID, 10, nil)
	if err != nil || len(audit.Items) != 3 || audit.Items[0].EventType != "partially_completed" || audit.Items[2].EventType != "requested" {
		t.Fatalf("audit = %#v/%v", audit, err)
	}
	encoded, err := json.Marshal(audit.Items)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"privateResult", "not-audited", managementHash("actor-a"), input.GroupJID} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("audit JSON exposed %q: %s", forbidden, encoded)
		}
	}
	firstPage, err := repository.ListAudit(context.Background(), instances[0].Id, input.GroupJID, 1, nil)
	if err != nil || len(firstPage.Items) != 1 || firstPage.NextCursor == nil {
		t.Fatalf("first audit page = %#v/%v", firstPage, err)
	}
	secondPage, err := repository.ListAudit(context.Background(), instances[0].Id, input.GroupJID, 1, firstPage.NextCursor)
	if err != nil || len(secondPage.Items) != 1 || secondPage.Items[0].ID == firstPage.Items[0].ID {
		t.Fatalf("second audit page = %#v/%v", secondPage, err)
	}
}

func TestManagementCommandRepositoryConcurrentIdempotencyCreatesOneCommand(t *testing.T) {
	db := managementPostgres(t)
	suffix := uuid.NewString()
	instance := instance_model.Instance{Name: "group-management-idempotency-" + suffix, Token: "group-management-idempotency-token-" + suffix}
	if err := db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Delete(&instance).Error })

	idempotencyHash := managementHash("concurrent-idempotency")
	repository := NewManagementCommandRepository(db)
	type result struct {
		commandID string
		created   bool
		err       error
	}
	results := make(chan result, 8)
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			command, created, err := repository.Create(context.Background(), CreateManagementCommandInput{
				ID: uuid.NewString(), InstanceID: instance.Id, GroupJID: "120363000003@g.us", CommandType: "group_description_updated",
				IdempotencyKeyHash: &idempotencyHash, RequestFingerprint: managementHash("same-request"),
				ActorType: "instance", ActorReferenceHash: managementHash("same-actor"),
			})
			commandID := ""
			if command != nil {
				commandID = command.ID
			}
			results <- result{commandID: commandID, created: created, err: err}
		}()
	}
	wait.Wait()
	close(results)
	createdCount := 0
	commandID := ""
	for item := range results {
		if item.err != nil || item.commandID == "" {
			t.Fatalf("concurrent create = %#v", item)
		}
		if commandID == "" {
			commandID = item.commandID
		} else if item.commandID != commandID {
			t.Fatalf("idempotency produced command IDs %s and %s", commandID, item.commandID)
		}
		if item.created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("created count = %d", createdCount)
	}
	var auditCount int64
	if err := db.Model(&group_model.ManagementAuditEvent{}).Where("instance_id = ? AND command_id = ?", instance.Id, commandID).Count(&auditCount).Error; err != nil || auditCount != 1 {
		t.Fatalf("requested audit count = %d/%v", auditCount, err)
	}
}

func TestManagementCommandRepositoryRecoversUnknownWithoutRetry(t *testing.T) {
	db := managementPostgres(t)
	suffix := uuid.NewString()
	instance := instance_model.Instance{Name: "group-management-recovery-" + suffix, Token: "group-management-recovery-token-" + suffix}
	if err := db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Delete(&instance).Error })

	repository := NewManagementCommandRepository(db)
	repositoryImpl := repository.(*managementCommandRepository)
	command, _, err := repository.Create(context.Background(), CreateManagementCommandInput{
		ID: uuid.NewString(), InstanceID: instance.Id, GroupJID: "120363000002@g.us", CommandType: "group_left",
		RequestFingerprint: managementHash("request-recovery"), ActorType: "instance", ActorReferenceHash: managementHash("actor-recovery"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.MarkExecuting(context.Background(), instance.Id, command.ID); err != nil {
		t.Fatal(err)
	}
	recoveryNow := time.Now().UTC().Add(time.Hour)
	repositoryImpl.now = func() time.Time { return recoveryNow }
	recovered, err := repository.RecoverStaleExecuting(context.Background(), recoveryNow.Add(-time.Minute), 10)
	if err != nil || recovered != 1 {
		t.Fatalf("recover = %d/%v", recovered, err)
	}
	stored, err := repository.Get(context.Background(), instance.Id, command.ID)
	if err != nil || stored.Status != group_model.ManagementCommandUnknown || !strings.Contains(string(stored.SafeOutcome), "recovery_timeout") {
		t.Fatalf("recovered command = %#v/%v", stored, err)
	}
	if _, err := repository.MarkExecuting(context.Background(), instance.Id, command.ID); !errors.Is(err, ErrManagementCommandConflict) {
		t.Fatalf("unknown command was restartable: %v", err)
	}
}

func managementPostgres(t *testing.T) *gorm.DB {
	t.Helper()
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
	return db
}

func managementHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
