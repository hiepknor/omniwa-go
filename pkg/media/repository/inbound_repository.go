package media_repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrDownloadClaimLost = errors.New("media download claim is no longer active")

type CaptureInboundInput struct {
	AssetID              string
	JobID                string
	InstanceID           string
	MessageID            string
	DescriptorCiphertext []byte
	DescriptorNonce      []byte
	DescriptorKeyVersion int
	MaxAttempts          int
	ExpiresAt            time.Time
	ProviderExpiresAt    *time.Time
}

type InboundRepository interface {
	Capture(context.Context, CaptureInboundInput) (*media_model.Asset, bool, error)
	ClaimDownloads(context.Context, int, time.Duration) ([]media_model.DownloadJob, error)
	RetryDownload(context.Context, *media_model.DownloadJob, string, time.Time) error
	FailDownload(context.Context, *media_model.DownloadJob, string) error
	CompleteDownload(context.Context, *media_model.DownloadJob, media_model.AssetVariant, media_model.AssetVariant) error
}

type inboundRepository struct {
	db  *gorm.DB
	now func() time.Time
}

func NewInbound(db *gorm.DB) InboundRepository { return &inboundRepository{db: db, now: time.Now} }

func (r *inboundRepository) Capture(ctx context.Context, input CaptureInboundInput) (*media_model.Asset, bool, error) {
	if err := validateCapture(r, ctx, input); err != nil {
		return nil, false, err
	}
	var result *media_model.Asset
	created := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing media_model.DownloadJob
		err := tx.Where("instance_id = ? AND message_id = ?", input.InstanceID, input.MessageID).First(&existing).Error
		if err == nil {
			asset, getErr := New(tx).Get(ctx, input.InstanceID, existing.MediaAssetID)
			result = asset
			return getErr
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		assetRepository := New(tx)
		asset, inserted, err := assetRepository.Create(ctx, CreateAssetInput{
			ID: input.AssetID, InstanceID: input.InstanceID, Origin: media_model.AssetOriginWhatsAppInbound,
			Status: media_model.AssetStatusDownloading, ExpiresAt: &input.ExpiresAt,
		})
		if err != nil || !inserted {
			if err != nil {
				return err
			}
			return ErrAssetConflict
		}
		retention := input.ExpiresAt.UTC()
		if err := assetRepository.AddReference(ctx, media_model.AssetReference{
			InstanceID: input.InstanceID, MediaAssetID: input.AssetID, OwnerType: media_model.ReferenceOwnerMessage,
			OwnerID: input.MessageID, RetentionUntil: &retention,
		}); err != nil {
			return err
		}
		now := r.now().UTC()
		keyVersion := input.DescriptorKeyVersion
		job := media_model.DownloadJob{
			ID: input.JobID, InstanceID: input.InstanceID, MediaAssetID: input.AssetID, MessageID: input.MessageID,
			Status: media_model.DownloadJobPending, DescriptorCiphertext: input.DescriptorCiphertext,
			DescriptorNonce: input.DescriptorNonce, DescriptorKeyVersion: &keyVersion,
			MaxAttempts: input.MaxAttempts, NextAttemptAt: now, ProviderExpiresAt: utcPointer(input.ProviderExpiresAt),
			CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&job).Error; err != nil {
			return err
		}
		if err := createMediaAudit(tx, input.InstanceID, input.AssetID, "inbound_capture_queued", map[string]any{"source": "live"}, now); err != nil {
			return err
		}
		result, created = asset, true
		return nil
	})
	if err == nil {
		return result, created, nil
	}
	// A concurrent duplicate may win the unique message constraint after our
	// initial lookup. Return its stable asset identity instead of losing capture.
	var existing media_model.DownloadJob
	if lookup := r.db.WithContext(ctx).Where("instance_id = ? AND message_id = ?", input.InstanceID, input.MessageID).First(&existing).Error; lookup == nil {
		asset, getErr := New(r.db).Get(ctx, input.InstanceID, existing.MediaAssetID)
		return asset, false, getErr
	}
	return nil, false, err
}

