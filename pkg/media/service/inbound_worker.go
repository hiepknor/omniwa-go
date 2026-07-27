package media_service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"strings"
	"time"

	instance_runtime "github.com/evolution-foundation/evolution-go/pkg/instance/runtime"
	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	media_repository "github.com/evolution-foundation/evolution-go/pkg/media/repository"
	storage_interfaces "github.com/evolution-foundation/evolution-go/pkg/storage/interfaces"
	"go.mau.fi/whatsmeow"
)

var errInboundDownloadTooLarge = errors.New("inbound media download exceeded its bound")

type InboundDownloader interface {
	Download(context.Context, string, InboundImageDescriptor, whatsmeow.File) error
}

type RuntimeInboundDownloader struct {
	clients instance_runtime.ClientProvider
}

func NewRuntimeInboundDownloader(clients instance_runtime.ClientProvider) *RuntimeInboundDownloader {
	return &RuntimeInboundDownloader{clients: clients}
}

func (d *RuntimeInboundDownloader) Download(ctx context.Context, instanceID string, descriptor InboundImageDescriptor, file whatsmeow.File) error {
	if d == nil || d.clients == nil || ctx == nil || file == nil {
		return errors.New("inbound media downloader is unavailable")
	}
	client := d.clients.Get(instanceID)
	if client == nil || !client.IsConnected() {
		return errors.New("instance runtime is unavailable")
	}
	return client.DownloadMediaWithPathToFile(ctx, descriptor.DirectPath, descriptor.FileEncSHA256, descriptor.FileSHA256,
		descriptor.MediaKey, whatsmeow.MediaImage, "", false, file)
}

type InboundWorkerSettings struct {
	BatchSize    int
	Lease        time.Duration
	PollInterval time.Duration
	Timeout      time.Duration
	RetryBase    time.Duration
	MaxBytes     int64
	MaxPixels    int64
}

type InboundWorkerResult struct {
	Claimed, Completed, Retried, Failed int
}

type InboundWorkerResultHandler func(InboundWorkerResult, error)

type InboundWorker struct {
	repository media_repository.InboundRepository
	cipher     *DescriptorCipher
	downloader InboundDownloader
	store      storage_interfaces.MediaAssetStore
	settings   InboundWorkerSettings
	now        func() time.Time
	onResult   InboundWorkerResultHandler
}

func NewInboundWorker(repository media_repository.InboundRepository, cipher *DescriptorCipher, downloader InboundDownloader, store storage_interfaces.MediaAssetStore, settings InboundWorkerSettings, onResult InboundWorkerResultHandler) *InboundWorker {
	return &InboundWorker{repository: repository, cipher: cipher, downloader: downloader, store: store, settings: settings, now: time.Now, onResult: onResult}
}

