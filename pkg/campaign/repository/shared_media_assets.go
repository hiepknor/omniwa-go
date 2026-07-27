package campaign_repository

import (
	"context"
	"errors"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	media_repository "github.com/evolution-foundation/evolution-go/pkg/media/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// sharedMediaAssetRepository keeps campaign_media_assets as a rollback shadow
// while media_assets is authoritative for new campaign image work.
type sharedMediaAssetRepository struct {
	db *gorm.DB
}

func NewSharedMediaAssetRepository(db *gorm.DB) MediaAssetRepository {
	return &sharedMediaAssetRepository{db: db}
}

func (r *sharedMediaAssetRepository) CreateUploading(ctx context.Context, input CreateMediaAssetInput) (*campaign_model.MediaAsset, bool, error) {
	if r == nil || r.db == nil {
		return nil, false, errors.New("shared campaign media repository is required")
	}
	var result *campaign_model.MediaAsset
	created := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sharedRepository := media_repository.New(tx)
		shared, sharedCreated, err := sharedRepository.Create(ctx, media_repository.CreateAssetInput{
			ID: input.ID, InstanceID: input.InstanceID, Origin: media_model.AssetOriginDeviceUpload,
			Status: media_model.AssetStatusUploading, RequestReferenceHash: input.RequestReferenceHash, ExpiresAt: &input.ExpiresAt,
		})
		if err != nil {
			return translateSharedMediaError(err)
		}
		if !sharedCreated {
			shared, err = sharedRepository.Get(ctx, shared.InstanceID, shared.ID)
			if err != nil {
				return translateSharedMediaError(err)
			}
		}
		objectKey := sharedCanonicalObjectKey(shared.InstanceID, shared.ID)
		legacy, legacyCreated, err := NewMediaAssetRepository(tx).CreateUploading(ctx, CreateMediaAssetInput{
			ID: shared.ID, InstanceID: shared.InstanceID, ObjectKey: objectKey,
			RequestReferenceHash: input.RequestReferenceHash, ExpiresAt: input.ExpiresAt,
		})
		if err != nil {
			return err
		}
		if legacy.ID != shared.ID || legacy.InstanceID != shared.InstanceID || legacy.ObjectKey != objectKey {
			return ErrMediaAssetConflict
		}
		created = sharedCreated && legacyCreated
		result = sharedCampaignMediaView(shared, legacy.ObjectKey)
		return nil
	})
	return result, created, err
}

func (r *sharedMediaAssetRepository) Get(ctx context.Context, instanceID, assetID string) (*campaign_model.MediaAsset, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("shared campaign media repository is required")
	}
	shared, err := media_repository.New(r.db).Get(ctx, instanceID, assetID)
	if err != nil {
		return nil, translateSharedMediaError(err)
	}
	fallback, err := sharedCampaignFallbackObjectKey(ctx, r.db, instanceID, assetID)
	if err != nil {
		return nil, err
	}
	return sharedCampaignMediaView(shared, fallback), nil
}

func (r *sharedMediaAssetRepository) MarkReady(ctx context.Context, instanceID, assetID string, input ReadyMediaAssetInput) (*campaign_model.MediaAsset, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("shared campaign media repository is required")
	}
	var result *campaign_model.MediaAsset
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sharedRepository := media_repository.New(tx)
		shared, err := sharedRepository.Get(ctx, instanceID, assetID)
		if err != nil {
			return translateSharedMediaError(err)
		}
		if shared.Status == media_model.AssetStatusReady {
			legacy, legacyErr := NewMediaAssetRepository(tx).Get(ctx, instanceID, assetID)
			if legacyErr != nil || legacy.Status != campaign_model.MediaAssetStatusReady || shared.Canonical == nil || legacy.ObjectKey != shared.Canonical.ObjectKey {
				return ErrMediaAssetConflict
			}
			result = sharedCampaignMediaView(shared, shared.Canonical.ObjectKey)
			return nil
		}
		if shared.Status != media_model.AssetStatusReady {
			variant := media_model.AssetVariant{
				MediaAssetID: assetID, InstanceID: instanceID, Kind: media_model.VariantCanonical,
				ObjectKey: sharedCanonicalObjectKey(instanceID, assetID), MIMEType: input.MIMEType,
				SizeBytes: input.SizeBytes, Width: input.Width, Height: input.Height, SHA256: input.SHA256,
			}
			if err := sharedRepository.AddVariant(ctx, variant); err != nil {
				return translateSharedMediaError(err)
			}
			shared, err = sharedRepository.MarkReady(ctx, instanceID, assetID)
			if err != nil {
				return translateSharedMediaError(err)
			}
		}
		legacy, err := NewMediaAssetRepository(tx).MarkReady(ctx, instanceID, assetID, input)
		if err != nil {
			return err
		}
		if shared.Canonical == nil || legacy.ObjectKey != shared.Canonical.ObjectKey {
			return ErrMediaAssetConflict
		}
		result = sharedCampaignMediaView(shared, shared.Canonical.ObjectKey)
		return nil
	})
	return result, err
}

