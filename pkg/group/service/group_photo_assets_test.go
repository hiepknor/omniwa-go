package group_service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"testing"
	"time"

	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	media_repository "github.com/evolution-foundation/evolution-go/pkg/media/repository"
	media_service "github.com/evolution-foundation/evolution-go/pkg/media/service"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/google/uuid"
)

type groupPhotoAssetReaderStub struct {
	instanceID string
	asset      *media_model.Asset
	content    []byte
}

func (s *groupPhotoAssetReaderStub) Get(_ context.Context, instanceID, _ string) (*media_model.Asset, error) {
	if instanceID != s.instanceID {
		return nil, media_repository.ErrAssetNotFound
	}
	return s.asset, nil
}

func (s *groupPhotoAssetReaderStub) OpenContent(_ context.Context, instanceID, _ string, offset, length int64) (*media_service.AssetContent, error) {
	if instanceID != s.instanceID || offset != 0 || length != int64(len(s.content)) {
		return nil, media_repository.ErrAssetNotFound
	}
	return &media_service.AssetContent{Reader: io.NopCloser(bytes.NewReader(s.content)), Length: length, Total: length}, nil
}

type groupPhotoReferencesStub struct {
	added    []media_model.AssetReference
	replaced []media_model.AssetReference
	removed  []media_model.AssetReference
}

func (s *groupPhotoReferencesStub) AddReference(_ context.Context, reference media_model.AssetReference) error {
	s.added = append(s.added, reference)
	return nil
}

func (s *groupPhotoReferencesStub) ReplaceOwnerReference(_ context.Context, reference media_model.AssetReference) error {
	s.replaced = append(s.replaced, reference)
	return nil
}

func (s *groupPhotoReferencesStub) RemoveReference(_ context.Context, instanceID, assetID string, ownerType media_model.ReferenceOwnerType, ownerID string) error {
	s.removed = append(s.removed, media_model.AssetReference{InstanceID: instanceID, MediaAssetID: assetID, OwnerType: ownerType, OwnerID: ownerID})
	return nil
}

type groupPhotoProjectionRepositoryStub struct {
	patches []projection_repository.GroupPatch
	err     error
}

func (*groupPhotoProjectionRepositoryStub) ApplySnapshot(context.Context, *projection_model.Group, []projection_model.GroupParticipant) (bool, error) {
	return true, nil
}
func (s *groupPhotoProjectionRepositoryStub) ApplyPatch(_ context.Context, patch projection_repository.GroupPatch) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	s.patches = append(s.patches, patch)
	return true, nil
}
func (*groupPhotoProjectionRepositoryStub) Tombstone(context.Context, string, string, string, time.Time, projection_model.GroupTombstoneCause) (bool, error) {
	return true, nil
}

type groupPhotoProjectionStateStub struct{}

func (groupPhotoProjectionStateStub) RecordEvent(string, string, int64, time.Time) error { return nil }
func (groupPhotoProjectionStateStub) MarkStale(string, string, int64) error              { return nil }

func TestGroupPhotoAssetPrepareNormalizesAndReservesInstanceAsset(t *testing.T) {
	instanceID, assetID := uuid.NewString(), uuid.NewString()
	imageBytes := testGroupPhotoPNG(t, 800, 600)
	reader := &groupPhotoAssetReaderStub{instanceID: instanceID, content: imageBytes}
	reader.asset = &media_model.Asset{ID: assetID, InstanceID: instanceID, Origin: media_model.AssetOriginDeviceUpload, Status: media_model.AssetStatusReady,
		Canonical: &media_model.AssetVariant{MediaAssetID: assetID, InstanceID: instanceID, Kind: media_model.VariantCanonical, MIMEType: "image/png", SizeBytes: int64(len(imageBytes)), Width: 800, Height: 600, SHA256: groupPhotoSHA(imageBytes)}}
	references := &groupPhotoReferencesStub{}
	service := NewGroupPhotoAssetService(reader, references, &projection_service.GroupWriter{}, 4<<20, 2_000_000)
	commandID := uuid.NewString()

	prepared, err := service.Prepare(context.Background(), instanceID, "123@g.us", assetID, commandID)
	if err != nil || prepared.MediaAssetID != assetID || len(references.added) != 1 || references.added[0].OwnerType != media_model.ReferenceOwnerGroupPhotoPending || references.added[0].RetentionUntil == nil {
		t.Fatalf("prepared=%#v error=%v references=%#v", prepared, err, references.added)
	}
	configuration, format, err := image.DecodeConfig(bytes.NewReader(prepared.Bytes))
	if err != nil || format != "jpeg" || configuration.Width != groupPhotoOutputPixels || configuration.Height != groupPhotoOutputPixels {
		t.Fatalf("normalized photo = %dx%d %s error=%v", configuration.Width, configuration.Height, format, err)
	}
}

