package campaign_repository

import (
	"context"
	"errors"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrMediaAssetNotFound = errors.New("campaign media asset not found")
	ErrMediaAssetConflict = errors.New("campaign media asset state conflict")
)

type CreateMediaAssetInput struct {
	ID                   string
	InstanceID           string
	ObjectKey            string
	RequestReferenceHash *string
	ExpiresAt            time.Time
}

type ReadyMediaAssetInput struct {
	MIMEType  string
	SizeBytes int64
	Width     int
	Height    int
	SHA256    string
}

type MediaAssetRepository interface {
	CreateUploading(context.Context, CreateMediaAssetInput) (*campaign_model.MediaAsset, bool, error)
	Get(context.Context, string, string) (*campaign_model.MediaAsset, error)
	MarkReady(context.Context, string, string, ReadyMediaAssetInput) (*campaign_model.MediaAsset, error)
	MarkFailed(context.Context, string, string) error
	ClaimDelete(context.Context, string, string, time.Duration) (*campaign_model.MediaAsset, error)
	ClaimExpired(context.Context, int, time.Duration) ([]campaign_model.MediaAsset, error)
	CompleteCleanup(context.Context, *campaign_model.MediaAsset) error
	ReleaseCleanup(context.Context, *campaign_model.MediaAsset) error
}

type mediaAssetRepository struct {
	db  *gorm.DB
	now func() time.Time
}

func NewMediaAssetRepository(db *gorm.DB) MediaAssetRepository {
	return &mediaAssetRepository{db: db, now: time.Now}
}

func (r *mediaAssetRepository) CreateUploading(ctx context.Context, input CreateMediaAssetInput) (*campaign_model.MediaAsset, bool, error) {
	if r == nil || r.db == nil || r.now == nil || ctx == nil || uuid.Validate(input.ID) != nil || uuid.Validate(input.InstanceID) != nil ||
		input.ObjectKey == "" || input.ExpiresAt.IsZero() {
		return nil, false, errors.New("campaign media repository and bounded identity are required")
	}
	now := r.now().UTC()
	if !input.ExpiresAt.After(now) {
		return nil, false, errors.New("campaign media expiry must be in the future")
	}
	asset := &campaign_model.MediaAsset{
		ID: input.ID, InstanceID: input.InstanceID, ObjectKey: input.ObjectKey, MediaType: "image",
		Status: campaign_model.MediaAssetStatusUploading, RequestReferenceHash: input.RequestReferenceHash,
		ExpiresAt: input.ExpiresAt.UTC(), CreatedAt: now, UpdatedAt: now,
	}
	create := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(asset)
	if create.Error != nil {
		return nil, false, create.Error
	}
	if create.RowsAffected == 0 {
		if input.RequestReferenceHash != nil {
			var existing campaign_model.MediaAsset
			lookup := r.db.WithContext(ctx).Where("instance_id = ? AND request_reference_hash = ? AND deleted_at IS NULL", input.InstanceID, *input.RequestReferenceHash).First(&existing).Error
			if lookup == nil {
				return &existing, false, nil
			}
			if !errors.Is(lookup, gorm.ErrRecordNotFound) {
				return nil, false, lookup
			}
		}
		return nil, false, ErrMediaAssetConflict
	}
	return asset, true, nil
}

func (r *mediaAssetRepository) Get(ctx context.Context, instanceID, assetID string) (*campaign_model.MediaAsset, error) {
	if err := validateMediaAssetRead(r, ctx, instanceID, assetID); err != nil {
		return nil, err
	}
	var asset campaign_model.MediaAsset
	err := r.db.WithContext(ctx).Where("instance_id = ? AND id = ? AND deleted_at IS NULL", instanceID, assetID).First(&asset).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrMediaAssetNotFound
	}
	return &asset, err
}

func (r *mediaAssetRepository) MarkReady(ctx context.Context, instanceID, assetID string, input ReadyMediaAssetInput) (*campaign_model.MediaAsset, error) {
	if err := validateMediaAssetRead(r, ctx, instanceID, assetID); err != nil || input.MIMEType == "" || input.SizeBytes < 1 || input.Width < 1 || input.Height < 1 || len(input.SHA256) != 64 {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("complete campaign media metadata is required")
	}
	now := r.now().UTC()
	result := r.db.WithContext(ctx).Model(&campaign_model.MediaAsset{}).
		Where("instance_id = ? AND id = ? AND status = ? AND deleted_at IS NULL", instanceID, assetID, campaign_model.MediaAssetStatusUploading).
		Updates(map[string]any{
			"mime_type": input.MIMEType, "size_bytes": input.SizeBytes, "width": input.Width, "height": input.Height,
			"sha256": input.SHA256, "status": campaign_model.MediaAssetStatusReady, "ready_at": now, "updated_at": now,
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		if _, err := r.Get(ctx, instanceID, assetID); errors.Is(err, ErrMediaAssetNotFound) {
			return nil, err
		}
		return nil, ErrMediaAssetConflict
	}
	return r.Get(ctx, instanceID, assetID)
}

func (r *mediaAssetRepository) MarkFailed(ctx context.Context, instanceID, assetID string) error {
	if err := validateMediaAssetRead(r, ctx, instanceID, assetID); err != nil {
		return err
	}
	now := r.now().UTC()
	result := r.db.WithContext(ctx).Model(&campaign_model.MediaAsset{}).
		Where("instance_id = ? AND id = ? AND status = ? AND deleted_at IS NULL", instanceID, assetID, campaign_model.MediaAssetStatusUploading).
		Updates(map[string]any{"status": campaign_model.MediaAssetStatusFailed, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrMediaAssetConflict
	}
	return nil
}

func (r *mediaAssetRepository) ClaimDelete(ctx context.Context, instanceID, assetID string, lease time.Duration) (*campaign_model.MediaAsset, error) {
	if err := validateMediaAssetRead(r, ctx, instanceID, assetID); err != nil || lease <= 0 {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("positive campaign media cleanup lease is required")
	}
	now := r.now().UTC()
	token := uuid.NewString()
	var asset campaign_model.MediaAsset
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id = ? AND id = ? AND deleted_at IS NULL", instanceID, assetID).First(&asset).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrMediaAssetNotFound
			}
			return err
		}
		if asset.Status == campaign_model.MediaAssetStatusUploading || asset.CleanupClaimToken != nil && asset.CleanupLeaseUntil != nil && asset.CleanupLeaseUntil.After(now) {
			return ErrMediaAssetConflict
		}
		var references int64
		if err := tx.Model(&campaign_model.Campaign{}).
			Where("instance_id = ? AND media_asset_id = ?", instanceID, assetID).Count(&references).Error; err != nil {
			return err
		}
		if references != 0 {
			return ErrMediaAssetConflict
		}
		leaseUntil := now.Add(lease)
		if err := tx.Model(&campaign_model.MediaAsset{}).Where("instance_id = ? AND id = ?", instanceID, assetID).
			Updates(map[string]any{"cleanup_claim_token": token, "cleanup_lease_until": leaseUntil, "updated_at": now}).Error; err != nil {
			return err
		}
		asset.CleanupClaimToken = &token
		asset.CleanupLeaseUntil = &leaseUntil
		return nil
	})
	return &asset, err
}

