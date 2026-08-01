package projection_repository_test

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
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPhoneIdentityEvidenceIsIdempotentConflictSafeAndInstanceScoped(t *testing.T) {
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
		{Name: "phone-evidence-a-" + suffix, Token: "phone-evidence-token-a-" + suffix},
		{Name: "phone-evidence-b-" + suffix, Token: "phone-evidence-token-b-" + suffix},
	}
	for index := range instances {
		if err := db.Create(&instances[index]).Error; err != nil {
			t.Fatal(err)
		}
		defer db.Delete(&instances[index])
	}
	repository := projection_repository.NewPhoneIdentityEvidenceRepository(db)
	observedAt := time.Unix(1_000, 0).UTC()
	lid := "900001@lid"
	first := projection_model.PhoneIdentityEvidence{
		InstanceID: instances[0].Id, PhoneJID: "15550001@s.whatsapp.net", LIDJID: &lid,
		EvidenceKind: projection_model.PhoneIdentityEvidencePairedAlt, FirstObservedAt: observedAt, LastObservedAt: observedAt,
	}

	var wait sync.WaitGroup
	errorsCh := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, observeErr := repository.Observe(context.Background(), first)
			errorsCh <- observeErr
		}()
	}
	wait.Wait()
	close(errorsCh)
	for observeErr := range errorsCh {
		if observeErr != nil {
			t.Fatalf("concurrent idempotent observation failed: %v", observeErr)
		}
	}

	conflict := first
	conflict.PhoneJID = "15550002@s.whatsapp.net"
	if _, err := repository.Observe(context.Background(), conflict); !errors.Is(err, projection_repository.ErrPhoneIdentityEvidenceConflict) {
		t.Fatalf("conflicting relation error = %v", err)
	}
	secondInstance := first
	secondInstance.InstanceID = instances[1].Id
	secondInstance.PhoneJID = "15559999@s.whatsapp.net"
	if _, err := repository.Observe(context.Background(), secondInstance); err != nil {
		t.Fatalf("same LID in another instance should be isolated: %v", err)
	}

	resolvedA, err := repository.Resolve(context.Background(), instances[0].Id, []string{lid})
	if err != nil {
		t.Fatal(err)
	}
	resolvedB, err := repository.Resolve(context.Background(), instances[1].Id, []string{lid})
	if err != nil {
		t.Fatal(err)
	}
	if resolvedA[lid] != first.PhoneJID || resolvedB[lid] != secondInstance.PhoneJID {
		t.Fatalf("instance isolation failed: a=%v b=%v", resolvedA, resolvedB)
	}
}
