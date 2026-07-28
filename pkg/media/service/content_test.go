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

func (f *contentRepositoryFake) GetMetadata(ctx context.Context, instanceID, assetID string) (*media_model.Asset, error) {
	return f.Get(ctx, instanceID, assetID)
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
	key     string
	content string
}

func (f *rangeStoreFake) Open(_ context.Context, key string) (io.ReadCloser, error) {
	f.key = key
	content := f.content
	if content == "" {
		content = "content"
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

func TestOpenContentRejectsFailedExpiredDeletedAndTamperedAssets(t *testing.T) {
	instanceID, assetID := uuid.NewString(), uuid.NewString()
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	asset := &media_model.Asset{ID: assetID, InstanceID: instanceID, Status: media_model.AssetStatusFailed}
	service := NewAssetService(&contentRepositoryFake{asset: asset}, &rangeStoreFake{}, AssetSettings{
		MaxBytes: 1024, MaxPixels: 100, UnboundTTL: time.Hour, DeleteLease: time.Minute,
	})
	service.now = func() time.Time { return now }
	if _, err := service.OpenContent(context.Background(), instanceID, assetID, 0, 1); !errors.Is(err, ErrMediaAssetFailed) {
		t.Fatalf("failed error=%v", err)
	}
	asset.Status = media_model.AssetStatusReady
	expired := now.Add(-time.Second)
	asset.ExpiresAt = &expired
	if _, err := service.OpenContent(context.Background(), instanceID, assetID, 0, 1); !errors.Is(err, ErrMediaAssetExpired) {
		t.Fatalf("expired error=%v", err)
	}
	asset.Status = media_model.AssetStatusDeleted
	deleted := now
	asset.DeletedAt = &deleted
	if _, err := service.OpenContent(context.Background(), instanceID, assetID, 0, 1); !errors.Is(err, ErrMediaAssetDeleted) {
		t.Fatalf("deleted error=%v", err)
	}
	asset.Status, asset.DeletedAt, asset.ExpiresAt = media_model.AssetStatusReady, nil, nil
	asset.Canonical = &media_model.AssetVariant{
		MediaAssetID: assetID, InstanceID: instanceID, Kind: media_model.VariantCanonical,
		ObjectKey: "media-assets/" + instanceID + "/" + assetID + "/canonical", MIMEType: "image/png",
		SizeBytes: 7, SHA256: sha256Bytes([]byte("content")),
	}
	service.store = &rangeStoreFake{content: "altered"}
	if _, err := service.OpenContent(context.Background(), instanceID, assetID, 0, 1); !errors.Is(err, ErrMediaAssetIntegrity) {
		t.Fatalf("integrity error=%v", err)
	}
}

func TestOpenContentUsesCanonicalInstanceScopedRange(t *testing.T) {
	instanceID, assetID := uuid.NewString(), uuid.NewString()
	repository := &contentRepositoryFake{asset: &media_model.Asset{
		ID: assetID, InstanceID: instanceID, Status: media_model.AssetStatusReady,
		Canonical: &media_model.AssetVariant{MediaAssetID: assetID, InstanceID: instanceID, Kind: media_model.VariantCanonical, ObjectKey: "media-assets/" + instanceID + "/" + assetID + "/canonical", MIMEType: "image/png", SizeBytes: 7, SHA256: sha256Bytes([]byte("content"))},
	}}
	store := &rangeStoreFake{}
	service := NewAssetService(repository, store, AssetSettings{MaxBytes: 1024, MaxPixels: 100, UnboundTTL: time.Hour, DeleteLease: time.Minute})
	content, err := service.OpenContent(context.Background(), instanceID, assetID, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer content.Reader.Close()
	read, readErr := io.ReadAll(content.Reader)
	if content.Offset != 2 || content.Length != 3 || content.Total != 7 || store.key != repository.asset.Canonical.ObjectKey || readErr != nil || string(read[:3]) != "nte" {
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
	asset.Canonical = &media_model.AssetVariant{MediaAssetID: assetID, InstanceID: instanceID, Kind: media_model.VariantCanonical, MIMEType: "image/png", SizeBytes: 4, SHA256: sha256Bytes([]byte("data")), ObjectKey: "media-assets/" + instanceID + "/" + assetID + "/canonical"}
	if _, err := service.OpenContent(context.Background(), instanceID, assetID, 3, 2); !errors.Is(err, ErrInvalidMediaAsset) {
		t.Fatalf("out-of-bounds error=%v", err)
	}
}
