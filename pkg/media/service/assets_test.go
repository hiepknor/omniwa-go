package media_service

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"testing"
	"time"

	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	media_repository "github.com/evolution-foundation/evolution-go/pkg/media/repository"
	"github.com/google/uuid"
)

type assetUploadRepositoryFake struct {
	media_repository.Repository
	created media_repository.CreateAssetInput
	variant media_model.AssetVariant
}

func (f *assetUploadRepositoryFake) Create(_ context.Context, input media_repository.CreateAssetInput) (*media_model.Asset, bool, error) {
	f.created = input
	return &media_model.Asset{ID: input.ID, InstanceID: input.InstanceID, Origin: input.Origin, Status: input.Status, ExpiresAt: input.ExpiresAt}, true, nil
}
func (f *assetUploadRepositoryFake) AddVariant(_ context.Context, variant media_model.AssetVariant) error {
	f.variant = variant
	return nil
}
func (f *assetUploadRepositoryFake) MarkReady(_ context.Context, instanceID, assetID string) (*media_model.Asset, error) {
	return &media_model.Asset{ID: assetID, InstanceID: instanceID, Status: media_model.AssetStatusReady, Canonical: &f.variant}, nil
}
func (f *assetUploadRepositoryFake) MarkFailed(context.Context, string, string, string) error {
	return nil
}

type assetUploadStoreFake struct {
	key, mime string
	size      int64
	bytes     []byte
}

func (f *assetUploadStoreFake) Put(_ context.Context, key string, reader io.Reader, size int64, mime string) error {
	f.key, f.mime, f.size = key, mime, size
	f.bytes, _ = io.ReadAll(reader)
	return nil
}
func (f *assetUploadStoreFake) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("not used")
}
func (f *assetUploadStoreFake) Delete(context.Context, string) error { return nil }
func (f *assetUploadStoreFake) Health(context.Context) error         { return nil }

func TestNormalizeAssetImageAcceptsPNGAndRewindsCanonicalFile(t *testing.T) {
	var source bytes.Buffer
	input := image.NewRGBA(image.Rect(0, 0, 2, 3))
	input.Set(1, 2, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&source, input); err != nil {
		t.Fatal(err)
	}
	normalized, err := normalizeAssetImage(&source, 1024*1024, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer normalized.Close()
	if normalized.mimeType != "image/png" || normalized.width != 2 || normalized.height != 3 || normalized.size < 1 || len(normalized.sha256) != 64 {
		t.Fatalf("normalized=%+v", normalized)
	}
	header := make([]byte, 8)
	if _, err := normalized.file.Read(header); err != nil || !bytes.Equal(header, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("canonical file was not rewound: %x, %v", header, err)
	}
}

func TestNormalizeAssetImageRejectsUnsupportedAndOversizedInput(t *testing.T) {
	if _, err := normalizeAssetImage(bytes.NewBufferString("not-an-image"), 1024, 100); !errors.Is(err, ErrUnsupportedMediaAsset) {
		t.Fatalf("unsupported error=%v", err)
	}
	if _, err := normalizeAssetImage(bytes.NewBuffer(bytes.Repeat([]byte("x"), 11)), 10, 100); !errors.Is(err, ErrMediaAssetTooLarge) {
		t.Fatalf("oversized error=%v", err)
	}
}

func TestAssetServiceUploadWritesCanonicalPrivateVariant(t *testing.T) {
	var source bytes.Buffer
	if err := png.Encode(&source, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	instanceID := uuid.NewString()
	repository := &assetUploadRepositoryFake{}
	store := &assetUploadStoreFake{}
	service := NewAssetService(repository, store, AssetSettings{MaxBytes: 1024 * 1024, MaxPixels: 100, UnboundTTL: time.Hour, DeleteLease: time.Minute})
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	asset, err := service.Upload(context.Background(), AssetUploadInput{InstanceID: instanceID, IdempotencyKey: "upload-1", Reader: &source})
	if err != nil || asset == nil || asset.Status != media_model.AssetStatusReady {
		t.Fatalf("asset=%+v err=%v", asset, err)
	}
	if repository.created.RequestReferenceHash == nil || repository.created.ExpiresAt == nil || !repository.created.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("create input=%+v", repository.created)
	}
	wantKey := "media-assets/" + instanceID + "/" + repository.created.ID + "/canonical"
	if store.key != wantKey || repository.variant.ObjectKey != wantKey || store.mime != "image/png" || store.size != int64(len(store.bytes)) || repository.variant.SHA256 != sha256Bytes(store.bytes) {
		t.Fatalf("store=%+v variant=%+v", store, repository.variant)
	}
}
