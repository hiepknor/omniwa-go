package media_service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"

	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	"github.com/google/uuid"
)

type AssetContent struct {
	Reader   io.ReadCloser
	MIMEType string
	SHA256   string
	Total    int64
	Offset   int64
	Length   int64
}

func (s *AssetService) OpenContent(ctx context.Context, instanceID, assetID string, offset, length int64) (*AssetContent, error) {
	if err := s.validate(ctx); err != nil || uuid.Validate(instanceID) != nil || uuid.Validate(assetID) != nil || offset < 0 || length < 1 {
		return nil, ErrInvalidMediaAsset
	}
	asset, err := s.repository.GetMetadata(ctx, instanceID, assetID)
	if err != nil {
		return nil, err
	}
	if asset.InstanceID != instanceID {
		return nil, ErrMediaAssetInstance
	}
	if err := AssetAvailability(asset, s.now().UTC()); err != nil {
		return nil, err
	}
	if asset.Canonical == nil {
		return nil, ErrMediaAssetIntegrity
	}
	variant := asset.Canonical
	if variant.MediaAssetID != asset.ID || variant.InstanceID != instanceID || variant.Kind != media_model.VariantCanonical ||
		variant.MIMEType != "image/jpeg" && variant.MIMEType != "image/png" || variant.SizeBytes < 1 || variant.SizeBytes > s.settings.MaxBytes ||
		len(variant.SHA256) != 64 {
		return nil, ErrMediaAssetIntegrity
	}
	if offset >= variant.SizeBytes || length > variant.SizeBytes-offset {
		return nil, ErrInvalidMediaAsset
	}
	object, err := s.store.Open(ctx, variant.ObjectKey)
	if err != nil {
		return nil, errors.Join(ErrMediaAssetStorage, err)
	}
	temporary, err := os.CreateTemp("", "omniwa-media-content-*")
	if err != nil {
		_ = object.Close()
		return nil, errors.Join(ErrMediaAssetStorage, err)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = temporary.Close()
			_ = os.Remove(temporary.Name())
		}
	}()
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(object, s.settings.MaxBytes+1))
	closeErr := object.Close()
	if copyErr != nil || closeErr != nil {
		return nil, errors.Join(ErrMediaAssetStorage, copyErr, closeErr)
	}
	if written != variant.SizeBytes || written > s.settings.MaxBytes || hex.EncodeToString(hash.Sum(nil)) != variant.SHA256 {
		return nil, ErrMediaAssetIntegrity
	}
	if _, err := temporary.Seek(offset, io.SeekStart); err != nil {
		return nil, errors.Join(ErrMediaAssetStorage, err)
	}
	removeTemporary = false
	return &AssetContent{Reader: &temporaryContent{File: temporary}, MIMEType: variant.MIMEType, SHA256: variant.SHA256, Total: variant.SizeBytes, Offset: offset, Length: length}, nil
}

type temporaryContent struct{ *os.File }

func (content *temporaryContent) Close() error {
	name := content.Name()
	return errors.Join(content.File.Close(), os.Remove(name))
}
