package media_repository

import (
	"context"
	"errors"
	"strings"
	"time"

	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrAssetNotFound = errors.New("media asset not found")
	ErrAssetConflict = errors.New("media asset state conflict")
)

type CreateAssetInput struct {
	ID                   string
	InstanceID           string
	Origin               media_model.AssetOrigin
	Status               media_model.AssetStatus
	RequestReferenceHash *string
	ExpiresAt            *time.Time
}

type Repository interface {
	Create(context.Context, CreateAssetInput) (*media_model.Asset, bool, error)
	Get(context.Context, string, string) (*media_model.Asset, error)
	ListVariants(context.Context, string, string) ([]media_model.AssetVariant, error)
	AddVariant(context.Context, media_model.AssetVariant) error
	MarkReady(context.Context, string, string) (*media_model.Asset, error)
	MarkFailed(context.Context, string, string, string) error
	AddReference(context.Context, media_model.AssetReference) error
	RemoveReference(context.Context, string, string, media_model.ReferenceOwnerType, string) error
	ClaimExpired(context.Context, int, time.Duration) ([]media_model.Asset, error)
	CompleteCleanup(context.Context, *media_model.Asset) error
	ReleaseCleanup(context.Context, *media_model.Asset) error
}

type repository struct {
	db  *gorm.DB
	now func() time.Time
}

func New(db *gorm.DB) Repository { return &repository{db: db, now: time.Now} }

func (r *repository) Create(ctx context.Context, input CreateAssetInput) (*media_model.Asset, bool, error) {
	if err := validateCreate(r, ctx, input); err != nil {
		return nil, false, err
	}
	now := r.now().UTC()
	asset := media_model.Asset{
		ID: input.ID, InstanceID: input.InstanceID, MediaType: "image", Origin: input.Origin, Status: input.Status,
		RequestReferenceHash: input.RequestReferenceHash, ExpiresAt: utcPointer(input.ExpiresAt), CreatedAt: now, UpdatedAt: now,
	}
	if err := r.db.WithContext(ctx).Create(&asset).Error; err != nil {
		if input.RequestReferenceHash != nil && isUniqueViolation(err) {
			var existing media_model.Asset
			lookup := r.db.WithContext(ctx).
				Where("instance_id = ? AND request_reference_hash = ? AND deleted_at IS NULL", input.InstanceID, *input.RequestReferenceHash).
				First(&existing).Error
			if lookup == nil {
				return &existing, false, nil
			}
		}
		return nil, false, err
	}
	return &asset, true, nil
}

func (r *repository) Get(ctx context.Context, instanceID, assetID string) (*media_model.Asset, error) {
	if err := validateIdentity(r, ctx, instanceID, assetID); err != nil {
		return nil, err
	}
	var asset media_model.Asset
	err := r.db.WithContext(ctx).Where("instance_id = ? AND id = ? AND deleted_at IS NULL", instanceID, assetID).First(&asset).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAssetNotFound
	}
	if err != nil {
		return nil, err
	}
	var canonical media_model.AssetVariant
	err = r.db.WithContext(ctx).
		Where("instance_id = ? AND media_asset_id = ? AND variant = ?", instanceID, assetID, media_model.VariantCanonical).
		First(&canonical).Error
	if err == nil {
		asset.Canonical = &canonical
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	return &asset, nil
}

func (r *repository) ListVariants(ctx context.Context, instanceID, assetID string) ([]media_model.AssetVariant, error) {
	if err := validateIdentity(r, ctx, instanceID, assetID); err != nil {
		return nil, err
	}
	var variants []media_model.AssetVariant
	if err := r.db.WithContext(ctx).Where("instance_id = ? AND media_asset_id = ?", instanceID, assetID).
		Order("variant ASC").Find(&variants).Error; err != nil {
		return nil, err
	}
	return variants, nil
}

func (r *repository) AddVariant(ctx context.Context, variant media_model.AssetVariant) error {
	if err := validateVariant(r, ctx, &variant); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var asset media_model.Asset
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("instance_id = ? AND id = ? AND deleted_at IS NULL", variant.InstanceID, variant.MediaAssetID).First(&asset).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAssetNotFound
			}
			return err
		}
		if asset.Status == media_model.AssetStatusReady || asset.Status == media_model.AssetStatusDeleting || asset.Status == media_model.AssetStatusDeleted || asset.CleanupClaimToken != nil {
			return ErrAssetConflict
		}
		variant.CreatedAt = r.now().UTC()
		if err := tx.Create(&variant).Error; err != nil {
			if isUniqueViolation(err) {
				return ErrAssetConflict
			}
			return err
		}
		return nil
	})
}

