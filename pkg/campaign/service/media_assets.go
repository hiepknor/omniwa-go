package campaign_service

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

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	campaign_repository "github.com/evolution-foundation/evolution-go/pkg/campaign/repository"
	storage_interfaces "github.com/evolution-foundation/evolution-go/pkg/storage/interfaces"
	"github.com/google/uuid"
)

var (
	ErrInvalidMediaUpload      = errors.New("invalid campaign media upload")
	ErrMediaTooLarge           = errors.New("campaign media is too large")
	ErrUnsupportedMediaType    = errors.New("unsupported campaign media type")
	ErrInvalidMediaDimension   = errors.New("invalid campaign media dimensions")
	ErrMediaStorageUnavailable = errors.New("campaign media storage is unavailable")
)

type MediaSettings struct {
	MaxBytes        int64
	MaxPixels       int64
	UnboundTTL      time.Duration
	CleanupBatch    int
	CleanupLease    time.Duration
	CleanupInterval time.Duration
}

func (s MediaSettings) Validate() error {
	if s.MaxBytes < 1 || s.MaxBytes > 64*1024*1024 || s.MaxPixels < 1 || s.MaxPixels > 100_000_000 || s.UnboundTTL <= 0 ||
		s.CleanupBatch < 1 || s.CleanupBatch > 100 || s.CleanupLease <= 0 || s.CleanupInterval <= 0 {
		return errors.New("campaign media settings are invalid")
	}
	return nil
}

type MediaUploadInput struct {
	InstanceID     string
	IdempotencyKey string
	Reader         io.Reader
}

type MediaAssetService struct {
	repository campaign_repository.MediaAssetRepository
	store      storage_interfaces.CampaignMediaStore
	settings   MediaSettings
	now        func() time.Time
}

func NewMediaAssetService(repository campaign_repository.MediaAssetRepository, store storage_interfaces.CampaignMediaStore, settings MediaSettings) *MediaAssetService {
	return &MediaAssetService{repository: repository, store: store, settings: settings, now: time.Now}
}

func (s *MediaAssetService) Upload(ctx context.Context, input MediaUploadInput) (*campaign_model.MediaAsset, error) {
	if err := s.validate(ctx); err != nil || uuid.Validate(input.InstanceID) != nil || input.Reader == nil || len(input.IdempotencyKey) > 255 {
		return nil, ErrInvalidMediaUpload
	}
	assetID := uuid.NewString()
	var requestHash *string
	if key := strings.TrimSpace(input.IdempotencyKey); key != "" {
		hash := scopedMediaHash(input.InstanceID, key)
		requestHash = &hash
	}
	asset, created, err := s.repository.CreateUploading(ctx, campaign_repository.CreateMediaAssetInput{
		ID: assetID, InstanceID: input.InstanceID, ObjectKey: mediaObjectKey(input.InstanceID, assetID),
		RequestReferenceHash: requestHash, ExpiresAt: s.now().UTC().Add(s.settings.UnboundTTL),
	})
	if err != nil {
		return nil, err
	}
	if !created {
		return asset, nil
	}
	normalized, normalizeErr := s.normalize(input.Reader)
	if normalizeErr != nil {
		compensationCtx, cancel := mediaCompensationContext(ctx)
		defer cancel()
		_ = s.repository.MarkFailed(compensationCtx, input.InstanceID, asset.ID)
		return nil, normalizeErr
	}
	defer normalized.Close()
	if err := s.store.Health(ctx); err != nil {
		compensationCtx, cancel := mediaCompensationContext(ctx)
		defer cancel()
		_ = s.repository.MarkFailed(compensationCtx, input.InstanceID, asset.ID)
		return nil, fmt.Errorf("%w: %v", ErrMediaStorageUnavailable, err)
	}
	if err := s.store.Put(ctx, asset.ObjectKey, normalized.file, normalized.size, normalized.mimeType); err != nil {
		compensationCtx, cancel := mediaCompensationContext(ctx)
		defer cancel()
		_ = s.repository.MarkFailed(compensationCtx, input.InstanceID, asset.ID)
		_ = s.store.Delete(compensationCtx, asset.ObjectKey)
		return nil, fmt.Errorf("%w: %v", ErrMediaStorageUnavailable, err)
	}
	ready, err := s.repository.MarkReady(ctx, input.InstanceID, asset.ID, campaign_repository.ReadyMediaAssetInput{
		MIMEType: normalized.mimeType, SizeBytes: normalized.size, Width: normalized.width, Height: normalized.height, SHA256: normalized.sha256,
	})
	if err != nil {
		compensationCtx, cancel := mediaCompensationContext(ctx)
		defer cancel()
		_ = s.store.Delete(compensationCtx, asset.ObjectKey)
		_ = s.repository.MarkFailed(compensationCtx, input.InstanceID, asset.ID)
		return nil, err
	}
	return ready, nil
}

