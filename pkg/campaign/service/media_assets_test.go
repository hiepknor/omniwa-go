package campaign_service

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"testing"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	campaign_repository "github.com/evolution-foundation/evolution-go/pkg/campaign/repository"
	"github.com/google/uuid"
)

type mediaRepositoryFake struct {
	asset        *campaign_model.MediaAsset
	created      bool
	failed       bool
	claimed      []campaign_model.MediaAsset
	completed    int
	released     int
	createInput  campaign_repository.CreateMediaAssetInput
	readyInput   campaign_repository.ReadyMediaAssetInput
	createErr    error
	markReadyErr error
}

func (f *mediaRepositoryFake) CreateUploading(_ context.Context, input campaign_repository.CreateMediaAssetInput) (*campaign_model.MediaAsset, bool, error) {
	f.createInput = input
	if f.asset == nil {
		f.asset = &campaign_model.MediaAsset{ID: input.ID, InstanceID: input.InstanceID, ObjectKey: input.ObjectKey, Status: campaign_model.MediaAssetStatusUploading}
	}
	return f.asset, f.created, f.createErr
}
func (f *mediaRepositoryFake) Get(context.Context, string, string) (*campaign_model.MediaAsset, error) {
	return f.asset, nil
}
func (f *mediaRepositoryFake) MarkReady(_ context.Context, _, _ string, input campaign_repository.ReadyMediaAssetInput) (*campaign_model.MediaAsset, error) {
	f.readyInput = input
	if f.markReadyErr != nil {
		return nil, f.markReadyErr
	}
	f.asset.Status = campaign_model.MediaAssetStatusReady
	return f.asset, nil
}
func (f *mediaRepositoryFake) MarkFailed(context.Context, string, string) error {
	f.failed = true
	return nil
}
func (f *mediaRepositoryFake) ClaimDelete(context.Context, string, string, time.Duration) (*campaign_model.MediaAsset, error) {
	return f.asset, nil
}
func (f *mediaRepositoryFake) ClaimExpired(context.Context, int, time.Duration) ([]campaign_model.MediaAsset, error) {
	return f.claimed, nil
}
func (f *mediaRepositoryFake) CompleteCleanup(context.Context, *campaign_model.MediaAsset) error {
	f.completed++
	return nil
}
func (f *mediaRepositoryFake) ReleaseCleanup(context.Context, *campaign_model.MediaAsset) error {
	f.released++
	return nil
}

type campaignMediaStoreFake struct {
	content []byte
	key     string
	mime    string
	health  error
	put     error
	delete  error
}

func (f *campaignMediaStoreFake) Put(_ context.Context, key string, reader io.Reader, _ int64, mime string) error {
	f.key, f.mime = key, mime
	if f.put != nil {
		return f.put
	}
	f.content, _ = io.ReadAll(reader)
	return nil
}
func (f *campaignMediaStoreFake) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(f.content)), nil
}
func (f *campaignMediaStoreFake) Delete(context.Context, string) error { return f.delete }
func (f *campaignMediaStoreFake) Health(context.Context) error         { return f.health }

func TestMediaUploadNormalizesImageAndScopesIdempotency(t *testing.T) {
	instanceID := uuid.NewString()
	repository := &mediaRepositoryFake{created: true}
	store := &campaignMediaStoreFake{}
	service := newTestMediaService(repository, store)
	asset, err := service.Upload(context.Background(), MediaUploadInput{
		InstanceID: instanceID, IdempotencyKey: "device-upload-1", Reader: bytes.NewReader(testJPEG(t, 4, 3)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if asset.Status != campaign_model.MediaAssetStatusReady || repository.createInput.RequestReferenceHash == nil ||
		*repository.createInput.RequestReferenceHash == "device-upload-1" || repository.readyInput.Width != 4 || repository.readyInput.Height != 3 ||
		repository.readyInput.MIMEType != "image/jpeg" || len(repository.readyInput.SHA256) != 64 || store.mime != "image/jpeg" {
		t.Fatalf("asset=%+v create=%+v ready=%+v store=%+v", asset, repository.createInput, repository.readyInput, store)
	}
	if _, _, err := image.Decode(bytes.NewReader(store.content)); err != nil {
		t.Fatalf("stored normalized image is invalid: %v", err)
	}
	if store.key != repository.createInput.ObjectKey || !bytes.HasPrefix(store.content, []byte{0xff, 0xd8}) {
		t.Fatalf("unexpected stored object key=%q bytes=%x", store.key, store.content[:2])
	}
}

func TestMediaUploadRejectsUnsafeInputBeforeStorage(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
		err  error
	}{
		{name: "empty", data: nil, err: ErrInvalidMediaUpload},
		{name: "unsupported", data: []byte("not an image"), err: ErrUnsupportedMediaType},
		{name: "too large", data: bytes.Repeat([]byte("x"), 1025), err: ErrMediaTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &mediaRepositoryFake{created: true}
			store := &campaignMediaStoreFake{}
			service := newTestMediaService(repository, store)
			service.settings.MaxBytes = 1024
			_, err := service.Upload(context.Background(), MediaUploadInput{InstanceID: uuid.NewString(), Reader: bytes.NewReader(test.data)})
			if !errors.Is(err, test.err) || !repository.failed || len(store.content) != 0 {
				t.Fatalf("err=%v failed=%t stored=%d", err, repository.failed, len(store.content))
			}
		})
	}
}

func TestMediaUploadStorageFailureLeavesFailedAsset(t *testing.T) {
	repository := &mediaRepositoryFake{created: true}
	store := &campaignMediaStoreFake{put: errors.New("object store down")}
	service := newTestMediaService(repository, store)
	_, err := service.Upload(context.Background(), MediaUploadInput{InstanceID: uuid.NewString(), Reader: bytes.NewReader(testJPEG(t, 2, 2))})
	if !errors.Is(err, ErrMediaStorageUnavailable) || !repository.failed {
		t.Fatalf("err=%v failed=%t", err, repository.failed)
	}
}

func newTestMediaService(repository campaign_repository.MediaAssetRepository, store *campaignMediaStoreFake) *MediaAssetService {
	return NewMediaAssetService(repository, store, MediaSettings{
		MaxBytes: 8 * 1024 * 1024, MaxPixels: 16_000_000, UnboundTTL: time.Hour,
		CleanupBatch: 10, CleanupLease: time.Minute, CleanupInterval: time.Minute,
	})
}

func testJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	var data bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := jpeg.Encode(&data, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}