func (r *inboundRepository) ClaimDownloads(ctx context.Context, limit int, lease time.Duration) ([]media_model.DownloadJob, error) {
	if r == nil || r.db == nil || r.now == nil || ctx == nil || limit < 1 || limit > 100 || lease <= 0 {
		return nil, errors.New("bounded media download claim is required")
	}
	now := r.now().UTC()
	token := uuid.NewString()
	leaseUntil := now.Add(lease)
	var jobs []media_model.DownloadJob
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw(`WITH candidates AS (
    SELECT id
    FROM media_download_jobs
	WHERE (status IN ('pending', 'retry_wait') AND attempt_count < max_attempts AND next_attempt_at <= ?)
	   OR (status = 'processing' AND attempt_count <= max_attempts AND lease_until <= ?)
    ORDER BY next_attempt_at ASC, id ASC
    FOR UPDATE SKIP LOCKED
    LIMIT ?
)
UPDATE media_download_jobs AS jobs
SET status = 'processing', claim_token = ?, lease_until = ?,
    attempt_count = CASE WHEN jobs.status = 'processing' THEN jobs.attempt_count ELSE jobs.attempt_count + 1 END,
	updated_at = ?
FROM candidates
WHERE jobs.id = candidates.id
RETURNING jobs.*`, now, now, limit, token, leaseUntil, now).Scan(&jobs).Error; err != nil {
			return err
		}
		if len(jobs) == 0 {
			return nil
		}
		assetIDs := make([]string, len(jobs))
		for index := range jobs {
			assetIDs[index] = jobs[index].MediaAssetID
		}
		return tx.Model(&media_model.Asset{}).
			Where("id IN ? AND status = ? AND deleted_at IS NULL", assetIDs, media_model.AssetStatusDownloading).
			Updates(map[string]any{"status": media_model.AssetStatusProcessing, "updated_at": now}).Error
	})
	return jobs, err
}

func (r *inboundRepository) RetryDownload(ctx context.Context, job *media_model.DownloadJob, code string, nextAttemptAt time.Time) error {
	if err := validateDownloadClaim(r, ctx, job, code); err != nil || nextAttemptAt.IsZero() || !nextAttemptAt.After(r.now()) {
		if err != nil {
			return err
		}
		return errors.New("future media download retry time is required")
	}
	now := r.now().UTC()
	result := r.claimedJob(ctx, job).Updates(map[string]any{
		"status": media_model.DownloadJobRetryWait, "next_attempt_at": nextAttemptAt.UTC(), "last_error_code": code,
		"claim_token": nil, "lease_until": nil, "updated_at": now,
	})
	return downloadClaimResult(result)
}

func (r *inboundRepository) FailDownload(ctx context.Context, job *media_model.DownloadJob, code string) error {
	if err := validateDownloadClaim(r, ctx, job, code); err != nil {
		return err
	}
	now := r.now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := claimedJobQuery(tx, job).Updates(map[string]any{
			"status": media_model.DownloadJobFailed, "last_error_code": code, "claim_token": nil, "lease_until": nil,
			"descriptor_ciphertext": nil, "descriptor_nonce": nil, "descriptor_key_version": nil, "updated_at": now,
		})
		if err := downloadClaimResult(result); err != nil {
			return err
		}
		if err := tx.Model(&media_model.Asset{}).Where("instance_id = ? AND id = ? AND status IN ? AND deleted_at IS NULL", job.InstanceID, job.MediaAssetID,
			[]media_model.AssetStatus{media_model.AssetStatusDownloading, media_model.AssetStatusProcessing}).
			Updates(map[string]any{"status": media_model.AssetStatusFailed, "failure_code": code, "updated_at": now}).Error; err != nil {
			return err
		}
		return createMediaAudit(tx, job.InstanceID, job.MediaAssetID, "inbound_download_failed", map[string]any{"errorCode": code, "attempt": job.AttemptCount}, now)
	})
}

