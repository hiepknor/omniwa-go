package campaign_repository_test

import (
	"context"
	"errors"
	"os"
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

func TestImageCampaignDraftSnapshotsReadyInstanceMediaAndProtectsReference(t *testing.T) {
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
		{Name: "image-campaign-a-" + suffix, Token: "image-campaign-token-a-" + suffix},
		{Name: "image-campaign-b-" + suffix, Token: "image-campaign-token-b-" + suffix},
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

	mediaRepository := campaign_repository.NewMediaAssetRepository(db)
	assetID := uuid.NewString()
	asset, _, err := mediaRepository.CreateUploading(context.Background(), campaign_repository.CreateMediaAssetInput{
		ID: assetID, InstanceID: instances[0].Id,
		ObjectKey: "campaign-media/" + instances[0].Id + "/" + assetID + "/image", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	asset, err = mediaRepository.MarkReady(context.Background(), instances[0].Id, asset.ID, campaign_repository.ReadyMediaAssetInput{
		MIMEType: "image/png", SizeBytes: 2048, Width: 800, Height: 600,
		SHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	})
	if err != nil {
		t.Fatal(err)
	}

	repository := campaign_repository.NewCampaignRepository(db)
	input := campaign_repository.DraftInput{
		Name: "Device image", ContentType: campaign_model.CampaignContentImage, TextBody: "Optional caption", MediaAssetID: asset.ID,
		Recipients: []campaign_repository.RecipientConsent{{
			JID: "15550001111@s.whatsapp.net", OptInSource: "integration_test", EvidenceReference: "consent-image",
			OptedInAt: time.Now().Add(-time.Hour),
		}}, Actor: campaign_repository.Actor{Type: "system"},
	}
	campaign, _, err := repository.CreateDraft(context.Background(), instances[0].Id, input)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.ContentType != campaign_model.CampaignContentImage || campaign.MediaAssetID == nil || *campaign.MediaAssetID != asset.ID ||
		campaign.MediaMIMEType == nil || *campaign.MediaMIMEType != "image/png" || campaign.MediaSizeBytes == nil || *campaign.MediaSizeBytes != 2048 ||
		campaign.MediaWidth == nil || *campaign.MediaWidth != 800 || campaign.MediaHeight == nil || *campaign.MediaHeight != 600 ||
		campaign.MediaSHA256 == nil || *campaign.MediaSHA256 != "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" || campaign.TextBody != "Optional caption" {
		t.Fatalf("campaign snapshot=%+v", campaign)
	}
	if _, err := mediaRepository.ClaimDelete(context.Background(), instances[0].Id, asset.ID, time.Minute); !errors.Is(err, campaign_repository.ErrMediaAssetConflict) {
		t.Fatalf("referenced asset deletion error=%v", err)
	}
	if err := db.Exec("UPDATE campaign_media_assets SET expires_at = created_at + INTERVAL '1 millisecond' WHERE id = ?", asset.ID).Error; err != nil {
		t.Fatal(err)
	}
	claimed, err := mediaRepository.ClaimExpired(context.Background(), 100, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, expired := range claimed {
		if expired.ID == asset.ID {
			t.Fatal("cleanup claimed an asset referenced by a campaign")
		}
	}

	claimedAssetID := uuid.NewString()
	claimedAsset, _, err := mediaRepository.CreateUploading(context.Background(), campaign_repository.CreateMediaAssetInput{
		ID: claimedAssetID, InstanceID: instances[0].Id,
		ObjectKey: "campaign-media/" + instances[0].Id + "/" + claimedAssetID + "/image", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	claimedAsset, err = mediaRepository.MarkReady(context.Background(), instances[0].Id, claimedAsset.ID, campaign_repository.ReadyMediaAssetInput{
		MIMEType: "image/jpeg", SizeBytes: 1024, Width: 400, Height: 300,
		SHA256: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
	})
	if err != nil {
		t.Fatal(err)
	}
	claimedAsset, err = mediaRepository.ClaimDelete(context.Background(), instances[0].Id, claimedAsset.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claimedInput := input
	claimedInput.Name, claimedInput.MediaAssetID = "Cleanup race", claimedAsset.ID
	if _, _, err := repository.CreateDraft(context.Background(), instances[0].Id, claimedInput); !errors.Is(err, campaign_repository.ErrMediaAssetConflict) {
		t.Fatalf("cleanup-claimed image create error=%v", err)
	}
	if err := mediaRepository.ReleaseCleanup(context.Background(), claimedAsset); err != nil {
		t.Fatal(err)
	}

	crossInstance := input
	crossInstance.Name = "Cross instance"
	if _, _, err := repository.CreateDraft(context.Background(), instances[1].Id, crossInstance); !errors.Is(err, campaign_repository.ErrMediaAssetNotFound) {
		t.Fatalf("cross-instance image create error=%v", err)
	}
}
