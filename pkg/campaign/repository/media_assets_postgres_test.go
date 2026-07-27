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

func TestMediaAssetRepositoryPostgresEnforcesIsolationIdempotencyAndCleanupFencing(t *testing.T) {
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
		{Name: "campaign-media-a-" + suffix, Token: "campaign-media-token-a-" + suffix},
		{Name: "campaign-media-b-" + suffix, Token: "campaign-media-token-b-" + suffix},
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

	repository := campaign_repository.NewMediaAssetRepository(db)
	requestHash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	assetID := uuid.NewString()
	input := campaign_repository.CreateMediaAssetInput{
		ID: assetID, InstanceID: instances[0].Id,
		ObjectKey:            "campaign-media/" + instances[0].Id + "/" + assetID + "/image",
		RequestReferenceHash: &requestHash, ExpiresAt: time.Now().Add(time.Hour),
	}
	created, inserted, err := repository.CreateUploading(context.Background(), input)
	if err != nil || !inserted || created.ID != assetID {
		t.Fatalf("create=%+v inserted=%t err=%v", created, inserted, err)
	}
	replayInput := input
	replayInput.ID = uuid.NewString()
	replayInput.ObjectKey = "campaign-media/" + instances[0].Id + "/" + replayInput.ID + "/image"
	replayed, inserted, err := repository.CreateUploading(context.Background(), replayInput)
	if err != nil || inserted || replayed.ID != assetID {
		t.Fatalf("replay=%+v inserted=%t err=%v", replayed, inserted, err)
	}
	if _, err := repository.Get(context.Background(), instances[1].Id, assetID); !errors.Is(err, campaign_repository.ErrMediaAssetNotFound) {
		t.Fatalf("cross-instance read error=%v", err)
	}
	ready, err := repository.MarkReady(context.Background(), instances[0].Id, assetID, campaign_repository.ReadyMediaAssetInput{
		MIMEType: "image/jpeg", SizeBytes: 512, Width: 16, Height: 12,
		SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	})
	if err != nil || ready.ReadyAt == nil {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	if _, err := repository.MarkReady(context.Background(), instances[0].Id, assetID, campaign_repository.ReadyMediaAssetInput{
		MIMEType: "image/jpeg", SizeBytes: 512, Width: 16, Height: 12,
		SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}); !errors.Is(err, campaign_repository.ErrMediaAssetConflict) {
		t.Fatalf("second ready mutation error=%v", err)
	}

	results := make(chan error, 2)
	claims := make(chan *campaign_model.MediaAsset, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			claim, claimErr := repository.ClaimDelete(context.Background(), instances[0].Id, assetID, time.Minute)
			claims <- claim
			results <- claimErr
		}()
	}
	workers.Wait()
	close(results)
	close(claims)
	succeeded, conflicted := 0, 0
	var winner *campaign_model.MediaAsset
	for claim := range claims {
		if claim != nil {
			winner = claim
		}
	}
	for claimErr := range results {
		if claimErr == nil {
			succeeded++
		} else if errors.Is(claimErr, campaign_repository.ErrMediaAssetConflict) {
			conflicted++
		} else {
			t.Fatalf("unexpected claim error=%v", claimErr)
		}
	}
	if succeeded != 1 || conflicted != 1 || winner == nil {
		t.Fatalf("cleanup claims succeeded=%d conflicted=%d winner=%T", succeeded, conflicted, winner)
	}
	if err := repository.CompleteCleanup(context.Background(), winner); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteCleanup(context.Background(), winner); !errors.Is(err, campaign_repository.ErrMediaAssetConflict) {
		t.Fatalf("stale cleanup completion error=%v", err)
	}
	if _, err := repository.Get(context.Background(), instances[0].Id, assetID); !errors.Is(err, campaign_repository.ErrMediaAssetNotFound) {
		t.Fatalf("deleted asset remains visible: %v", err)
	}
}