func (r *sharedMediaAssetRepository) MarkFailed(ctx context.Context, instanceID, assetID string) error {
	if r == nil || r.db == nil {
		return errors.New("shared campaign media repository is required")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := media_repository.New(tx).MarkFailed(ctx, instanceID, assetID, "campaign_media_failed"); err != nil {
			return translateSharedMediaError(err)
		}
		return NewMediaAssetRepository(tx).MarkFailed(ctx, instanceID, assetID)
	})
}

func (r *sharedMediaAssetRepository) ClaimDelete(ctx context.Context, instanceID, assetID string, lease time.Duration) (*campaign_model.MediaAsset, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("shared campaign media repository is required")
	}
	var result *campaign_model.MediaAsset
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		sharedRepository := media_repository.New(tx)
		claimed, err := sharedRepository.ClaimDelete(ctx, instanceID, assetID, lease)
		if err != nil {
			return translateSharedMediaError(err)
		}
		if err := mirrorSharedCleanupClaim(tx, claimed); err != nil {
			return err
		}
		view, err := sharedRepository.Get(ctx, instanceID, assetID)
		if err != nil {
			return translateSharedMediaError(err)
		}
		fallback, err := sharedCampaignFallbackObjectKey(ctx, tx, instanceID, assetID)
		if err != nil {
			return err
		}
		result = sharedCampaignMediaView(view, fallback)
		result.CleanupClaimToken = claimed.CleanupClaimToken
		result.CleanupLeaseUntil = claimed.CleanupLeaseUntil
		return nil
	})
	return result, err
}

func (r *sharedMediaAssetRepository) ClaimExpired(ctx context.Context, limit int, lease time.Duration) ([]campaign_model.MediaAsset, error) {
	if r == nil || r.db == nil || ctx == nil || limit < 1 || limit > 100 || lease <= 0 {
		return nil, errors.New("bounded shared campaign media cleanup claim is required")
	}
	var result []campaign_model.MediaAsset
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		leaseUntil := now.Add(lease)
		claimToken := uuid.NewString()
		var shared []media_model.Asset
		if err := tx.Raw(`WITH candidates AS (
    SELECT shared.id
    FROM media_assets AS shared
    INNER JOIN campaign_media_assets AS legacy
      ON legacy.id = shared.id AND legacy.instance_id = shared.instance_id
    WHERE shared.origin = 'device_upload'
      AND shared.deleted_at IS NULL
      AND shared.expires_at IS NOT NULL
      AND shared.expires_at <= ?
      AND shared.status IN ('pending', 'uploading', 'ready', 'failed', 'deleting')
      AND (shared.cleanup_lease_until IS NULL OR shared.cleanup_lease_until <= ?)
      AND NOT EXISTS (
          SELECT 1 FROM media_asset_references AS refs
          WHERE refs.instance_id = shared.instance_id AND refs.media_asset_id = shared.id
            AND (refs.retention_until IS NULL OR refs.retention_until > ?)
      )
    ORDER BY shared.expires_at ASC, shared.id ASC
    FOR UPDATE OF shared SKIP LOCKED
    LIMIT ?
)
UPDATE media_assets AS shared
SET status = 'deleting', cleanup_claim_token = ?, cleanup_lease_until = ?, updated_at = ?
FROM candidates
WHERE shared.id = candidates.id
RETURNING shared.*`, now, now, now, limit, claimToken, leaseUntil, now).Scan(&shared).Error; err != nil {
			return err
		}
		result = make([]campaign_model.MediaAsset, len(shared))
		for index := range shared {
			if err := mirrorSharedCleanupClaim(tx, &shared[index]); err != nil {
				return err
			}
			view, err := media_repository.New(tx).Get(ctx, shared[index].InstanceID, shared[index].ID)
			if err != nil {
				return translateSharedMediaError(err)
			}
			fallback, err := sharedCampaignFallbackObjectKey(ctx, tx, shared[index].InstanceID, shared[index].ID)
			if err != nil {
				return err
			}
			result[index] = *sharedCampaignMediaView(view, fallback)
			result[index].CleanupClaimToken = shared[index].CleanupClaimToken
			result[index].CleanupLeaseUntil = shared[index].CleanupLeaseUntil
		}
		return nil
	})
	return result, err
}

func (r *sharedMediaAssetRepository) CompleteCleanup(ctx context.Context, asset *campaign_model.MediaAsset) error {
	if r == nil || r.db == nil || asset == nil {
		return errors.New("shared campaign media repository and claim are required")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		shared := sharedCleanupAsset(asset)
		if err := media_repository.New(tx).CompleteCleanup(ctx, shared); err != nil {
			return translateSharedMediaError(err)
		}
		return NewMediaAssetRepository(tx).CompleteCleanup(ctx, asset)
	})
}