func (r *mediaAssetRepository) ClaimExpired(ctx context.Context, limit int, lease time.Duration) ([]campaign_model.MediaAsset, error) {
	if r == nil || r.db == nil || r.now == nil || ctx == nil || limit < 1 || limit > 100 || lease <= 0 {
		return nil, errors.New("bounded campaign media cleanup claim is required")
	}
	now := r.now().UTC()
	token := uuid.NewString()
	leaseUntil := now.Add(lease)
	var assets []campaign_model.MediaAsset
	err := r.db.WithContext(ctx).Raw(`WITH candidates AS (
    SELECT id
    FROM campaign_media_assets
    WHERE deleted_at IS NULL
      AND status IN ('uploading', 'ready', 'failed')
      AND expires_at <= ?
      AND (cleanup_lease_until IS NULL OR cleanup_lease_until <= ?)
      AND NOT EXISTS (
          SELECT 1 FROM campaigns
          WHERE campaigns.instance_id = campaign_media_assets.instance_id
            AND campaigns.media_asset_id = campaign_media_assets.id
      )
    ORDER BY expires_at ASC, id ASC
    FOR UPDATE SKIP LOCKED
    LIMIT ?
)
UPDATE campaign_media_assets AS assets
SET cleanup_claim_token = ?, cleanup_lease_until = ?, updated_at = ?
FROM candidates
WHERE assets.id = candidates.id
RETURNING assets.*`, now, now, limit, token, leaseUntil, now).Scan(&assets).Error
	return assets, err
}

func (r *mediaAssetRepository) CompleteCleanup(ctx context.Context, asset *campaign_model.MediaAsset) error {
	if err := validateMediaCleanupMutation(r, ctx, asset); err != nil {
		return err
	}
	now := r.now().UTC()
	result := r.db.WithContext(ctx).Model(&campaign_model.MediaAsset{}).
		Where("instance_id = ? AND id = ? AND cleanup_claim_token = ? AND deleted_at IS NULL", asset.InstanceID, asset.ID, *asset.CleanupClaimToken).
		Updates(map[string]any{
			"status": campaign_model.MediaAssetStatusDeleted, "deleted_at": now, "cleanup_claim_token": nil,
			"cleanup_lease_until": nil, "updated_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrMediaAssetConflict
	}
	return nil
}

func (r *mediaAssetRepository) ReleaseCleanup(ctx context.Context, asset *campaign_model.MediaAsset) error {
	if err := validateMediaCleanupMutation(r, ctx, asset); err != nil {
		return err
	}
	result := r.db.WithContext(ctx).Model(&campaign_model.MediaAsset{}).
		Where("instance_id = ? AND id = ? AND cleanup_claim_token = ? AND deleted_at IS NULL", asset.InstanceID, asset.ID, *asset.CleanupClaimToken).
		Updates(map[string]any{"cleanup_claim_token": nil, "cleanup_lease_until": nil, "updated_at": r.now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrMediaAssetConflict
	}
	return nil
}

func validateMediaAssetRead(r *mediaAssetRepository, ctx context.Context, instanceID, assetID string) error {
	if r == nil || r.db == nil || r.now == nil || ctx == nil || uuid.Validate(instanceID) != nil || uuid.Validate(assetID) != nil {
		return errors.New("campaign media repository and identities are required")
	}
	return nil
}

func validateMediaCleanupMutation(r *mediaAssetRepository, ctx context.Context, asset *campaign_model.MediaAsset) error {
	if r == nil || r.db == nil || r.now == nil || ctx == nil || asset == nil || uuid.Validate(asset.ID) != nil || uuid.Validate(asset.InstanceID) != nil ||
		asset.CleanupClaimToken == nil || uuid.Validate(*asset.CleanupClaimToken) != nil {
		return errors.New("claimed campaign media cleanup identity is required")
	}
	return nil
}