func (w *InboundWorker) Run(ctx context.Context) error {
	if err := w.validate(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(w.settings.PollInterval)
	defer ticker.Stop()
	for {
		result, err := w.RunOnce(ctx)
		if w.onResult != nil {
			w.onResult(result, err)
		}
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (w *InboundWorker) RunOnce(ctx context.Context) (InboundWorkerResult, error) {
	var result InboundWorkerResult
	if err := w.validate(ctx); err != nil {
		return result, err
	}
	var failures []error
	for result.Claimed < w.settings.BatchSize {
		jobs, err := w.repository.ClaimDownloads(ctx, 1, w.settings.Lease)
		if err != nil {
			failures = append(failures, err)
			break
		}
		if len(jobs) == 0 {
			break
		}
		result.Claimed++
		outcome, processErr := w.process(ctx, &jobs[0])
		switch outcome {
		case media_model.DownloadJobCompleted:
			result.Completed++
		case media_model.DownloadJobRetryWait:
			result.Retried++
		case media_model.DownloadJobFailed:
			result.Failed++
		}
		if processErr != nil {
			failures = append(failures, processErr)
		}
	}
	return result, errors.Join(failures...)
}

func (w *InboundWorker) process(parent context.Context, job *media_model.DownloadJob) (media_model.DownloadJobStatus, error) {
	keyVersion := 0
	if job.DescriptorKeyVersion != nil {
		keyVersion = *job.DescriptorKeyVersion
	}
	descriptor, err := w.cipher.Decrypt(job.InstanceID, job.MessageID, job.MediaAssetID, EncryptedDescriptor{
		Ciphertext: job.DescriptorCiphertext, Nonce: job.DescriptorNonce, KeyVersion: keyVersion,
	})
	if err != nil {
		return w.finishFailure(parent, job, "descriptor_decrypt_failed", true, nil)
	}
	if err := validateInboundDescriptor(*descriptor, w.settings.MaxBytes, w.settings.MaxPixels); err != nil {
		return w.finishFailure(parent, job, "invalid_media_descriptor", true, nil)
	}
	ctx, cancel := context.WithTimeout(parent, w.settings.Timeout)
	defer cancel()
	download, err := newBoundedDownloadFile(w.settings.MaxBytes + 64*1024)
	if err != nil {
		return w.finishFailure(parent, job, "media_tempfile_failed", false, err)
	}
	defer download.Close()
	if err := w.downloader.Download(ctx, job.InstanceID, *descriptor, download); err != nil {
		permanent, code := classifyInboundDownloadError(err)
		return w.finishFailure(parent, job, code, permanent, err)
	}
	if err := download.verify(descriptor.SizeBytes, descriptor.FileSHA256); err != nil {
		return w.finishFailure(parent, job, "provider_media_integrity_failed", true, err)
	}
	configuration, format, err := image.DecodeConfig(download)
	if err != nil || format != "jpeg" && format != "png" || configuration.Width < 1 || configuration.Height < 1 ||
		configuration.Width > 32768 || configuration.Height > 32768 || int64(configuration.Width) > w.settings.MaxPixels/int64(configuration.Height) {
		return w.finishFailure(parent, job, "unsupported_inbound_media", true, err)
	}
	if _, err := download.Seek(0, io.SeekStart); err != nil {
		return w.finishFailure(parent, job, "media_tempfile_failed", false, err)
	}
	normalized, err := normalizeAssetImage(download, w.settings.MaxBytes, w.settings.MaxPixels)
	if err != nil {
		return w.finishFailure(parent, job, inboundNormalizationCode(err), true, err)
	}
	defer normalized.Close()
	originalMIME := "image/" + format
	originalKey := inboundAssetKey(job.InstanceID, job.MediaAssetID, "provider_original")
	canonicalKey := inboundAssetKey(job.InstanceID, job.MediaAssetID, "canonical")
	if _, err := download.Seek(0, io.SeekStart); err != nil {
		return w.finishFailure(parent, job, "media_tempfile_failed", false, err)
	}
	if err := w.store.Put(ctx, originalKey, download, download.size(), originalMIME); err != nil {
		return w.finishFailureWithObjects(parent, job, "media_asset_storage_unavailable", false, err, originalKey)
	}
	if err := w.store.Put(ctx, canonicalKey, normalized.file, normalized.size, normalized.mimeType); err != nil {
		return w.finishFailureWithObjects(parent, job, "media_asset_storage_unavailable", false, err, originalKey, canonicalKey)
	}
	original := media_model.AssetVariant{
		MediaAssetID: job.MediaAssetID, InstanceID: job.InstanceID, Kind: media_model.VariantProviderOriginal,
		ObjectKey: originalKey, MIMEType: originalMIME, SizeBytes: download.size(), Width: configuration.Width, Height: configuration.Height,
		SHA256: hex.EncodeToString(descriptor.FileSHA256),
	}
	canonical := media_model.AssetVariant{
		MediaAssetID: job.MediaAssetID, InstanceID: job.InstanceID, Kind: media_model.VariantCanonical,
		ObjectKey: canonicalKey, MIMEType: normalized.mimeType, SizeBytes: normalized.size, Width: normalized.width, Height: normalized.height,
		SHA256: normalized.sha256,
	}
	if err := w.repository.CompleteDownload(parent, job, original, canonical); err != nil {
		return media_model.DownloadJobProcessing, err
	}
	return media_model.DownloadJobCompleted, nil
}

func (w *InboundWorker) finishFailureWithObjects(ctx context.Context, job *media_model.DownloadJob, code string, permanent bool, cause error, keys ...string) (media_model.DownloadJobStatus, error) {
	status, err := w.finishFailure(ctx, job, code, permanent, cause)
	if status == media_model.DownloadJobFailed {
		compensation, cancel := compensationContext(ctx)
		defer cancel()
		for _, key := range keys {
			_ = w.store.Delete(compensation, key)
		}
	}
	return status, err
}

func (w *InboundWorker) finishFailure(ctx context.Context, job *media_model.DownloadJob, code string, permanent bool, cause error) (media_model.DownloadJobStatus, error) {
	if permanent || job.AttemptCount >= job.MaxAttempts {
		if err := w.repository.FailDownload(ctx, job, code); err != nil {
			return media_model.DownloadJobProcessing, errors.Join(cause, err)
		}
		return media_model.DownloadJobFailed, nil
	}
	next := w.now().UTC().Add(inboundRetryDelay(w.settings.RetryBase, job))
	if err := w.repository.RetryDownload(ctx, job, code, next); err != nil {
		return media_model.DownloadJobProcessing, errors.Join(cause, err)
	}
	return media_model.DownloadJobRetryWait, nil
}

func (w *InboundWorker) validate(ctx context.Context) error {
	if w == nil {
		return errors.New("bounded inbound media worker dependencies are required")
	}
	settings := w.settings
	if w.repository == nil || w.cipher == nil || w.downloader == nil || w.store == nil || w.now == nil || ctx == nil ||
		settings.BatchSize < 1 || settings.BatchSize > 100 || settings.Lease <= settings.Timeout+30*time.Second || settings.PollInterval <= 0 || settings.Timeout <= 0 ||
		settings.RetryBase <= 0 || settings.MaxBytes < 1 || settings.MaxBytes > 64*1024*1024 || settings.MaxPixels < 1 || settings.MaxPixels > 100_000_000 {
		return errors.New("bounded inbound media worker dependencies are required")
	}
	return nil
}

func classifyInboundDownloadError(err error) (bool, string) {
	switch {
	case errors.Is(err, errInboundDownloadTooLarge):
		return true, "inbound_media_too_large"
	case errors.Is(err, whatsmeow.ErrInvalidMediaSHA256), errors.Is(err, whatsmeow.ErrInvalidUnencryptedMediaSHA256):
		return true, "provider_media_integrity_failed"
	case errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith403), errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404), errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410):
		return true, "provider_media_expired"
	case errors.Is(err, context.Canceled):
		return false, "media_download_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return false, "media_download_timeout"
	default:
		return false, "provider_media_download_failed"
	}
}