func (r *inboundRepository) CompleteDownload(ctx context.Context, job *media_model.DownloadJob, original, canonical media_model.AssetVariant) error {
	if err := validateDownloadClaim(r, ctx, job, "download_completed"); err != nil ||
		original.Kind != media_model.VariantProviderOriginal || canonical.Kind != media_model.VariantCanonical ||
		original.MediaAssetID != job.MediaAssetID || canonical.MediaAssetID != job.MediaAssetID ||
		original.InstanceID != job.InstanceID || canonical.InstanceID != job.InstanceID {
		if err != nil {
			return err
		}
		return errors.New("complete media download variants are invalid")
	}
	now := r.now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked media_model.DownloadJob
		if err := claimedJobQuery(tx.Clauses(clause.Locking{Strength: "UPDATE"}), job).First(&locked).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrDownloadClaimLost
			}
			return err
		}
		assetRepository := New(tx)
		if err := assetRepository.AddVariant(ctx, original); err != nil {
			return err
		}
		if err := assetRepository.AddVariant(ctx, canonical); err != nil {
			return err
		}
		if _, err := assetRepository.MarkReady(ctx, job.InstanceID, job.MediaAssetID); err != nil {
			return err
		}
		if err := tx.Model(&media_model.DownloadJob{}).Where("id = ? AND claim_token = ?", job.ID, *job.ClaimToken).Updates(map[string]any{
			"status": media_model.DownloadJobCompleted, "claim_token": nil, "lease_until": nil, "last_error_code": nil,
			"descriptor_ciphertext": nil, "descriptor_nonce": nil, "descriptor_key_version": nil,
			"completed_at": now, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Table("projected_messages").Where("instance_id = ? AND message_id = ? AND media_type = 'image'", job.InstanceID, job.MessageID).
			Update("media_asset_id", job.MediaAssetID).Error; err != nil {
			return err
		}
		return createMediaAudit(tx, job.InstanceID, job.MediaAssetID, "inbound_download_completed", map[string]any{"attempt": job.AttemptCount}, now)
	})
}

func (r *inboundRepository) claimedJob(ctx context.Context, job *media_model.DownloadJob) *gorm.DB {
	return claimedJobQuery(r.db.WithContext(ctx), job)
}

func claimedJobQuery(db *gorm.DB, job *media_model.DownloadJob) *gorm.DB {
	return db.Model(&media_model.DownloadJob{}).Where("id = ? AND instance_id = ? AND status = ? AND claim_token = ?", job.ID, job.InstanceID, media_model.DownloadJobProcessing, *job.ClaimToken)
}

func downloadClaimResult(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrDownloadClaimLost
	}
	return nil
}

func validateCapture(r *inboundRepository, ctx context.Context, input CaptureInboundInput) error {
	if r == nil || r.db == nil || r.now == nil || ctx == nil || uuid.Validate(input.AssetID) != nil || uuid.Validate(input.JobID) != nil ||
		uuid.Validate(input.InstanceID) != nil || strings.TrimSpace(input.MessageID) == "" || len(input.MessageID) > 255 ||
		len(input.DescriptorCiphertext) < 17 || len(input.DescriptorNonce) != 12 || input.DescriptorKeyVersion < 1 ||
		input.MaxAttempts < 1 || input.MaxAttempts > 20 || !input.ExpiresAt.After(r.now()) {
		return errors.New("bounded encrypted inbound media capture is required")
	}
	return nil
}

func validateDownloadClaim(r *inboundRepository, ctx context.Context, job *media_model.DownloadJob, code string) error {
	if r == nil || r.db == nil || r.now == nil || ctx == nil || job == nil || uuid.Validate(job.ID) != nil || uuid.Validate(job.InstanceID) != nil ||
		uuid.Validate(job.MediaAssetID) != nil || job.Status != media_model.DownloadJobProcessing || job.ClaimToken == nil || uuid.Validate(*job.ClaimToken) != nil ||
		!validCode(code) {
		return errors.New("claimed media download job and bounded code are required")
	}
	return nil
}

func createMediaAudit(tx *gorm.DB, instanceID, assetID, eventType string, details map[string]any, occurredAt time.Time) error {
	encoded, err := json.Marshal(details)
	if err != nil {
		return err
	}
	return tx.Exec(`INSERT INTO media_asset_audit_events
    (id, instance_id, media_asset_id, event_type, actor_type, details, occurred_at)
VALUES (?, ?, ?, ?, 'system', ?::jsonb, ?)`, uuid.NewString(), instanceID, assetID, eventType, string(encoded), occurredAt.UTC()).Error
}