func TestGroupPhotoAssetPrepareRejectsStorageIntegrityMismatch(t *testing.T) {
	instanceID, assetID, commandID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	imageBytes := testGroupPhotoPNG(t, 16, 16)
	reader := &groupPhotoAssetReaderStub{instanceID: instanceID, content: imageBytes, asset: &media_model.Asset{
		ID: assetID, InstanceID: instanceID, Origin: media_model.AssetOriginDeviceUpload, Status: media_model.AssetStatusReady,
		Canonical: &media_model.AssetVariant{MIMEType: "image/png", SizeBytes: int64(len(imageBytes)), Width: 16, Height: 16, SHA256: fmt.Sprintf("%064x", 1)},
	}}
	service := NewGroupPhotoAssetService(reader, &groupPhotoReferencesStub{}, &projection_service.GroupWriter{}, 1<<20, 1024)
	if _, err := service.Prepare(context.Background(), instanceID, "123@g.us", assetID, commandID); !errors.Is(err, ErrGroupPhotoAssetIntegrity) {
		t.Fatalf("Prepare() error = %v", err)
	}
}

func TestGroupPhotoAssetPrepareEnforcesInstanceOwnership(t *testing.T) {
	reader := &groupPhotoAssetReaderStub{instanceID: uuid.NewString()}
	service := NewGroupPhotoAssetService(reader, &groupPhotoReferencesStub{}, &projection_service.GroupWriter{}, 1024, 1024)
	_, err := service.Prepare(context.Background(), uuid.NewString(), "123@g.us", uuid.NewString(), uuid.NewString())
	if err != ErrGroupPhotoAssetNotFound {
		t.Fatalf("Prepare() error = %v", err)
	}
}

func TestGroupPhotoAssetCommitWritesProjectionAndReplacesReference(t *testing.T) {
	instanceID, assetID := uuid.NewString(), uuid.NewString()
	projectionRepository := &groupPhotoProjectionRepositoryStub{}
	writer := projection_service.NewGroupWriter(projectionRepository, groupPhotoProjectionStateStub{})
	references := &groupPhotoReferencesStub{}
	service := NewGroupPhotoAssetService(&groupPhotoAssetReaderStub{}, references, writer, 1024, 1024)
	commandID := uuid.NewString()
	if err := service.Commit(context.Background(), instanceID, "123@g.us", "provider-picture-id", assetID, commandID); err != nil {
		t.Fatal(err)
	}
	if len(projectionRepository.patches) != 1 || projectionRepository.patches[0].PictureMediaAssetID == nil || *projectionRepository.patches[0].PictureMediaAssetID != assetID || len(references.replaced) != 1 || len(references.removed) != 1 || references.removed[0].OwnerID != commandID {
		t.Fatalf("patches=%#v references=%#v", projectionRepository.patches, references.replaced)
	}
}

func testGroupPhotoPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	value := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			value.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 100, A: 255})
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, value); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func groupPhotoSHA(value []byte) string {
	digest := sha256.Sum256(value)
	return fmt.Sprintf("%x", digest[:])
}