func inboundNormalizationCode(err error) string {
	switch {
	case errors.Is(err, ErrMediaAssetTooLarge):
		return "inbound_media_too_large"
	case errors.Is(err, ErrInvalidMediaDimensions):
		return "invalid_media_dimensions"
	default:
		return "unsupported_inbound_media"
	}
}

func inboundRetryDelay(base time.Duration, job *media_model.DownloadJob) time.Duration {
	attempt := job.AttemptCount
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 8 {
		attempt = 8
	}
	delay := base << (attempt - 1)
	if delay > 30*time.Minute {
		delay = 30 * time.Minute
	}
	// Stable 75-125% jitter from the job identity and attempt.
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", job.ID, attempt)))
	permille := 750 + int(sum[0])%501
	return time.Duration(int64(delay) * int64(permille) / 1000)
}

func inboundAssetKey(instanceID, assetID, variant string) string {
	return "media-assets/" + instanceID + "/" + assetID + "/" + variant
}

type boundedDownloadFile struct {
	file  *os.File
	path  string
	limit int64
}

func newBoundedDownloadFile(limit int64) (*boundedDownloadFile, error) {
	file, err := os.CreateTemp("", "omniwa-inbound-media-*")
	if err != nil {
		return nil, err
	}
	return &boundedDownloadFile{file: file, path: file.Name(), limit: limit}, nil
}

func (f *boundedDownloadFile) Read(p []byte) (int, error)              { return f.file.Read(p) }
func (f *boundedDownloadFile) ReadAt(p []byte, off int64) (int, error) { return f.file.ReadAt(p, off) }
func (f *boundedDownloadFile) Seek(off int64, whence int) (int64, error) {
	return f.file.Seek(off, whence)
}
func (f *boundedDownloadFile) Stat() (os.FileInfo, error) { return f.file.Stat() }
func (f *boundedDownloadFile) Write(p []byte) (int, error) {
	position, err := f.file.Seek(0, io.SeekCurrent)
	if err != nil || position+int64(len(p)) > f.limit {
		return 0, errInboundDownloadTooLarge
	}
	return f.file.Write(p)
}
func (f *boundedDownloadFile) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > f.limit {
		return 0, errInboundDownloadTooLarge
	}
	return f.file.WriteAt(p, off)
}
func (f *boundedDownloadFile) Truncate(size int64) error {
	if size < 0 || size > f.limit {
		return errInboundDownloadTooLarge
	}
	return f.file.Truncate(size)
}
func (f *boundedDownloadFile) size() int64 {
	info, err := f.file.Stat()
	if err != nil {
		return 0
	}
	return info.Size()
}
func (f *boundedDownloadFile) verify(expectedSize int64, expectedSHA []byte) error {
	if f.size() != expectedSize || f.size() < 1 || f.size() > f.limit || len(expectedSHA) != sha256.Size {
		return ErrMediaAssetIntegrity
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), hex.EncodeToString(expectedSHA)) {
		return ErrMediaAssetIntegrity
	}
	_, err := f.Seek(0, io.SeekStart)
	return err
}
func (f *boundedDownloadFile) Close() error {
	if f == nil {
		return nil
	}
	return errors.Join(f.file.Close(), os.Remove(f.path))
}
