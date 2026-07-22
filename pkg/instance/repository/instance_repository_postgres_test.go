package instance_repository

import (
	"context"
	"os"
	"sync"
	"testing"

	instance_credential "github.com/evolution-foundation/evolution-go/pkg/instance/credential"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
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
	repository := NewInstanceRepositoryWithTokenDigester(db, digester)

	created, err := repository.Create(instance_model.Instance{Name: prefix + "-new", Token: prefix + "-new-token"})
	if err != nil {
		t.Fatal(err)
	}
	if created.TokenDigest == nil || created.TokenKeyVersion == nil || *created.TokenKeyVersion != 9 || *created.TokenDigest == created.Token {
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

	backfiller := repository.(TokenBackfiller)
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
			return
		}
	}
	t.Fatal("bounded concurrent backfill did not reach the test fixtures")
}
