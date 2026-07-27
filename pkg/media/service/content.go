package media_service

import (
	"context"
	"errors"
	"io"

	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	storage_interfaces "github.com/evolution-foundation/evolution-go/pkg/storage/interfaces"
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
	asset, err := s.repository.Get(ctx, instanceID, assetID)
	if err != nil {
		return nil, err
	}
	if asset.Status != media_model.AssetStatusReady || asset.Canonical == nil || asset.CleanupClaimToken != nil || asset.DeletedAt != nil {
		return nil, ErrMediaAssetNotReady
	}
	variant := asset.Canonical
	if variant.SizeBytes < 1 || variant.SizeBytes > s.settings.MaxBytes || offset >= variant.SizeBytes || length > variant.SizeBytes-offset {
		return nil, ErrInvalidMediaAsset
	}
	var reader io.ReadCloser
	if ranged, ok := s.store.(storage_interfaces.MediaAssetRangeStore); ok {
		reader, err = ranged.OpenRange(ctx, variant.ObjectKey, offset, length)
	} else {
		reader, err = s.store.Open(ctx, variant.ObjectKey)
		if err == nil && offset > 0 {
			_, err = io.CopyN(io.Discard, reader, offset)
		}
	}
	if err != nil {
		if reader != nil {
			_ = reader.Close()
		}
		return nil, errors.Join(ErrMediaAssetStorage, err)
	}
	return &AssetContent{Reader: reader, MIMEType: variant.MIMEType, SHA256: variant.SHA256, Total: variant.SizeBytes, Offset: offset, Length: length}, nil
}