func (r *repository) MarkReady(ctx context.Context, instanceID, assetID string) (*media_model.Asset, error) {
	if err := validateIdentity(r, ctx, instanceID, assetID); err != nil {
		return nil, err
	}
	now := r.now().UTC()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var asset media_model.Asset
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("instance_id = ? AND id = ? AND deleted_at IS NULL", instanceID, assetID).First(&asset).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAssetNotFound
			}
			return err
		}
		if asset.Status == media_model.AssetStatusReady {
			return nil
		}
		if asset.Status == media_model.AssetStatusDeleting || asset.Status == media_model.AssetStatusDeleted || asset.CleanupClaimToken != nil {
			return ErrAssetConflict
		}
		var canonicalCount int64
		if err := tx.Model(&media_model.AssetVariant{}).
			Where("instance_id = ? AND media_asset_id = ? AND variant = ?", instanceID, assetID, media_model.VariantCanonical).
			Count(&canonicalCount).Error; err != nil {
			return err
		}
		if canonicalCount != 1 {
			return ErrAssetConflict
		}
		result := tx.Model(&media_model.Asset{}).Where("instance_id = ? AND id = ? AND deleted_at IS NULL", instanceID, assetID).
			Updates(map[string]any{"status": media_model.AssetStatusReady, "ready_at": now, "failure_code": nil, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrAssetConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, instanceID, assetID)
}

func (r *repository) MarkFailed(ctx context.Context, instanceID, assetID, failureCode string) error {
	if err := validateIdentity(r, ctx, instanceID, assetID); err != nil {
		return err
	}
	failureCode = strings.TrimSpace(failureCode)
	if !validCode(failureCode) {
		return errors.New("bounded media asset failure code is required")
	}
	result := r.db.WithContext(ctx).Model(&media_model.Asset{}).
		Where("instance_id = ? AND id = ? AND status NOT IN ? AND deleted_at IS NULL", instanceID, assetID,
			[]media_model.AssetStatus{media_model.AssetStatusReady, media_model.AssetStatusDeleting, media_model.AssetStatusDeleted}).
		Updates(map[string]any{"status": media_model.AssetStatusFailed, "failure_code": failureCode, "updated_at": r.now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAssetConflict
	}
	return nil
}

func (r *repository) AddReference(ctx context.Context, reference media_model.AssetReference) error {
	if err := validateReference(r, ctx, &reference); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var asset media_model.Asset
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("instance_id = ? AND id = ? AND deleted_at IS NULL", reference.InstanceID, reference.MediaAssetID).First(&asset).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAssetNotFound
			}
			return err
		}
		if asset.Status != media_model.AssetStatusReady || asset.CleanupClaimToken != nil {
			return ErrAssetConflict
		}
		reference.CreatedAt = r.now().UTC()
		if reference.RetentionUntil != nil {
			value := reference.RetentionUntil.UTC()
			reference.RetentionUntil = &value
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&reference).Error; err != nil {
			return err
		}
		return tx.Model(&media_model.Asset{}).Where("instance_id = ? AND id = ?", reference.InstanceID, reference.MediaAssetID).
			Update("updated_at", r.now().UTC()).Error
	})
}

func (r *repository) RemoveReference(ctx context.Context, instanceID, assetID string, ownerType media_model.ReferenceOwnerType, ownerID string) error {
	if err := validateIdentity(r, ctx, instanceID, assetID); err != nil {
		return err
	}
	if !validOwner(ownerType) || strings.TrimSpace(ownerID) == "" || len(ownerID) > 255 {
		return errors.New("bounded media asset owner is required")
	}
	return r.db.WithContext(ctx).Where("instance_id = ? AND media_asset_id = ? AND owner_type = ? AND owner_id = ?", instanceID, assetID, ownerType, ownerID).
		Delete(&media_model.AssetReference{}).Error
}

func (r *repository) ClaimExpired(ctx context.Context, limit int, lease time.Duration) ([]media_model.Asset, error) {
	if r == nil || r.db == nil || r.now == nil || ctx == nil || limit < 1 || limit > 100 || lease <= 0 {
		return nil, errors.New("bounded media asset cleanup claim is required")
	}
	now := r.now().UTC()
	token := uuid.NewString()
	leaseUntil := now.Add(lease)
	var assets []media_model.Asset
	err := r.db.WithContext(ctx).Raw(`WITH candidates AS (
    SELECT assets.id
    FROM media_assets AS assets
    WHERE assets.deleted_at IS NULL
      AND assets.expires_at IS NOT NULL
      AND assets.expires_at <= ?
      AND assets.status IN ('pending', 'uploading', 'downloading', 'processing', 'ready', 'failed', 'deleting')
      AND (assets.cleanup_lease_until IS NULL OR assets.cleanup_lease_until <= ?)
      AND NOT EXISTS (
          SELECT 1 FROM media_asset_references AS refs
          WHERE refs.instance_id = assets.instance_id AND refs.media_asset_id = assets.id
      )
    ORDER BY assets.expires_at ASC, assets.id ASC
    FOR UPDATE SKIP LOCKED
    LIMIT ?
)
UPDATE media_assets AS assets
SET status = 'deleting', cleanup_claim_token = ?, cleanup_lease_until = ?, updated_at = ?
FROM candidates
WHERE assets.id = candidates.id
RETURNING assets.*`, now, now, limit, token, leaseUntil, now).Scan(&assets).Error
	return assets, err
}

