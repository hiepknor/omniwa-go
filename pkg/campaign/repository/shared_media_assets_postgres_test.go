package campaign_repository

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	media_repository "github.com/evolution-foundation/evolution-go/pkg/media/repository"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresSharedCampaignMediaRepositoryMirrorsRollbackState(t *testing.T) {
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
	instance := instance_model.Instance{Name: "shared-campaign-media-" + suffix, Token: "shared-campaign-media-token-" + suffix}
	other := instance_model.Instance{Name: "shared-campaign-media-other-" + suffix, Token: "shared-campaign-media-other-token-" + suffix}
	if err := db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	assetID := uuid.NewString()
	t.Cleanup(func() {
		_ = db.Where("instance_id = ?", instance.Id).Delete(&campaign_model.AuditEvent{}).Error
		_ = db.Where("instance_id = ?", instance.Id).Delete(&campaign_model.Recipient{}).Error
		_ = db.Where("instance_id = ?", instance.Id).Delete(&campaign_model.Campaign{}).Error
		_ = db.Where("instance_id = ?", instance.Id).Delete(&media_model.AssetReference{}).Error
		_ = db.Where("instance_id = ?", instance.Id).Delete(&media_model.AssetVariant{}).Error
		_ = db.Unscoped().Where("instance_id = ?", instance.Id).Delete(&media_model.Asset{}).Error
		_ = db.Unscoped().Where("instance_id = ?", instance.Id).Delete(&campaign_model.MediaAsset{}).Error
		_ = db.Where("id IN ?", []string{instance.Id, other.Id}).Delete(&instance_model.Instance{}).Error
	})

	repository := NewSharedMediaAssetRepository(db)
	ctx := context.Background()
	expires := time.Now().UTC().Add(time.Hour)
	requestHash := strings.Repeat("2", 64)
	uploading, created, err := repository.CreateUploading(ctx, CreateMediaAssetInput{
		ID: assetID, InstanceID: instance.Id, RequestReferenceHash: &requestHash, ExpiresAt: expires,
	})
	if err != nil || !created || uploading.ObjectKey != sharedCanonicalObjectKey(instance.Id, assetID) {
		t.Fatalf("create uploading = %+v created=%t err=%v", uploading, created, err)
	}
	var shared media_model.Asset
	var legacy campaign_model.MediaAsset
	if err := db.First(&shared, "id = ? AND instance_id = ?", assetID, instance.Id).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&legacy, "id = ? AND instance_id = ?", assetID, instance.Id).Error; err != nil {
		t.Fatal(err)
	}
	if shared.Status != media_model.AssetStatusUploading || legacy.Status != campaign_model.MediaAssetStatusUploading || legacy.ObjectKey != uploading.ObjectKey {
		t.Fatalf("upload shadows diverged: shared=%+v legacy=%+v", shared, legacy)
	}
	purgePlan, err := media_repository.New(db).PlanInstancePurge(ctx, instance.Id)
	if err != nil || len(purgePlan.Variants) != 1 || purgePlan.Variants[0].ObjectKey != uploading.ObjectKey {
		t.Fatalf("uploading rollback purge plan=%+v err=%v", purgePlan, err)
	}
	ready, err := repository.MarkReady(ctx, instance.Id, assetID, ReadyMediaAssetInput{
		MIMEType: "image/jpeg", SizeBytes: 128, Width: 16, Height: 8, SHA256: strings.Repeat("a", 64),
	})
	if err != nil || ready.Status != campaign_model.MediaAssetStatusReady || ready.MIMEType == nil || ready.ObjectKey != uploading.ObjectKey {
		t.Fatalf("mark ready = %+v err=%v", ready, err)
	}
	purgePlan, err = media_repository.New(db).PlanInstancePurge(ctx, instance.Id)
	if err != nil || len(purgePlan.Variants) != 1 || purgePlan.Variants[0].ObjectKey != uploading.ObjectKey || purgePlan.Variants[0].MIMEType != "image/jpeg" {
		t.Fatalf("ready purge plan=%+v err=%v", purgePlan, err)
	}
	replayed, created, err := repository.CreateUploading(ctx, CreateMediaAssetInput{
		ID: uuid.NewString(), InstanceID: instance.Id, RequestReferenceHash: &requestHash, ExpiresAt: expires,
	})
	if err != nil || created || replayed.ID != assetID || replayed.MIMEType == nil || replayed.SHA256 == nil {
		t.Fatalf("ready replay = %+v created=%t err=%v", replayed, created, err)
	}
	if _, err := repository.Get(ctx, other.Id, assetID); !errors.Is(err, ErrMediaAssetNotFound) {
		t.Fatalf("cross-instance read error = %v", err)
	}
	campaign, _, err := NewCampaignRepository(db).CreateDraft(ctx, instance.Id, DraftInput{
		Name: "Shared asset campaign", ContentType: campaign_model.CampaignContentImage,
		TextBody: "Caption", MediaAssetID: assetID,
		Recipients: []RecipientConsent{{
			JID: "15550001111@s.whatsapp.net", OptInSource: "integration_test",
			EvidenceReference: "shared-media-consent", OptedInAt: time.Now().Add(-time.Hour),
		}},
		Actor: Actor{Type: "system"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var reference media_model.AssetReference
	if err := db.Where(
		"instance_id = ? AND media_asset_id = ? AND owner_type = ? AND owner_id = ?",
		instance.Id, assetID, media_model.ReferenceOwnerCampaign, campaign.ID,
	).First(&reference).Error; err != nil {
		t.Fatalf("campaign shared media reference: %v", err)
	}
	if _, err := repository.ClaimDelete(ctx, instance.Id, assetID, time.Minute); !errors.Is(err, ErrMediaAssetConflict) {
		t.Fatalf("referenced delete claim error = %v", err)
	}
	if err := db.Where("instance_id = ? AND media_asset_id = ? AND owner_type = ? AND owner_id = ?", instance.Id, assetID, media_model.ReferenceOwnerCampaign, campaign.ID).Delete(&media_model.AssetReference{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("campaign_id = ?", campaign.ID).Delete(&campaign_model.AuditEvent{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("campaign_id = ?", campaign.ID).Delete(&campaign_model.Recipient{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&campaign_model.Campaign{}, "id = ?", campaign.ID).Error; err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.ClaimDelete(ctx, instance.Id, assetID, time.Minute)
	if err != nil || claimed.CleanupClaimToken == nil {
		t.Fatalf("delete claim = %+v err=%v", claimed, err)
	}
	if err := repository.CompleteCleanup(ctx, claimed); err != nil {
		t.Fatal(err)
	}
	if err := db.Unscoped().First(&shared, "id = ?", assetID).Error; err != nil || shared.Status != media_model.AssetStatusDeleted || shared.DeletedAt == nil {
		t.Fatalf("shared cleanup state = %+v err=%v", shared, err)
	}
	if err := db.Unscoped().First(&legacy, "id = ?", assetID).Error; err != nil || legacy.Status != campaign_model.MediaAssetStatusDeleted || legacy.DeletedAt == nil {
		t.Fatalf("legacy cleanup state = %+v err=%v", legacy, err)
	}

	releaseID := uuid.NewString()
	released, _, err := repository.CreateUploading(ctx, CreateMediaAssetInput{ID: releaseID, InstanceID: instance.Id, ExpiresAt: expires})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.MarkReady(ctx, instance.Id, releaseID, ReadyMediaAssetInput{
		MIMEType: "image/png", SizeBytes: 64, Width: 8, Height: 8, SHA256: strings.Repeat("b", 64),
	}); err != nil {
		t.Fatal(err)
	}
	released, err = repository.ClaimDelete(ctx, instance.Id, releaseID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ReleaseCleanup(ctx, released); err != nil {
		t.Fatal(err)
	}
	shared = media_model.Asset{}
	if err := db.First(&shared, "id = ?", releaseID).Error; err != nil || shared.Status != media_model.AssetStatusFailed || shared.CleanupClaimToken != nil {
		t.Fatalf("shared released state = %+v err=%v", shared, err)
	}
	legacy = campaign_model.MediaAsset{}
	if err := db.First(&legacy, "id = ?", releaseID).Error; err != nil || legacy.Status != campaign_model.MediaAssetStatusFailed || legacy.CleanupClaimToken != nil {
		t.Fatalf("legacy released state = %+v err=%v", legacy, err)
	}
	if err := db.Where("media_asset_id = ?", releaseID).Delete(&media_model.AssetVariant{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&media_model.Asset{}, "id = ?", releaseID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&campaign_model.MediaAsset{}, "id = ?", releaseID).Error; err != nil {
		t.Fatal(err)
	}
}
