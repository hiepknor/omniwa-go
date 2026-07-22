package campaign_repository_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	campaign_repository "github.com/evolution-foundation/evolution-go/pkg/campaign/repository"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestCampaignRepositoryPostgresSerializesTransitionsAndEnforcesConsent(t *testing.T) {
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
	instance := instance_model.Instance{Name: "campaign-concurrency-" + suffix, Token: "campaign-concurrency-token-" + suffix}
	if err := db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Delete(&instance)

	repository := campaign_repository.NewCampaignRepository(db)
	campaign, _, err := repository.CreateDraft(context.Background(), instance.Id, campaign_repository.DraftInput{
		Name: "Concurrency", TextBody: "Hello", Actor: campaign_repository.Actor{Type: "system"},
		Recipients: []campaign_repository.RecipientConsent{{
			JID: "15550007777@s.whatsapp.net", OptInSource: "integration_test",
			EvidenceReference: "consent-record", OptedInAt: time.Now().Add(-time.Hour),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	startsAt := time.Now().UTC()
	if _, err := repository.Transition(context.Background(), instance.Id, campaign.ID, campaign_model.CampaignStatusScheduled, &startsAt, campaign_repository.Actor{Type: "system"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Transition(context.Background(), instance.Id, campaign.ID, campaign_model.CampaignStatusRunning, nil, campaign_repository.Actor{Type: "system"}); err != nil {
		t.Fatal(err)
	}

	results := make(chan error, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	for index := 0; index < 2; index++ {
		go func() {
			defer workers.Done()
			_, transitionErr := repository.Transition(context.Background(), instance.Id, campaign.ID, campaign_model.CampaignStatusPaused, nil, campaign_repository.Actor{Type: "system"})
			results <- transitionErr
		}()
	}
	workers.Wait()
	close(results)
	succeeded, rejected := 0, 0
	for result := range results {
		if result == nil {
			succeeded++
		} else if errors.Is(result, campaign_repository.ErrInvalidCampaignTransition) || errors.Is(result, campaign_repository.ErrCampaignConflict) {
			rejected++
		} else {
			t.Fatalf("unexpected concurrent transition error: %v", result)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("concurrent transitions succeeded=%d rejected=%d", succeeded, rejected)
	}
	audit, err := repository.ListAudit(context.Background(), instance.Id, campaign.ID)
	if err != nil || len(audit) != 4 {
		t.Fatalf("atomic audit = %#v, %v", audit, err)
	}

	invalidRecipient := campaign_model.Recipient{
		ID: uuid.NewString(), CampaignID: campaign.ID, InstanceID: instance.Id, RecipientJID: "15550008888@s.whatsapp.net",
		Status: campaign_model.RecipientStatusPending, OptInSource: "integration_test", OptInReferenceHash: "raw-reference",
		OptedInAt: time.Now().Add(-time.Hour), NextAttemptAt: time.Now(),
	}
	if err := db.Create(&invalidRecipient).Error; err == nil {
		t.Fatal("database accepted an unhashed consent reference")
	}
}
