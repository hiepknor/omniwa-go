package media_service

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	media_repository "github.com/evolution-foundation/evolution-go/pkg/media/repository"
	"github.com/google/uuid"
)

type contentRepositoryFake struct {
	media_repository.Repository
	asset *media_model.Asset
	err   error
}

func (f *contentRepositoryFake) Get(_ context.Context, instanceID, assetID string) (*media_model.Asset, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.asset == nil || f.asset.InstanceID != instanceID || f.asset.ID != assetID {
		return nil, media_repository.ErrAssetNotFound
	}
	return f.asset, nil
}

type rangeStoreFake struct {
	assetUploadStoreFake
	key            string
	offset, length int64
}

func (f *rangeStoreFake) OpenRange(_ context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	f.key, f.offset, f.length = key, offset, length
	return io.NopCloser(strings.NewReader("content")), nil
}

func TestOpenContentUsesCanonicalInstanceScopedRange(t *testing.T) {
	instanceID, assetID := uuid.NewString(), uuid.NewString()
	repository := &contentRepositoryFake{asset: &media_model.Asset{
		ID: assetID, InstanceID: instanceID, Status: media_model.AssetStatusReady,
		Canonical: &media_model.AssetVariant{ObjectKey: "media-assets/" + instanceID + "/" + assetID + "/canonical", MIMEType: "image/png", SizeBytes: 7, SHA256: strings.Repeat("a", 64)},
	}}
	store := &rangeStoreFake{}
	service := NewAssetService(repository, store, AssetSettings{MaxBytes: 1024, MaxPixels: 100, UnboundTTL: time.Hour, DeleteLease: time.Minute})
	content, err := service.OpenContent(context.Background(), instanceID, assetID, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer content.Reader.Close()
	if content.Offset != 2 || content.Length != 3 || content.Total != 7 || store.offset != 2 || store.length != 3 || store.key != repository.asset.Canonical.ObjectKey {
		t.Fatalf("content=%+v store=%+v", content, store)
	}
	if _, err := service.OpenContent(context.Background(), uuid.NewString(), assetID, 0, 1); !errors.Is(err, media_repository.ErrAssetNotFound) {
		t.Fatalf("cross-instance error=%v", err)
	}
}

func TestOpenContentRejectsNonReadyAndOutOfBounds(t *testing.T) {
	instanceID, assetID := uuid.NewString(), uuid.NewString()
	asset := &media_model.Asset{ID: assetID, InstanceID: instanceID, Status: media_model.AssetStatusProcessing}
	service := NewAssetService(&contentRepositoryFake{asset: asset}, &rangeStoreFake{}, AssetSettings{
		MaxBytes: 1024, MaxPixels: 100, UnboundTTL: time.Hour, DeleteLease: time.Minute,
	})
	if _, err := service.OpenContent(context.Background(), instanceID, assetID, 0, 1); !errors.Is(err, ErrMediaAssetNotReady) {
		t.Fatalf("non-ready error=%v", err)
	}
	asset.Status = media_model.AssetStatusReady
	asset.Canonical = &media_model.AssetVariant{SizeBytes: 4, ObjectKey: "media-assets/" + instanceID + "/" + assetID + "/canonical"}
	if _, err := service.OpenContent(context.Background(), instanceID, assetID, 3, 2); !errors.Is(err, ErrInvalidMediaAsset) {
		t.Fatalf("out-of-bounds error=%v", err)
	}
}
