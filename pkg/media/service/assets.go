package media_service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"strings"
	"time"

	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	media_repository "github.com/evolution-foundation/evolution-go/pkg/media/repository"
	storage_interfaces "github.com/evolution-foundation/evolution-go/pkg/storage/interfaces"
	"github.com/google/uuid"
)

var (
	ErrInvalidMediaAsset      = errors.New("invalid media asset")
	ErrMediaAssetTooLarge     = errors.New("media asset is too large")
	ErrUnsupportedMediaAsset  = errors.New("unsupported media asset type")
	ErrInvalidMediaDimensions = errors.New("invalid media asset dimensions")
	ErrMediaAssetStorage      = errors.New("media asset storage is unavailable")
)

type AssetSettings struct {
	MaxBytes    int64
	MaxPixels   int64
	UnboundTTL  time.Duration
	DeleteLease time.Duration
}

func (s AssetSettings) Validate() error {
	if s.MaxBytes < 1 || s.MaxBytes > 64*1024*1024 || s.MaxPixels < 1 || s.MaxPixels > 100_000_000 || s.UnboundTTL <= 0 || s.DeleteLease <= 0 {
		return errors.New("media asset settings are invalid")
	}
	return nil
}

type AssetUploadInput struct {
	InstanceID     string
	IdempotencyKey string
	Reader         io.Reader
}

type AssetService struct {
	repository media_repository.Repository
	store      storage_interfaces.MediaAssetStore
	settings   AssetSettings
	now        func() time.Time
}

func NewAssetService(repository media_repository.Repository, store storage_interfaces.MediaAssetStore, settings AssetSettings) *AssetService {
	return &AssetService{repository: repository, store: store, settings: settings, now: time.Now}
}

func (s *AssetService) Upload(ctx context.Context, input AssetUploadInput) (*media_model.Asset, error) {
	if err := s.validate(ctx); err != nil || uuid.Validate(input.InstanceID) != nil || input.Reader == nil || len(input.IdempotencyKey) > 255 {
		return nil, ErrInvalidMediaAsset
	}
	assetID := uuid.NewString()
	var requestHash *string
	if key := strings.TrimSpace(input.IdempotencyKey); key != "" {
		hash := scopedAssetHash(input.InstanceID, key)
		requestHash = &hash
	}
	expires := s.now().UTC().Add(s.settings.UnboundTTL)
	asset, created, err := s.repository.Create(ctx, media_repository.CreateAssetInput{
		ID: assetID, InstanceID: input.InstanceID, Origin: media_model.AssetOriginDeviceUpload,
		Status: media_model.AssetStatusUploading, RequestReferenceHash: requestHash, ExpiresAt: &expires,
	})
	if err != nil || !created {
		if err != nil {
			return nil, err
		}
		return s.repository.Get(ctx, asset.InstanceID, asset.ID)
	}
	normalized, err := normalizeAssetImage(input.Reader, s.settings.MaxBytes, s.settings.MaxPixels)
	if err != nil {
		s.fail(ctx, asset)
		return nil, err
	}
	defer normalized.Close()
	key := canonicalAssetKey(input.InstanceID, asset.ID)
	if err := s.store.Health(ctx); err != nil {
		s.fail(ctx, asset)
		return nil, fmt.Errorf("%w: %v", ErrMediaAssetStorage, err)
	}
	if err := s.store.Put(ctx, key, normalized.file, normalized.size, normalized.mimeType); err != nil {
		s.fail(ctx, asset)
		return nil, fmt.Errorf("%w: %v", ErrMediaAssetStorage, err)
	}
	variant := media_model.AssetVariant{
		MediaAssetID: asset.ID, InstanceID: asset.InstanceID, Kind: media_model.VariantCanonical,
		ObjectKey: key, MIMEType: normalized.mimeType, SizeBytes: normalized.size,
		Width: normalized.width, Height: normalized.height, SHA256: normalized.sha256,
	}
	if err := s.repository.AddVariant(ctx, variant); err != nil {
		s.compensateObject(ctx, asset, key)
		return nil, err
	}
	ready, err := s.repository.MarkReady(ctx, asset.InstanceID, asset.ID)
	if err != nil {
		s.compensateObject(ctx, asset, key)
		return nil, err
	}
	return ready, nil
}

func (s *AssetService) Get(ctx context.Context, instanceID, assetID string) (*media_model.Asset, error) {
	if err := s.validate(ctx); err != nil || uuid.Validate(instanceID) != nil || uuid.Validate(assetID) != nil {
		return nil, ErrInvalidMediaAsset
	}
	return s.repository.Get(ctx, instanceID, assetID)
}

