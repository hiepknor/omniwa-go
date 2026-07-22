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
		Recipients: []campaign_repository.RecipientConsent{
			{JID: "15550007771@s.whatsapp.net", OptInSource: "integration_test", EvidenceReference: "consent-1", OptedInAt: time.Now().Add(-time.Hour)},
			{JID: "15550007772@s.whatsapp.net", OptInSource: "integration_test", EvidenceReference: "consent-2", OptedInAt: time.Now().Add(-time.Hour)},
			{JID: "15550007773@s.whatsapp.net", OptInSource: "integration_test", EvidenceReference: "consent-3", OptedInAt: time.Now().Add(-time.Hour)},
			{JID: "15550007774@s.whatsapp.net", OptInSource: "integration_test", EvidenceReference: "consent-4", OptedInAt: time.Now().Add(-time.Hour)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.GetCampaign(context.Background(), instance.Id, campaign.ID)
	if err != nil || loaded.ID != campaign.ID || loaded.InstanceID != instance.Id {
		t.Fatalf("scoped campaign lookup = %#v, %v", loaded, err)
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
	pausedClaims, err := repository.ClaimReadyForInstance(context.Background(), instance.Id, 4, time.Minute)
	if err != nil || len(pausedClaims) != 0 {
		t.Fatalf("paused campaign produced claims = %#v, %v", pausedClaims, err)
	}
	globalPausedClaims, err := repository.ClaimReady(context.Background(), 4, time.Minute)
	if err != nil || len(globalPausedClaims) != 0 {
		t.Fatalf("global claim on paused campaign = %#v, %v", globalPausedClaims, err)
	}
	if _, err := repository.Transition(context.Background(), instance.Id, campaign.ID, campaign_model.CampaignStatusRunning, nil, campaign_repository.Actor{Type: "system"}); err != nil {
		t.Fatal(err)
	}

	claimResults := make(chan []campaign_model.Recipient, 2)
	claimErrors := make(chan error, 2)
	workers.Add(2)
	for index := 0; index < 2; index++ {
		go func() {
			defer workers.Done()
			claimed, claimErr := repository.ClaimReadyForInstance(context.Background(), instance.Id, 2, time.Minute)
			claimResults <- claimed
			claimErrors <- claimErr
		}()
	}
	workers.Wait()
	close(claimResults)
	close(claimErrors)
	for claimErr := range claimErrors {
		if claimErr != nil {
			t.Fatalf("concurrent claim: %v", claimErr)
		}
	}
	claimed := make([]campaign_model.Recipient, 0, 4)
	claimedIDs := make(map[string]struct{}, 4)
	for batch := range claimResults {
		claimed = append(claimed, batch...)
		for _, recipient := range batch {
			if _, duplicate := claimedIDs[recipient.ID]; duplicate {
				t.Fatalf("recipient claimed twice: %s", recipient.ID)
			}
			claimedIDs[recipient.ID] = struct{}{}
		}
	}
	if len(claimed) != 4 {
		t.Fatalf("claimed recipients = %d, want 4", len(claimed))
	}
	if err := repository.MarkSent(context.Background(), &claimed[0], "provider-1"); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkSent(context.Background(), &claimed[0], "provider-duplicate"); !errors.Is(err, campaign_repository.ErrRecipientClaimLost) {
		t.Fatalf("stale claim completion error = %v", err)
	}
	retryAt := time.Now().Add(25 * time.Millisecond)
	if err := repository.MarkDeferred(context.Background(), &claimed[1], "outbound_rate_limited", retryAt); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkFailed(context.Background(), &claimed[2], "permanent_failure"); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkSent(context.Background(), &claimed[3], "provider-4"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Transition(context.Background(), instance.Id, campaign.ID, campaign_model.CampaignStatusCompleted, nil, campaign_repository.Actor{Type: "system"}); !errors.Is(err, campaign_repository.ErrCampaignHasPendingWork) {
		t.Fatalf("premature completion error = %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	retried, err := repository.ClaimReadyForInstance(context.Background(), instance.Id, 1, time.Minute)
	if err != nil || len(retried) != 1 || retried[0].ID != claimed[1].ID {
		t.Fatalf("retried claim = %#v, %v", retried, err)
	}
	if retried[0].AttemptCount != 0 {
		t.Fatalf("rate-limit deferral consumed an attempt: %d", retried[0].AttemptCount)
	}
	retryAt = time.Now().Add(25 * time.Millisecond)
	if err := repository.MarkRetry(context.Background(), &retried[0], "temporary_failure", retryAt); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	retried, err = repository.ClaimReadyForInstance(context.Background(), instance.Id, 1, time.Minute)
	if err != nil || len(retried) != 1 || retried[0].AttemptCount != 1 {
		t.Fatalf("counted retry claim = %#v, %v", retried, err)
	}
	if err := repository.MarkSent(context.Background(), &retried[0], "provider-2"); err != nil {
		t.Fatal(err)
	}
	completed, err := repository.Transition(context.Background(), instance.Id, campaign.ID, campaign_model.CampaignStatusCompleted, nil, campaign_repository.Actor{Type: "system"})
	if err != nil || completed.Status != campaign_model.CampaignStatusCompleted {
		t.Fatalf("completed campaign = %#v, %v", completed, err)
	}
	audit, err = repository.ListAudit(context.Background(), instance.Id, campaign.ID)
	if err != nil || len(audit) != 12 {
		t.Fatalf("recipient audit = %#v, %v", audit, err)
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