func (s *MediaAssetService) Get(ctx context.Context, instanceID, assetID string) (*campaign_model.MediaAsset, error) {
	if err := s.validate(ctx); err != nil || uuid.Validate(instanceID) != nil || uuid.Validate(assetID) != nil {
		return nil, ErrInvalidMediaUpload
	}
	return s.repository.Get(ctx, instanceID, assetID)
}

func (s *MediaAssetService) Delete(ctx context.Context, instanceID, assetID string) error {
	if err := s.validate(ctx); err != nil || uuid.Validate(instanceID) != nil || uuid.Validate(assetID) != nil {
		return ErrInvalidMediaUpload
	}
	asset, err := s.repository.ClaimDelete(ctx, instanceID, assetID, s.settings.CleanupLease)
	if err != nil {
		return err
	}
	if err := s.store.Delete(ctx, asset.ObjectKey); err != nil {
		compensationCtx, cancel := mediaCompensationContext(ctx)
		defer cancel()
		_ = s.repository.ReleaseCleanup(compensationCtx, asset)
		return fmt.Errorf("%w: %v", ErrMediaStorageUnavailable, err)
	}
	return s.repository.CompleteCleanup(ctx, asset)
}

func (s *MediaAssetService) RunCleanupOnce(ctx context.Context) (int, error) {
	if err := s.validate(ctx); err != nil {
		return 0, err
	}
	assets, err := s.repository.ClaimExpired(ctx, s.settings.CleanupBatch, s.settings.CleanupLease)
	if err != nil {
		return 0, err
	}
	cleaned := 0
	errorsList := make([]error, 0)
	for index := range assets {
		asset := &assets[index]
		if err := s.store.Delete(ctx, asset.ObjectKey); err != nil {
			errorsList = append(errorsList, err)
			compensationCtx, cancel := mediaCompensationContext(ctx)
			if releaseErr := s.repository.ReleaseCleanup(compensationCtx, asset); releaseErr != nil {
				errorsList = append(errorsList, releaseErr)
			}
			cancel()
			continue
		}
		if err := s.repository.CompleteCleanup(ctx, asset); err != nil {
			errorsList = append(errorsList, err)
			continue
		}
		cleaned++
	}
	return cleaned, errors.Join(errorsList...)
}

func (s *MediaAssetService) RunCleanup(ctx context.Context) error {
	if err := s.validate(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(s.settings.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			_, _ = s.RunCleanupOnce(ctx)
		}
	}
}

func (s *MediaAssetService) validate(ctx context.Context) error {
	if s == nil || s.repository == nil || s.store == nil || s.now == nil || ctx == nil {
		return errors.New("campaign media service is unavailable")
	}
	return s.settings.Validate()
}

type normalizedMedia struct {
	file     *os.File
	path     string
	mimeType string
	size     int64
	width    int
	height   int
	sha256   string
}

func (m *normalizedMedia) Close() error {
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

func (s *MediaAssetService) normalize(reader io.Reader) (*normalizedMedia, error) {
	input, err := os.CreateTemp("", "omniwa-campaign-media-input-*")
	if err != nil {
		return nil, err
	}
	inputPath := input.Name()
	defer func() {
		_ = input.Close()
		_ = os.Remove(inputPath)
	}()
	written, err := io.Copy(input, io.LimitReader(reader, s.settings.MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if written == 0 {
		return nil, ErrInvalidMediaUpload
	}
	if written > s.settings.MaxBytes {
		return nil, ErrMediaTooLarge
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	config, format, err := image.DecodeConfig(input)
	if err != nil || format != "jpeg" && format != "png" {
		return nil, ErrUnsupportedMediaType
	}
	if config.Width < 1 || config.Height < 1 || config.Width > 32768 || config.Height > 32768 ||
		int64(config.Width) > s.settings.MaxPixels/int64(config.Height) {
		return nil, ErrInvalidMediaDimension
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	decoded, format, err := image.Decode(input)
	if err != nil {
		return nil, ErrUnsupportedMediaType
	}
	output, err := os.CreateTemp("", "omniwa-campaign-media-normalized-*")
	if err != nil {
		return nil, err
	}
	result := &normalizedMedia{file: output, path: output.Name(), width: config.Width, height: config.Height}
	failed := true
	defer func() {
		if failed {
			_ = result.Close()
		}
	}()
	switch format {
	case "jpeg":
		result.mimeType = "image/jpeg"
		err = jpeg.Encode(output, decoded, &jpeg.Options{Quality: 90})
	case "png":
		result.mimeType = "image/png"
		err = png.Encode(output, decoded)
	default:
		return nil, ErrUnsupportedMediaType
	}
	if err != nil {
		return nil, err
	}
	info, err := output.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() < 1 || info.Size() > s.settings.MaxBytes {
		return nil, ErrMediaTooLarge
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

func scopedMediaHash(instanceID, value string) string {
	sum := sha256.Sum256([]byte(instanceID + "\x00" + value))
	return hex.EncodeToString(sum[:])
}

func mediaObjectKey(instanceID, assetID string) string {
	return "campaign-media/" + instanceID + "/" + assetID + "/image"
}

func mediaCompensationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
}
