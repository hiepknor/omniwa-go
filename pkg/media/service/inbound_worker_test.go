package media_service

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"go.mau.fi/whatsmeow"
)

type inboundWorkerRepositoryFake struct {
	media_repository.InboundRepository
	jobs      []media_model.DownloadJob
	completed bool
	retried   bool
	failed    bool
	original  media_model.AssetVariant
	canonical media_model.AssetVariant
}

func (f *inboundWorkerRepositoryFake) ClaimDownloads(context.Context, int, time.Duration) ([]media_model.DownloadJob, error) {
	if len(f.jobs) == 0 {
		return nil, nil
	}
	job := f.jobs[0]
	f.jobs = f.jobs[1:]
	return []media_model.DownloadJob{job}, nil
}
func (f *inboundWorkerRepositoryFake) CompleteDownload(_ context.Context, _ *media_model.DownloadJob, original, canonical media_model.AssetVariant) error {
	f.completed, f.original, f.canonical = true, original, canonical
	return nil
}
func (f *inboundWorkerRepositoryFake) RetryDownload(context.Context, *media_model.DownloadJob, string, time.Time) error {
	f.retried = true
	return nil
}
func (f *inboundWorkerRepositoryFake) FailDownload(context.Context, *media_model.DownloadJob, string) error {
	f.failed = true
	return nil
}

type inboundDownloaderFake struct {
	data []byte
	err  error
}

func (f *inboundDownloaderFake) Download(_ context.Context, _ string, _ InboundImageDescriptor, target whatsmeow.File) error {
	if f.err != nil {
		return f.err
	}
	_, err := target.Write(f.data)
	return err
}

type inboundStoreFake struct{ objects map[string][]byte }

func (f *inboundStoreFake) Put(_ context.Context, key string, reader io.Reader, _ int64, _ string) error {
	data, err := io.ReadAll(reader)
	if err == nil {
		f.objects[key] = data
	}
	return err
}
func (f *inboundStoreFake) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("not used")
}
func (f *inboundStoreFake) Delete(_ context.Context, key string) error {
	delete(f.objects, key)
	return nil
}
func (f *inboundStoreFake) Health(context.Context) error { return nil }

func TestInboundWorkerValidatesAndStoresOriginalAndCanonical(t *testing.T) {
	var encoded bytes.Buffer
	input := image.NewRGBA(image.Rect(0, 0, 3, 2))
	input.Set(2, 1, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&encoded, input); err != nil {
		t.Fatal(err)
	}
	data := encoded.Bytes()
	digest := sha256.Sum256(data)
	descriptor := validInboundDescriptor()
	descriptor.SizeBytes, descriptor.FileSHA256, descriptor.Width, descriptor.Height = int64(len(data)), digest[:], 3, 2
	job, cipher := encryptedDownloadJob(t, descriptor, 1, 3)
	repository := &inboundWorkerRepositoryFake{jobs: []media_model.DownloadJob{job}}
	store := &inboundStoreFake{objects: map[string][]byte{}}
	worker := testInboundWorker(repository, cipher, &inboundDownloaderFake{data: data}, store)

	result, err := worker.RunOnce(context.Background())
	if err != nil || result.Completed != 1 || !repository.completed || repository.retried || repository.failed {
		t.Fatalf("result=%+v repo=%+v err=%v", result, repository, err)
	}
	if repository.original.Kind != media_model.VariantProviderOriginal || repository.canonical.Kind != media_model.VariantCanonical ||
		repository.original.SHA256 != sha256Bytes(data) || len(store.objects) != 2 {
		t.Fatalf("original=%+v canonical=%+v objects=%v", repository.original, repository.canonical, store.objects)
	}
}

func TestInboundWorkerRetriesTransientAndStopsAtMaxAttempts(t *testing.T) {
	descriptor := validInboundDescriptor()
	for _, test := range []struct {
		attempt       int
		wantRetry     bool
		wantPermanent bool
	}{
		{attempt: 1, wantRetry: true},
		{attempt: 3, wantPermanent: true},
	} {
		job, cipher := encryptedDownloadJob(t, descriptor, test.attempt, 3)
		repository := &inboundWorkerRepositoryFake{jobs: []media_model.DownloadJob{job}}
		worker := testInboundWorker(repository, cipher, &inboundDownloaderFake{err: errors.New("temporary provider failure")}, &inboundStoreFake{objects: map[string][]byte{}})
		result, err := worker.RunOnce(context.Background())
		if err != nil || repository.retried != test.wantRetry || repository.failed != test.wantPermanent ||
			result.Retried != boolInt(test.wantRetry) || result.Failed != boolInt(test.wantPermanent) {
			t.Fatalf("attempt=%d result=%+v repo=%+v err=%v", test.attempt, result, repository, err)
		}
	}
}

func encryptedDownloadJob(t *testing.T, descriptor InboundImageDescriptor, attempt, maxAttempts int) (media_model.DownloadJob, *DescriptorCipher) {
	t.Helper()
	cipher, err := NewDescriptorCipher(map[int][]byte{2: bytes.Repeat([]byte{4}, 32)}, 2)
	if err != nil {
		t.Fatal(err)
	}
	instanceID, assetID, jobID, messageID := uuid.NewString(), uuid.NewString(), uuid.NewString(), "message-1"
	encrypted, err := cipher.Encrypt(instanceID, messageID, assetID, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	claim := uuid.NewString()
	version := encrypted.KeyVersion
	return media_model.DownloadJob{
		ID: jobID, InstanceID: instanceID, MediaAssetID: assetID, MessageID: messageID,
		Status: media_model.DownloadJobProcessing, DescriptorCiphertext: encrypted.Ciphertext, DescriptorNonce: encrypted.Nonce,
		DescriptorKeyVersion: &version, AttemptCount: attempt, MaxAttempts: maxAttempts, ClaimToken: &claim,
	}, cipher
}

func testInboundWorker(repository media_repository.InboundRepository, cipher *DescriptorCipher, downloader InboundDownloader, store *inboundStoreFake) *InboundWorker {
	return NewInboundWorker(repository, cipher, downloader, store, InboundWorkerSettings{
		BatchSize: 4, Lease: time.Minute, PollInterval: time.Second, Timeout: 10 * time.Second,
		RetryBase: time.Second, MaxBytes: 1024 * 1024, MaxPixels: 100,
	}, nil)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
