package group_list_repository

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresRepositoryEnforcesIsolationVersioningAndAudit(t *testing.T) {
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
		{Name: "group-list-repository-a-" + suffix, Token: "token-a-" + suffix},
		{Name: "group-list-repository-b-" + suffix, Token: "token-b-" + suffix},
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

	repository := New(db)
	created, err := repository.Create(context.Background(), createRepositoryInput(uuid.NewString(), instances[0].Id, "Northern branches", "northern branches", "120363000001@g.us"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 {
		t.Fatalf("created version = %d", created.Version)
	}
	if _, err := repository.Create(context.Background(), createRepositoryInput(uuid.NewString(), instances[0].Id, "Northern Branches", "northern branches", "120363000002@g.us")); !errors.Is(err, ErrNameConflict) {
		t.Fatalf("same-instance name conflict = %v", err)
	}
	if _, err := repository.Create(context.Background(), createRepositoryInput(uuid.NewString(), instances[1].Id, "Northern branches", "northern branches", "120363000003@g.us")); err != nil {
		t.Fatalf("cross-instance name reuse failed: %v", err)
	}
	if _, err := repository.Get(context.Background(), instances[1].Id, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-instance read = %v", err)
	}

	page, err := repository.List(context.Background(), instances[0].Id, "north", 1, nil)
	if err != nil || len(page.Items) != 1 || page.Items[0].GroupCount != 1 {
		t.Fatalf("list page = %+v, %v", page, err)
	}
	entries, err := repository.ListEntries(context.Background(), instances[0].Id, created.ID, 1, nil)
	if err != nil || len(entries.Items) != 1 || entries.Items[0].GroupNameSnapshot != "Branch 01" {
		t.Fatalf("entry page = %+v, %v", entries, err)
	}

	update := UpdateInput{
		Name: "Northern branches v2", NormalizedName: "northern branches v2", ExpectedVersion: 1,
		AuthorizationSource: "operator_attestation", AuthorizationReferenceHash: hash64("b"), AuthorizedAt: time.Unix(20, 0),
		ActorReferenceHash: hash64("c"), Entries: []EntryInput{{GroupJID: "120363000004@g.us", GroupNameSnapshot: "Branch 04"}},
	}
	updated, err := repository.Update(context.Background(), instances[0].Id, created.ID, update)
	if err != nil || updated.Version != 2 {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	if _, err := repository.Update(context.Background(), instances[0].Id, created.ID, update); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale update = %v", err)
	}
	audit, err := repository.ListAudit(context.Background(), instances[0].Id, created.ID, 10, nil)
	if err != nil || len(audit.Items) != 2 || audit.Items[0].EventType != "created" || audit.Items[1].EventType != "updated" {
		t.Fatalf("audit = %+v, %v", audit, err)
	}
	encodedAudit, err := json.Marshal(audit.Items)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedAudit), "actorReferenceHash") || strings.Contains(string(encodedAudit), hash64("f")) {
		t.Fatalf("actor hash leaked through JSON: %s", encodedAudit)
	}
	if err := repository.Delete(context.Background(), instances[0].Id, created.ID, hash64("9")); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Get(context.Background(), instances[0].Id, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("soft-deleted list read = %v", err)
	}
	audit, err = repository.ListAudit(context.Background(), instances[0].Id, created.ID, 10, nil)
	if err != nil || len(audit.Items) != 3 || audit.Items[2].EventType != "deleted" || audit.Items[2].ToVersion != 3 {
		t.Fatalf("delete audit = %+v, %v", audit, err)
	}
	if _, err := repository.Create(context.Background(), createRepositoryInput(uuid.NewString(), instances[0].Id, "Northern branches v2", "northern branches v2", "120363000005@g.us")); err != nil {
		t.Fatalf("soft-deleted name was not reusable: %v", err)
	}
}

func TestPostgresRepositoryAllowsOnlyOneConcurrentExpectedVersionUpdate(t *testing.T) {
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
	instance := instance_model.Instance{Name: "group-list-concurrency-" + suffix, Token: "token-" + suffix}
	if err := db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Delete(&instance).Error })
	repository := New(db)
	created, err := repository.Create(context.Background(), createRepositoryInput(uuid.NewString(), instance.Id, "Branches", "branches", "120363000001@g.us"))
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errorsByUpdate := make(chan error, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, updateErr := repository.Update(context.Background(), instance.Id, created.ID, UpdateInput{
				Name: "Branches update", NormalizedName: "branches update", ExpectedVersion: 1,
				AuthorizationSource: "operator_attestation", AuthorizationReferenceHash: hash64("d"), AuthorizedAt: time.Unix(20, 0),
				ActorReferenceHash: hash64("e"), Entries: []EntryInput{{GroupJID: "12036300000" + string(rune('2'+index)) + "@g.us", GroupNameSnapshot: "Branch"}},
			})
			errorsByUpdate <- updateErr
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByUpdate)
	successes, conflicts := 0, 0
	for updateErr := range errorsByUpdate {
		switch {
		case updateErr == nil:
			successes++
		case errors.Is(updateErr, ErrVersionConflict):
			conflicts++
		default:
			t.Fatalf("concurrent update error = %v", updateErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent outcomes = successes:%d conflicts:%d", successes, conflicts)
	}
}

func createRepositoryInput(id, instanceID, name, normalizedName, groupJID string) CreateInput {
	return CreateInput{
		ID: id, InstanceID: instanceID, Name: name, NormalizedName: normalizedName,
		AuthorizationSource: "operator_attestation", AuthorizationReferenceHash: hash64("a"), AuthorizedAt: time.Unix(10, 0),
		ActorReferenceHash: hash64("f"), Entries: []EntryInput{{GroupJID: groupJID, GroupNameSnapshot: "Branch 01"}},
	}
}

func hash64(character string) string {
	value := ""
	for len(value) < 64 {
		value += character
	}
	return value[:64]
}