func (s *AssetService) Delete(ctx context.Context, instanceID, assetID string) error {
	if err := s.validate(ctx); err != nil || uuid.Validate(instanceID) != nil || uuid.Validate(assetID) != nil {
		return ErrInvalidMediaAsset
	}
	shadowed, err := s.repository.HasCampaignShadow(ctx, instanceID, assetID)
	if err != nil {
		return err
	}
	if shadowed {
		return media_repository.ErrAssetConflict
	}
	asset, err := s.repository.ClaimDelete(ctx, instanceID, assetID, s.settings.DeleteLease)
	if err != nil {
		return err
	}
	variants, err := s.repository.ListVariants(ctx, instanceID, assetID)
	if err != nil {
		s.release(ctx, asset)
		return err
	}
	for _, variant := range variants {
		if err := s.store.Delete(ctx, variant.ObjectKey); err != nil {
			s.release(ctx, asset)
			return fmt.Errorf("%w: %v", ErrMediaAssetStorage, err)
		}
	}
	return s.repository.CompleteCleanup(ctx, asset)
}

func (s *AssetService) validate(ctx context.Context) error {
	if s == nil || s.repository == nil || s.store == nil || s.now == nil || ctx == nil {
		return errors.New("media asset service is unavailable")
	}
	return s.settings.Validate()
}

func (s *AssetService) fail(ctx context.Context, asset *media_model.Asset) {
	compensation, cancel := compensationContext(ctx)
	defer cancel()
	_ = s.repository.MarkFailed(compensation, asset.InstanceID, asset.ID, "device_upload_failed")
}

func (s *AssetService) compensateObject(ctx context.Context, asset *media_model.Asset, key string) {
	compensation, cancel := compensationContext(ctx)
	defer cancel()
	_ = s.store.Delete(compensation, key)
	_ = s.repository.MarkFailed(compensation, asset.InstanceID, asset.ID, "device_upload_failed")
}

func (s *AssetService) release(ctx context.Context, asset *media_model.Asset) {
	compensation, cancel := compensationContext(ctx)
	defer cancel()
	_ = s.repository.ReleaseCleanup(compensation, asset)
}

type normalizedAssetImage struct {
	file                   *os.File
	path, mimeType, sha256 string
	size                   int64
	width, height          int
}

func (m *normalizedAssetImage) Close() error {
	if m == nil {
		return nil
	}
	var err error
	if m.file != nil {
		err = m.file.Close()
	}
	if m.path != "" {
		err = errors.Join(err, os.Remove(m.path))
	}
	return err
}

func normalizeAssetImage(reader io.Reader, maxBytes, maxPixels int64) (*normalizedAssetImage, error) {
	input, err := os.CreateTemp("", "omniwa-media-input-*")
	if err != nil {
		return nil, err
	}
	inputPath := input.Name()
	defer func() { _ = input.Close(); _ = os.Remove(inputPath) }()
	written, err := io.Copy(input, io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if written == 0 {
		return nil, ErrInvalidMediaAsset
	}
	if written > maxBytes {
		return nil, ErrMediaAssetTooLarge
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	configuration, format, err := image.DecodeConfig(input)
	if err != nil || format != "jpeg" && format != "png" {
		return nil, ErrUnsupportedMediaAsset
	}
	if configuration.Width < 1 || configuration.Height < 1 || configuration.Width > 32768 || configuration.Height > 32768 || int64(configuration.Width) > maxPixels/int64(configuration.Height) {
		return nil, ErrInvalidMediaDimensions
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	decoded, format, err := image.Decode(input)
	if err != nil {
		return nil, ErrUnsupportedMediaAsset
	}
	output, err := os.CreateTemp("", "omniwa-media-normalized-*")
	if err != nil {
		return nil, err
	}
	result := &normalizedAssetImage{file: output, path: output.Name(), width: configuration.Width, height: configuration.Height}
	failed := true
	defer func() {
		if failed {
			_ = result.Close()
		}
	}()
	switch format {
	case "jpeg":
		result.mimeType, err = "image/jpeg", jpeg.Encode(output, decoded, &jpeg.Options{Quality: 90})
	case "png":
		result.mimeType, err = "image/png", png.Encode(output, decoded)
	default:
		return nil, ErrUnsupportedMediaAsset
	}
	if err != nil {
		return nil, err
	}
	info, err := output.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() < 1 || info.Size() > maxBytes {
		return nil, ErrMediaAssetTooLarge
	}
	result.size = info.Size()
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, output); err != nil {
		return nil, err
	}
	result.sha256 = hex.EncodeToString(hash.Sum(nil))
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	failed = false
	return result, nil
}

func scopedAssetHash(instanceID, key string) string {
	sum := sha256.Sum256([]byte(instanceID + "\x00" + key))
	return hex.EncodeToString(sum[:])
}

func canonicalAssetKey(instanceID, assetID string) string {
	return "media-assets/" + instanceID + "/" + assetID + "/canonical"
}

func compensationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
}
