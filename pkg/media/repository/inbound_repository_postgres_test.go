package media_repository

import (
	"bytes"
	"context"
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

func TestPostgresInboundCaptureLeaseCompletionAndSecretErasure(t *testing.T) {
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
	instance := instance_model.Instance{Name: "inbound-media-" + uuid.NewString(), Token: "inbound-media-token-" + uuid.NewString()}
	if err := db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	assetID, jobID := uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM media_asset_audit_events WHERE instance_id = ?", instance.Id).Error
		_ = db.Exec("DELETE FROM media_asset_references WHERE instance_id = ?", instance.Id).Error
		_ = db.Exec("DELETE FROM media_download_jobs WHERE instance_id = ?", instance.Id).Error
		_ = db.Exec("DELETE FROM media_asset_variants WHERE instance_id = ?", instance.Id).Error
		_ = db.Exec("DELETE FROM media_assets WHERE instance_id = ?", instance.Id).Error
		_ = db.Delete(&instance).Error
	})

	repository := NewInbound(db)
	ctx := context.Background()
	input := CaptureInboundInput{
		AssetID: assetID, JobID: jobID, InstanceID: instance.Id, MessageID: "message-inbound-1",
		DescriptorCiphertext: bytes.Repeat([]byte{7}, 48), DescriptorNonce: bytes.Repeat([]byte{8}, 12),
		DescriptorKeyVersion: 2, MaxAttempts: 3, ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	asset, created, err := repository.Capture(ctx, input)
	if err != nil || !created || asset.ID != assetID || asset.Status != media_model.AssetStatusDownloading {
		t.Fatalf("capture=%+v/%t/%v", asset, created, err)
	}
	replay := input
	replay.AssetID, replay.JobID = uuid.NewString(), uuid.NewString()
	asset, created, err = repository.Capture(ctx, replay)
	if err != nil || created || asset.ID != assetID {
		t.Fatalf("replay=%+v/%t/%v", asset, created, err)
	}
	jobs, err := repository.ClaimDownloads(ctx, 1, time.Minute)
	if err != nil || len(jobs) != 1 || jobs[0].AttemptCount != 1 || jobs[0].ClaimToken == nil {
		t.Fatalf("claim=%+v/%v", jobs, err)
	}
	pastCreated, pastExpiry := time.Now().UTC().Add(-2*time.Hour), time.Now().UTC().Add(-time.Hour)
	if err := db.Model(&media_model.Asset{}).Where("id = ?", assetID).Updates(map[string]any{"created_at": pastCreated, "expires_at": pastExpiry}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&media_model.AssetReference{}).Where("media_asset_id = ?", assetID).Update("retention_until", pastExpiry).Error; err != nil {
		t.Fatal(err)
	}
	if cleanupClaims, err := New(db).ClaimExpired(ctx, 1, time.Minute); err != nil || len(cleanupClaims) != 0 {
		t.Fatalf("active download entered cleanup: %+v/%v", cleanupClaims, err)
	}
	if err := db.Model(&media_model.DownloadJob{}).Where("id = ?", jobID).Updates(map[string]any{
		"attempt_count": 3, "max_attempts": 3, "lease_until": time.Now().UTC().Add(-time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	jobs, err = repository.ClaimDownloads(ctx, 1, time.Minute)
	if err != nil || len(jobs) != 1 || jobs[0].AttemptCount != 3 || jobs[0].ClaimToken == nil {
		t.Fatalf("stale max-attempt lease was not reclaimed safely: %+v/%v", jobs, err)
	}
	original := media_model.AssetVariant{
		MediaAssetID: assetID, InstanceID: instance.Id, Kind: media_model.VariantProviderOriginal,
		ObjectKey: "media-assets/" + instance.Id + "/" + assetID + "/provider_original", MIMEType: "image/png",
		SizeBytes: 100, Width: 5, Height: 5, SHA256: strings.Repeat("a", 64),
	}
	canonical := original
	canonical.Kind = media_model.VariantCanonical
	canonical.ObjectKey = "media-assets/" + instance.Id + "/" + assetID + "/canonical"
	canonical.SHA256 = strings.Repeat("b", 64)
	if err := repository.CompleteDownload(ctx, &jobs[0], original, canonical); err != nil {
		t.Fatal(err)
	}
	var completed media_model.DownloadJob
	if err := db.First(&completed, "id = ?", jobID).Error; err != nil {
		t.Fatal(err)
	}
	if completed.Status != media_model.DownloadJobCompleted || completed.CompletedAt == nil || completed.DescriptorCiphertext != nil ||
		completed.DescriptorNonce != nil || completed.DescriptorKeyVersion != nil || completed.ClaimToken != nil {
		t.Fatalf("completed job retained secret or lease: %+v", completed)
	}
	ready, err := New(db).Get(ctx, instance.Id, assetID)
	if err != nil || ready.Status != media_model.AssetStatusReady || ready.Canonical == nil {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	var auditCount int64
	if err := db.Table("media_asset_audit_events").Where("instance_id = ? AND media_asset_id = ?", instance.Id, assetID).Count(&auditCount).Error; err != nil || auditCount != 2 {
		t.Fatalf("audit count=%d err=%v", auditCount, err)
	}

	failedAssetID, failedJobID := uuid.NewString(), uuid.NewString()
	failedInput := input
	failedInput.AssetID, failedInput.JobID, failedInput.MessageID = failedAssetID, failedJobID, "message-inbound-2"
	failedInput.ExpiresAt = time.Now().UTC().Add(time.Hour)
	if _, created, err := repository.Capture(ctx, failedInput); err != nil || !created {
		t.Fatalf("failed-path capture=%t/%v", created, err)
	}
	failedJobs, err := repository.ClaimDownloads(ctx, 1, time.Minute)
	if err != nil || len(failedJobs) != 1 || failedJobs[0].ID != failedJobID {
		t.Fatalf("failed-path claim=%+v/%v", failedJobs, err)
	}
	if err := repository.FailDownload(ctx, &failedJobs[0], "provider_media_expired"); err != nil {
		t.Fatal(err)
	}
	var terminal media_model.DownloadJob
	if err := db.First(&terminal, "id = ?", failedJobID).Error; err != nil {
		t.Fatal(err)
	}
	if terminal.Status != media_model.DownloadJobFailed || terminal.CompletedAt != nil || terminal.DescriptorCiphertext != nil ||
		terminal.DescriptorNonce != nil || terminal.DescriptorKeyVersion != nil || terminal.ClaimToken != nil {
		t.Fatalf("failed job retained secret or lease: %+v", terminal)
	}
	failedAsset, err := New(db).Get(ctx, instance.Id, failedAssetID)
	if err != nil || failedAsset.Status != media_model.AssetStatusFailed || failedAsset.FailureCode == nil || *failedAsset.FailureCode != "provider_media_expired" {
		t.Fatalf("failed asset=%+v err=%v", failedAsset, err)
	}
}
