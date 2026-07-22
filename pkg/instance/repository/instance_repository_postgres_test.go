package instance_repository_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	instance_credential "github.com/evolution-foundation/evolution-go/pkg/instance/credential"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	instance_repository "github.com/evolution-foundation/evolution-go/pkg/instance/repository"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresTokenDigestDualReadWriteAndConcurrentBackfill(t *testing.T) {
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

	prefix := "token-digest-test-" + uuid.NewString()
	t.Cleanup(func() {
		if err := db.Where("name LIKE ?", prefix+"%").Delete(&instance_model.Instance{}).Error; err != nil {
			t.Errorf("cleanup token digest fixtures: %v", err)
		}
	})
	digester, err := instance_credential.NewDigester([]byte("0123456789abcdef0123456789abcdef"), 9)
	if err != nil {
		t.Fatal(err)
	}
	repository := instance_repository.NewInstanceRepositoryWithTokenDigester(db, digester)

	created, err := repository.Create(instance_model.Instance{Name: prefix + "-new", Token: prefix + "-new-token"})
	if err != nil {
		t.Fatal(err)
	}
	if created.TokenDigest == nil || created.TokenKeyVersion == nil || *created.TokenKeyVersion != 9 || *created.TokenDigest == created.Token || created.TokenGeneration != 1 {
		t.Fatalf("dual-written credential metadata = %#v", created)
	}
	// Break the plaintext lookup deliberately: the original credential must
	// still resolve through the digest-first path.
	if err := db.Model(&instance_model.Instance{}).Where("id = ?", created.Id).Update("token", prefix+"-changed-plaintext").Error; err != nil {
		t.Fatal(err)
	}
	resolved, err := repository.GetInstanceByToken(prefix + "-new-token")
	if err != nil || resolved.Id != created.Id {
		t.Fatalf("digest-first lookup = %#v, %v", resolved, err)
	}

	legacyIDs := make([]string, 0, 4)
	for index := 0; index < 4; index++ {
		legacy := instance_model.Instance{Name: prefix + "-legacy-" + string(rune('a'+index)), Token: prefix + "-legacy-token-" + string(rune('a'+index))}
		if err := db.Create(&legacy).Error; err != nil {
			t.Fatal(err)
		}
		legacyIDs = append(legacyIDs, legacy.Id)
		if index == 0 {
			fallback, err := repository.GetInstanceByToken(legacy.Token)
			if err != nil || fallback.Id != legacy.Id {
				t.Fatalf("plaintext fallback = %#v, %v", fallback, err)
			}
		}
	}

	backfiller := repository.(instance_repository.TokenBackfiller)
	backfillComplete := false
	for attempt := 0; attempt < 100; attempt++ {
		var wg sync.WaitGroup
		errorsByWorker := make(chan error, 2)
		for worker := 0; worker < 2; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := backfiller.BackfillTokenDigests(context.Background(), 2)
				errorsByWorker <- err
			}()
		}
		wg.Wait()
		close(errorsByWorker)
		for err := range errorsByWorker {
			if err != nil {
				t.Fatal(err)
			}
		}
		var remaining int64
		if err := db.Model(&instance_model.Instance{}).Where("id IN ? AND token_digest IS NULL", legacyIDs).Count(&remaining).Error; err != nil {
			t.Fatal(err)
		}
		if remaining == 0 {
			backfillComplete = true
			break
		}
	}
	if !backfillComplete {
		t.Fatal("bounded concurrent backfill did not reach the test fixtures")
	}

	rotator := repository.(instance_repository.TokenRotator)
	firstRotation, err := rotator.RotateToken(context.Background(), instance_repository.TokenRotation{
		InstanceID: created.Id, ExpectedGeneration: 1, NewToken: prefix + "-rotated-token", Reason: "scheduled credential rotation",
		ActorReferenceHash: strings.Repeat("a", 64), RequestID: "request-identity-0001", OccurredAt: time.Unix(1000, 0),
	})
	if err != nil || firstRotation.TokenGeneration != 2 {
		t.Fatalf("first rotation = %#v, %v", firstRotation, err)
	}
	if _, err := repository.GetInstanceByToken(prefix + "-new-token"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("old token remains valid: %v", err)
	}
	if rotated, err := repository.GetInstanceByToken(prefix + "-rotated-token"); err != nil || rotated.Id != created.Id {
		t.Fatalf("rotated token lookup = %#v, %v", rotated, err)
	}

	results := make(chan error, 2)
	for index := 0; index < 2; index++ {
		index := index
		go func() {
			_, err := rotator.RotateToken(context.Background(), instance_repository.TokenRotation{
				InstanceID: created.Id, ExpectedGeneration: 2, NewToken: fmt.Sprintf("%s-concurrent-%d", prefix, index), Reason: "concurrent operator request",
				ActorReferenceHash: strings.Repeat("b", 64), RequestID: fmt.Sprintf("request-identity-000%d", index+2), OccurredAt: time.Unix(1100+int64(index), 0),
			})
			results <- err
		}()
	}
	successes, conflicts := 0, 0
	for index := 0; index < 2; index++ {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, instance_repository.ErrTokenRotationConflict):
			conflicts++
		default:
			t.Fatalf("concurrent rotation error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent rotations: successes=%d conflicts=%d", successes, conflicts)
	}
	beforeDuplicate, err := repository.GetInstanceByID(created.Id)
	if err != nil || beforeDuplicate.TokenGeneration != 3 {
		t.Fatalf("state before duplicate request = %#v, %v", beforeDuplicate, err)
	}
	_, err = rotator.RotateToken(context.Background(), instance_repository.TokenRotation{
		InstanceID: created.Id, ExpectedGeneration: 3, NewToken: prefix + "-must-rollback", Reason: "duplicate request identity",
		ActorReferenceHash: strings.Repeat("c", 64), RequestID: "request-identity-0001", OccurredAt: time.Unix(1200, 0),
	})
	if !errors.Is(err, instance_repository.ErrTokenRotationConflict) {
		t.Fatalf("duplicate request identity error = %v", err)
	}
	afterDuplicate, err := repository.GetInstanceByID(created.Id)
	if err != nil || afterDuplicate.TokenGeneration != beforeDuplicate.TokenGeneration || afterDuplicate.Token != beforeDuplicate.Token {
		t.Fatalf("duplicate request did not roll back: before=%#v after=%#v err=%v", beforeDuplicate, afterDuplicate, err)
	}
	var audits []instance_model.TokenRotationAudit
	if err := db.Where("instance_id = ?", created.Id).Order("occurred_at ASC").Find(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if len(audits) != 2 || audits[0].PreviousGeneration != 1 || audits[0].NewGeneration != 2 ||
		audits[0].ActorReferenceHash == "admin-secret" || audits[0].Reason != "scheduled credential rotation" {
		t.Fatalf("rotation audit = %#v", audits)
	}
}