func (r *sharedMediaAssetRepository) ReleaseCleanup(ctx context.Context, asset *campaign_model.MediaAsset) error {
	if r == nil || r.db == nil || asset == nil {
		return errors.New("shared campaign media repository and claim are required")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := media_repository.New(tx).ReleaseCleanup(ctx, sharedCleanupAsset(asset)); err != nil {
			return translateSharedMediaError(err)
		}
		result := tx.Model(&campaign_model.MediaAsset{}).
			Where("instance_id = ? AND id = ? AND cleanup_claim_token = ? AND deleted_at IS NULL", asset.InstanceID, asset.ID, *asset.CleanupClaimToken).
			Updates(map[string]any{
				"status": campaign_model.MediaAssetStatusFailed, "cleanup_claim_token": nil,
				"cleanup_lease_until": nil, "updated_at": time.Now().UTC(),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrMediaAssetConflict
		}
		return nil
	})
}

func mirrorSharedCleanupClaim(tx *gorm.DB, asset *media_model.Asset) error {
	if asset == nil || asset.CleanupClaimToken == nil || asset.CleanupLeaseUntil == nil {
		return ErrMediaAssetConflict
	}
	result := tx.Model(&campaign_model.MediaAsset{}).
		Where("instance_id = ? AND id = ? AND deleted_at IS NULL", asset.InstanceID, asset.ID).
		Updates(map[string]any{"cleanup_claim_token": *asset.CleanupClaimToken, "cleanup_lease_until": *asset.CleanupLeaseUntil, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrMediaAssetConflict
	}
	return nil
}

func sharedCampaignFallbackObjectKey(ctx context.Context, db *gorm.DB, instanceID, assetID string) (string, error) {
	var legacy campaign_model.MediaAsset
	err := db.WithContext(ctx).Select("object_key").Where("instance_id = ? AND id = ?", instanceID, assetID).First(&legacy).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", ErrMediaAssetConflict
	}
	if err != nil {
		return "", err
	}
	return legacy.ObjectKey, nil
}

func sharedCampaignMediaView(asset *media_model.Asset, fallbackObjectKey string) *campaign_model.MediaAsset {
	if asset == nil {
		return nil
	}
	status := campaign_model.MediaAssetStatusFailed
	switch asset.Status {
	case media_model.AssetStatusReady:
		status = campaign_model.MediaAssetStatusReady
	case media_model.AssetStatusDeleted:
		status = campaign_model.MediaAssetStatusDeleted
	case media_model.AssetStatusUploading, media_model.AssetStatusPending:
		status = campaign_model.MediaAssetStatusUploading
	}
	view := &campaign_model.MediaAsset{
		ID: asset.ID, InstanceID: asset.InstanceID, ObjectKey: fallbackObjectKey, MediaType: asset.MediaType,
		Status: status, RequestReferenceHash: asset.RequestReferenceHash, CleanupClaimToken: asset.CleanupClaimToken,
		CleanupLeaseUntil: asset.CleanupLeaseUntil, ReadyAt: asset.ReadyAt, ExpiresAt: time.Time{},
		DeletedAt: asset.DeletedAt, CreatedAt: asset.CreatedAt, UpdatedAt: asset.UpdatedAt,
	}
	if asset.ExpiresAt != nil {
		view.ExpiresAt = *asset.ExpiresAt
	}
	if asset.Canonical != nil {
		view.ObjectKey = asset.Canonical.ObjectKey
		view.MIMEType = &asset.Canonical.MIMEType
		view.SizeBytes = &asset.Canonical.SizeBytes
		view.Width = &asset.Canonical.Width
		view.Height = &asset.Canonical.Height
		view.SHA256 = &asset.Canonical.SHA256
	}
	return view
}

func sharedCleanupAsset(asset *campaign_model.MediaAsset) *media_model.Asset {
	return &media_model.Asset{
		ID: asset.ID, InstanceID: asset.InstanceID, Status: media_model.AssetStatusDeleting,
		CleanupClaimToken: asset.CleanupClaimToken, CleanupLeaseUntil: asset.CleanupLeaseUntil,
	}
}

func sharedCanonicalObjectKey(instanceID, assetID string) string {
	return "media-assets/" + instanceID + "/" + assetID + "/canonical"
}

func translateSharedMediaError(err error) error {
	switch {
	case errors.Is(err, media_repository.ErrAssetNotFound):
		return ErrMediaAssetNotFound
	case errors.Is(err, media_repository.ErrAssetConflict):
		return ErrMediaAssetConflict
	default:
		return err
	}
}