func (r *repository) CompleteCleanup(ctx context.Context, asset *media_model.Asset) error {
	if err := validateCleanup(r, ctx, asset); err != nil {
		return err
	}
	now := r.now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var references int64
		if err := tx.Model(&media_model.AssetReference{}).
			Where("instance_id = ? AND media_asset_id = ?", asset.InstanceID, asset.ID).Count(&references).Error; err != nil {
			return err
		}
		if references != 0 {
			return ErrAssetConflict
		}
		if err := tx.Where("instance_id = ? AND media_asset_id = ?", asset.InstanceID, asset.ID).Delete(&media_model.AssetVariant{}).Error; err != nil {
			return err
		}
		result := tx.Model(&media_model.Asset{}).
			Where("instance_id = ? AND id = ? AND status = ? AND cleanup_claim_token = ? AND deleted_at IS NULL", asset.InstanceID, asset.ID, media_model.AssetStatusDeleting, *asset.CleanupClaimToken).
			Updates(map[string]any{"status": media_model.AssetStatusDeleted, "deleted_at": now, "cleanup_claim_token": nil, "cleanup_lease_until": nil, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrAssetConflict
		}
		return nil
	})
}

func (r *repository) ReleaseCleanup(ctx context.Context, asset *media_model.Asset) error {
	if err := validateCleanup(r, ctx, asset); err != nil {
		return err
	}
	result := r.db.WithContext(ctx).Model(&media_model.Asset{}).
		Where("instance_id = ? AND id = ? AND status = ? AND cleanup_claim_token = ? AND deleted_at IS NULL", asset.InstanceID, asset.ID, media_model.AssetStatusDeleting, *asset.CleanupClaimToken).
		Updates(map[string]any{"status": media_model.AssetStatusFailed, "failure_code": "cleanup_failed", "cleanup_claim_token": nil, "cleanup_lease_until": nil, "updated_at": r.now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrAssetConflict
	}
	return nil
}

func validateCreate(r *repository, ctx context.Context, input CreateAssetInput) error {
	if r == nil || r.db == nil || r.now == nil || ctx == nil || uuid.Validate(input.ID) != nil || uuid.Validate(input.InstanceID) != nil {
		return errors.New("media asset repository and bounded identity are required")
	}
	if input.Origin != media_model.AssetOriginDeviceUpload && input.Origin != media_model.AssetOriginWhatsAppInbound {
		return errors.New("supported media asset origin is required")
	}
	if input.Status != media_model.AssetStatusPending && input.Status != media_model.AssetStatusUploading {
		return errors.New("initial media asset status is invalid")
	}
	if input.RequestReferenceHash != nil && !validSHA(*input.RequestReferenceHash) {
		return errors.New("media asset request hash is invalid")
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(r.now()) {
		return errors.New("media asset expiry must be in the future")
	}
	return nil
}

func validateIdentity(r *repository, ctx context.Context, instanceID, assetID string) error {
	if r == nil || r.db == nil || r.now == nil || ctx == nil || uuid.Validate(instanceID) != nil || uuid.Validate(assetID) != nil {
		return errors.New("media asset repository and bounded identity are required")
	}
	return nil
}

func validateVariant(r *repository, ctx context.Context, variant *media_model.AssetVariant) error {
	expectedObjectKey := ""
	if variant != nil {
		expectedObjectKey = "media-assets/" + variant.InstanceID + "/" + variant.MediaAssetID + "/" + string(variant.Kind)
	}
	if variant == nil || validateIdentity(r, ctx, variant.InstanceID, variant.MediaAssetID) != nil ||
		(variant.Kind != media_model.VariantCanonical && variant.Kind != media_model.VariantProviderOriginal) ||
		variant.ObjectKey != expectedObjectKey ||
		(variant.MIMEType != "image/jpeg" && variant.MIMEType != "image/png") || variant.SizeBytes < 1 || variant.SizeBytes > 64*1024*1024 ||
		variant.Width < 1 || variant.Width > 32768 || variant.Height < 1 || variant.Height > 32768 || !validSHA(variant.SHA256) {
		return errors.New("complete tenant-scoped media variant is required")
	}
	return nil
}

func validateReference(r *repository, ctx context.Context, reference *media_model.AssetReference) error {
	if reference == nil || validateIdentity(r, ctx, reference.InstanceID, reference.MediaAssetID) != nil || !validOwner(reference.OwnerType) ||
		strings.TrimSpace(reference.OwnerID) == "" || len(reference.OwnerID) > 255 {
		return errors.New("complete tenant-scoped media reference is required")
	}
	return nil
}

func validateCleanup(r *repository, ctx context.Context, asset *media_model.Asset) error {
	if asset == nil || validateIdentity(r, ctx, asset.InstanceID, asset.ID) != nil || asset.CleanupClaimToken == nil || uuid.Validate(*asset.CleanupClaimToken) != nil {
		return errors.New("fenced media asset cleanup claim is required")
	}
	return nil
}

func validOwner(value media_model.ReferenceOwnerType) bool {
	return value == media_model.ReferenceOwnerCampaign || value == media_model.ReferenceOwnerMessage
}

func validSHA(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func validCode(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func utcPointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := value.UTC()
	return &result
}

func isUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}
