package group_service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"strings"
	"time"

	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	media_repository "github.com/evolution-foundation/evolution-go/pkg/media/repository"
	media_service "github.com/evolution-foundation/evolution-go/pkg/media/service"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types"
	"golang.org/x/image/draw"
)

var (
	ErrGroupPhotoAssetNotFound    = errors.New("group photo media asset not found")
	ErrGroupPhotoAssetNotReady    = errors.New("group photo media asset is not ready")
	ErrGroupPhotoAssetInvalidType = errors.New("group photo media asset has an invalid type")
	ErrGroupPhotoAssetTooLarge    = errors.New("group photo media asset is too large")
	ErrGroupPhotoAssetIntegrity   = errors.New("group photo media asset integrity check failed")
	ErrGroupPhotoAssetStorage     = errors.New("group photo media asset storage is unavailable")
)

const groupPhotoOutputPixels = 640
const groupPhotoReservationTTL = 24 * time.Hour

type groupPhotoAssetReader interface {
	Get(context.Context, string, string) (*media_model.Asset, error)
	OpenContent(context.Context, string, string, int64, int64) (*media_service.AssetContent, error)
}

type groupPhotoReferenceRepository interface {
	AddReference(context.Context, media_model.AssetReference) error
	ReplaceOwnerReference(context.Context, media_model.AssetReference) error
	RemoveReference(context.Context, string, string, media_model.ReferenceOwnerType, string) error
}

type PreparedGroupPhoto struct {
	MediaAssetID string
	Bytes        []byte
}

type GroupPhotoAssetService struct {
	assets      groupPhotoAssetReader
	references  groupPhotoReferenceRepository
	groupWriter *projection_service.GroupWriter
	maxBytes    int64
	maxPixels   int64
}

func NewGroupPhotoAssetService(assets groupPhotoAssetReader, references groupPhotoReferenceRepository, groupWriter *projection_service.GroupWriter, maxBytes, maxPixels int64) *GroupPhotoAssetService {
	return &GroupPhotoAssetService{assets: assets, references: references, groupWriter: groupWriter, maxBytes: maxBytes, maxPixels: maxPixels}
}

