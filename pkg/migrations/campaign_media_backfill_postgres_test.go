package migrations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	campaign_repository "github.com/evolution-foundation/evolution-go/pkg/campaign/repository"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	media_repository "github.com/evolution-foundation/evolution-go/pkg/media/repository"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestCampaignMediaBackfillCopiesReadyAssetsAndReferences(t *testing.T) {
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
	if err := Run(db); err != nil {
		t.Fatal(err)
	}

	migration := registeredMigration(t, 27)
	ctx := context.Background()
	rollback := errors.New("rollback campaign media backfill fixture")
	err = db.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC().Truncate(time.Microsecond)
		instance := instance_model.Instance{
			Name:  "campaign-media-backfill-" + uuid.NewString(),
			Token: "campaign-media-backfill-token-" + uuid.NewString(),
		}
		if err := tx.Create(&instance).Error; err != nil {
			return err
		}

		assetID := uuid.NewString()
		mimeType := "image/png"
		sizeBytes := int64(2048)
		width, height := 800, 600
		sha256 := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		readyAt := now.Add(time.Second)
		legacy := campaign_model.MediaAsset{
			ID: assetID, InstanceID: instance.Id,
			ObjectKey: "campaign-media/" + instance.Id + "/" + assetID + "/image",
			MediaType: "image", MIMEType: &mimeType, SizeBytes: &sizeBytes,
			Width: &width, Height: &height, SHA256: &sha256,
			Status: campaign_model.MediaAssetStatusReady, ReadyAt: &readyAt,
			ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: readyAt,
		}
		if err := tx.Create(&legacy).Error; err != nil {
			return err
		}
		uploadingID := uuid.NewString()
		uploadingKey := "campaign-media/" + instance.Id + "/" + uploadingID + "/image"
		if err := tx.Create(&campaign_model.MediaAsset{
			ID: uploadingID, InstanceID: instance.Id, ObjectKey: uploadingKey,
			MediaType: "image", Status: campaign_model.MediaAssetStatusUploading,
			ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			return err
		}

		campaign := campaign_model.Campaign{
			ID: uuid.NewString(), InstanceID: instance.Id, Name: "Backfilled image campaign",
			Status: campaign_model.CampaignStatusDraft, ContentType: campaign_model.CampaignContentImage,
			TextBody: "Caption", MediaAssetID: &assetID, MediaMIMEType: &mimeType,
			MediaSizeBytes: &sizeBytes, MediaWidth: &width, MediaHeight: &height, MediaSHA256: &sha256,
			TargetType: campaign_model.CampaignTargetDirect, Version: 1,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&campaign).Error; err != nil {
			return err
		}

		if err := tx.Exec(migration.SQL).Error; err != nil {
			return err
		}

		var shared media_model.Asset
		if err := tx.Where("id = ? AND instance_id = ?", assetID, instance.Id).First(&shared).Error; err != nil {
			return err
		}
		if shared.Origin != media_model.AssetOriginDeviceUpload || shared.Status != media_model.AssetStatusReady ||
			shared.ReadyAt == nil || !shared.ReadyAt.Equal(readyAt) || shared.ExpiresAt == nil || !shared.ExpiresAt.Equal(legacy.ExpiresAt) {
			return fmt.Errorf("shared asset=%+v", shared)
		}

		var variant media_model.AssetVariant
		if err := tx.Where("media_asset_id = ? AND instance_id = ? AND variant = ?", assetID, instance.Id, media_model.VariantCanonical).First(&variant).Error; err != nil {
			return err
		}
		if variant.ObjectKey != legacy.ObjectKey || variant.MIMEType != mimeType || variant.SizeBytes != sizeBytes ||
			variant.Width != width || variant.Height != height || variant.SHA256 != sha256 {
			return fmt.Errorf("canonical variant=%+v", variant)
		}

		var reference media_model.AssetReference
		if err := tx.Where(
			"instance_id = ? AND media_asset_id = ? AND owner_type = ? AND owner_id = ?",
			instance.Id, assetID, media_model.ReferenceOwnerCampaign, campaign.ID,
		).First(&reference).Error; err != nil {
			return err
		}
		if reference.RetentionUntil != nil {
			return fmt.Errorf("campaign reference retention=%v", reference.RetentionUntil)
		}
		uploading, err := campaign_repository.NewSharedMediaAssetRepository(tx).Get(ctx, instance.Id, uploadingID)
		if err != nil || uploading.Status != campaign_model.MediaAssetStatusUploading || uploading.ObjectKey != uploadingKey {
			return fmt.Errorf("backfilled uploading view=%+v err=%v", uploading, err)
		}
		plan, err := media_repository.New(tx).PlanInstancePurge(ctx, instance.Id)
		if err != nil {
			return err
		}
		foundUploadingKey := false
		for _, item := range plan.Variants {
			foundUploadingKey = foundUploadingKey || item.ObjectKey == uploadingKey
		}
		if !foundUploadingKey {
			return fmt.Errorf("instance purge plan omitted uploading legacy key: %+v", plan)
		}

		// The migration must remain replay-safe during staged deploys.
		if err := tx.Exec(migration.SQL).Error; err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}

	err = db.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC().Truncate(time.Microsecond)
		instance := instance_model.Instance{Name: "campaign-media-backfill-conflict-" + uuid.NewString(), Token: "campaign-media-backfill-conflict-token-" + uuid.NewString()}
		if err := tx.Create(&instance).Error; err != nil {
			return err
		}
		assetID := uuid.NewString()
		if err := tx.Create(&campaign_model.MediaAsset{
			ID: assetID, InstanceID: instance.Id,
			ObjectKey: "campaign-media/" + instance.Id + "/" + assetID + "/image",
			MediaType: "image", Status: campaign_model.MediaAssetStatusUploading,
			ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Create(&media_model.Asset{
			ID: assetID, InstanceID: instance.Id, MediaType: "image",
			Origin: media_model.AssetOriginDeviceUpload, Status: media_model.AssetStatusUploading,
			ExpiresAt: timePointerForMigration(now.Add(2 * time.Hour)), CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			return err
		}
		return tx.Exec(migration.SQL).Error
	})
	if err == nil || !strings.Contains(err.Error(), "campaign media asset backfill identity mismatch") {
		t.Fatalf("metadata collision error=%v", err)
	}
}

func timePointerForMigration(value time.Time) *time.Time { return &value }
