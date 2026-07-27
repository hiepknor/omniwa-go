package media_repository

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresMediaAssetLifecycleIsInstanceScopedAndCleanupFenced(t *testing.T) {
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
	instance := instance_model.Instance{Name: "media-foundation-" + suffix, Token: "media-foundation-token-" + suffix}
	other := instance_model.Instance{Name: "media-foundation-other-" + suffix, Token: "media-foundation-other-token-" + suffix}
	if err := db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	assetID := uuid.NewString()
	t.Cleanup(func() {
		_ = db.Where("media_asset_id = ?", assetID).Delete(&media_model.AssetReference{}).Error
		_ = db.Where("media_asset_id = ?", assetID).Delete(&media_model.AssetVariant{}).Error
		_ = db.Unscoped().Where("id = ?", assetID).Delete(&media_model.Asset{}).Error
		_ = db.Where("id IN ?", []string{instance.Id, other.Id}).Delete(&instance_model.Instance{}).Error
	})

	repository := New(db)
	ctx := context.Background()
	expires := time.Now().UTC().Add(time.Hour)
	requestHash := strings.Repeat("1", 64)
	created, inserted, err := repository.Create(ctx, CreateAssetInput{
		ID: assetID, InstanceID: instance.Id, Origin: media_model.AssetOriginDeviceUpload,
		Status: media_model.AssetStatusUploading, RequestReferenceHash: &requestHash, ExpiresAt: &expires,
	})
	if err != nil || !inserted || created.ID != assetID {
		t.Fatalf("create = %+v, inserted=%t, err=%v", created, inserted, err)
	}
	replayed, inserted, err := repository.Create(ctx, CreateAssetInput{
		ID: uuid.NewString(), InstanceID: instance.Id, Origin: media_model.AssetOriginDeviceUpload,
		Status: media_model.AssetStatusUploading, RequestReferenceHash: &requestHash, ExpiresAt: &expires,
	})
	if err != nil || inserted || replayed.ID != assetID {
		t.Fatalf("idempotent replay = %+v, inserted=%t, err=%v", replayed, inserted, err)
	}
	if _, err := repository.Get(ctx, other.Id, assetID); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("cross-instance read error = %v", err)
	}
	if _, err := repository.MarkReady(ctx, instance.Id, assetID); !errors.Is(err, ErrAssetConflict) {
		t.Fatalf("ready without canonical error = %v", err)
	}
	variant := media_model.AssetVariant{
		MediaAssetID: assetID, InstanceID: instance.Id, Kind: media_model.VariantCanonical,
		ObjectKey: "media-assets/" + instance.Id + "/" + assetID + "/canonical", MIMEType: "image/jpeg",
		SizeBytes: 128, Width: 16, Height: 8, SHA256: strings.Repeat("a", 64),
	}
	if err := repository.AddVariant(ctx, variant); err != nil {
		t.Fatal(err)
	}
	ready, err := repository.MarkReady(ctx, instance.Id, assetID)
	if err != nil || ready.Canonical == nil || ready.Canonical.ObjectKey != variant.ObjectKey {
		t.Fatalf("ready asset = %+v, err=%v", ready, err)
	}
	plan, err := repository.PlanInstancePurge(ctx, instance.Id)
	if err != nil || len(plan.AssetIDs) != 1 || plan.AssetIDs[0] != assetID || len(plan.Variants) != 1 || plan.Variants[0].ObjectKey != variant.ObjectKey {
		t.Fatalf("instance purge plan = %+v, err=%v", plan, err)
	}
	otherPlan, err := repository.PlanInstancePurge(ctx, other.Id)
	if err != nil || len(otherPlan.AssetIDs) != 0 || len(otherPlan.Variants) != 0 {
		t.Fatalf("cross-instance purge plan = %+v, err=%v", otherPlan, err)
	}
	retention := time.Now().UTC().Add(90 * 24 * time.Hour)
	if err := repository.AddReference(ctx, media_model.AssetReference{
		InstanceID: instance.Id, MediaAssetID: assetID, OwnerType: media_model.ReferenceOwnerMessage,
		OwnerID: "message-1", RetentionUntil: &retention,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&media_model.Asset{}).Where("id = ?", assetID).Updates(map[string]any{
		"created_at": time.Now().UTC().Add(-2 * time.Hour), "expires_at": time.Now().UTC().Add(-time.Hour),
	}).Error; err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.ClaimExpired(ctx, 10, time.Minute)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("referenced cleanup claim = %d, err=%v", len(claimed), err)
	}
	if err := repository.RemoveReference(ctx, instance.Id, assetID, media_model.ReferenceOwnerMessage, "message-1"); err != nil {
		t.Fatal(err)
	}
	claimed, err = repository.ClaimExpired(ctx, 10, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].ID != assetID || claimed[0].CleanupClaimToken == nil {
		t.Fatalf("unreferenced cleanup claim = %+v, err=%v", claimed, err)
	}
	if err := repository.AddReference(ctx, media_model.AssetReference{
		InstanceID: instance.Id, MediaAssetID: assetID, OwnerType: media_model.ReferenceOwnerCampaign, OwnerID: uuid.NewString(),
	}); !errors.Is(err, ErrAssetConflict) {
		t.Fatalf("reference during cleanup error = %v", err)
	}
	stale := claimed[0]
	staleToken := uuid.NewString()
	stale.CleanupClaimToken = &staleToken
	if err := repository.CompleteCleanup(ctx, &stale); !errors.Is(err, ErrAssetConflict) {
		t.Fatalf("stale cleanup token error = %v", err)
	}
	if err := repository.CompleteCleanup(ctx, &claimed[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Get(ctx, instance.Id, assetID); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("deleted asset read error = %v", err)
	}
}