func (s *GroupPhotoAssetService) Prepare(ctx context.Context, instanceID, groupJID, mediaAssetID, commandID string) (*PreparedGroupPhoto, error) {
	jid, err := types.ParseJID(groupJID)
	if s == nil || s.assets == nil || s.references == nil || s.groupWriter == nil || ctx == nil || uuid.Validate(instanceID) != nil ||
		err != nil || jid.Server != types.GroupServer || jid.String() != groupJID || uuid.Validate(mediaAssetID) != nil || uuid.Validate(commandID) != nil || s.maxBytes < 1 || s.maxPixels < 1 {
		return nil, ErrInvalidManagementFilter
	}
	asset, err := s.assets.Get(ctx, instanceID, mediaAssetID)
	if errors.Is(err, media_repository.ErrAssetNotFound) {
		return nil, ErrGroupPhotoAssetNotFound
	}
	if err != nil {
		return nil, err
	}
	if asset.Status != media_model.AssetStatusReady || asset.Canonical == nil || asset.CleanupClaimToken != nil || asset.DeletedAt != nil {
		return nil, ErrGroupPhotoAssetNotReady
	}
	variant := asset.Canonical
	if asset.Origin != media_model.AssetOriginDeviceUpload || variant.MIMEType != "image/jpeg" && variant.MIMEType != "image/png" {
		return nil, ErrGroupPhotoAssetInvalidType
	}
	if variant.SizeBytes < 1 || variant.SizeBytes > s.maxBytes || variant.Width < 1 || variant.Height < 1 || int64(variant.Width) > s.maxPixels/int64(variant.Height) {
		return nil, ErrGroupPhotoAssetTooLarge
	}
	retentionUntil := time.Now().UTC().Add(groupPhotoReservationTTL)
	reference := media_model.AssetReference{InstanceID: instanceID, MediaAssetID: mediaAssetID, OwnerType: media_model.ReferenceOwnerGroupPhotoPending, OwnerID: commandID, RetentionUntil: &retentionUntil}
	if err := s.references.AddReference(ctx, reference); err != nil {
		if errors.Is(err, media_repository.ErrAssetNotFound) {
			return nil, ErrGroupPhotoAssetNotFound
		}
		if errors.Is(err, media_repository.ErrAssetConflict) {
			return nil, ErrGroupPhotoAssetNotReady
		}
		return nil, err
	}
	content, err := s.assets.OpenContent(ctx, instanceID, mediaAssetID, 0, variant.SizeBytes)
	if err != nil {
		switch {
		case errors.Is(err, media_repository.ErrAssetNotFound):
			return nil, ErrGroupPhotoAssetNotFound
		case errors.Is(err, media_service.ErrMediaAssetNotReady):
			return nil, ErrGroupPhotoAssetNotReady
		case errors.Is(err, media_service.ErrMediaAssetStorage):
			return nil, ErrGroupPhotoAssetStorage
		}
		return nil, err
	}
	defer content.Reader.Close()
	encoded, err := io.ReadAll(io.LimitReader(content.Reader, s.maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(encoded)) != variant.SizeBytes || int64(len(encoded)) > s.maxBytes {
		return nil, ErrGroupPhotoAssetTooLarge
	}
	digest := sha256.Sum256(encoded)
	if !strings.EqualFold(variant.SHA256, fmt.Sprintf("%x", digest[:])) {
		return nil, ErrGroupPhotoAssetIntegrity
	}
	normalized, err := normalizeGroupPhoto(encoded, variant.Width, variant.Height, s.maxPixels)
	if err != nil {
		return nil, err
	}
	return &PreparedGroupPhoto{MediaAssetID: mediaAssetID, Bytes: normalized}, nil
}

func (s *GroupPhotoAssetService) Commit(ctx context.Context, instanceID, groupJID, pictureID, mediaAssetID, commandID string) error {
	if s == nil || s.references == nil || s.groupWriter == nil {
		return errors.New("group photo asset service is unavailable")
	}
	if err := s.groupWriter.WritePhoto(ctx, instanceID, groupJID, pictureID, mediaAssetID); err != nil {
		return err
	}
	if err := s.references.ReplaceOwnerReference(ctx, media_model.AssetReference{
		InstanceID: instanceID, MediaAssetID: mediaAssetID, OwnerType: media_model.ReferenceOwnerGroupPhoto, OwnerID: groupJID,
	}); err != nil {
		return err
	}
	// The durable owner now fences cleanup. A failed reservation cleanup is safe
	// because pending references expire automatically.
	_ = s.references.RemoveReference(ctx, instanceID, mediaAssetID, media_model.ReferenceOwnerGroupPhotoPending, commandID)
	return nil
}

func normalizeGroupPhoto(encoded []byte, expectedWidth, expectedHeight int, maxPixels int64) ([]byte, error) {
	configuration, _, err := image.DecodeConfig(bytes.NewReader(encoded))
	if err != nil || configuration.Width != expectedWidth || configuration.Height != expectedHeight || configuration.Width < 1 || configuration.Height < 1 ||
		int64(configuration.Width) > maxPixels/int64(configuration.Height) {
		return nil, ErrGroupPhotoAssetInvalidType
	}
	source, _, err := image.Decode(bytes.NewReader(encoded))
	if err != nil {
		return nil, ErrGroupPhotoAssetInvalidType
	}
	bounds := source.Bounds()
	side := min(bounds.Dx(), bounds.Dy())
	left := bounds.Min.X + (bounds.Dx()-side)/2
	top := bounds.Min.Y + (bounds.Dy()-side)/2
	destination := image.NewRGBA(image.Rect(0, 0, groupPhotoOutputPixels, groupPhotoOutputPixels))
	draw.CatmullRom.Scale(destination, destination.Bounds(), source, image.Rect(left, top, left+side, top+side), draw.Over, nil)
	var output bytes.Buffer
	if err := jpeg.Encode(&output, destination, &jpeg.Options{Quality: 90}); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
