package media_service

import (
	"context"
	"errors"
	"time"

	media_repository "github.com/evolution-foundation/evolution-go/pkg/media/repository"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types/events"
)

type InboundCaptureSettings struct {
	MaxBytes    int64
	MaxPixels   int64
	MaxAttempts int
	Retention   time.Duration
}

type InboundCaptureService struct {
	repository media_repository.InboundRepository
	cipher     *DescriptorCipher
	settings   InboundCaptureSettings
	now        func() time.Time
}

func NewInboundCaptureService(repository media_repository.InboundRepository, cipher *DescriptorCipher, settings InboundCaptureSettings) *InboundCaptureService {
	return &InboundCaptureService{repository: repository, cipher: cipher, settings: settings, now: time.Now}
}

// Capture persists only an encrypted provider descriptor. Network download and
// image decoding are intentionally deferred to the leased worker.
func (s *InboundCaptureService) Capture(ctx context.Context, instanceID string, raw any) (string, bool, error) {
	event, ok := raw.(*events.Message)
	if !ok || event == nil || event.Message == nil || event.Info.IsFromMe || event.Message.GetImageMessage() == nil {
		return "", false, nil
	}
	if err := s.validate(ctx); err != nil || uuid.Validate(instanceID) != nil || event.Info.ID == "" || len(event.Info.ID) > 255 || event.Info.Timestamp.IsZero() {
		return "", true, ErrInvalidMediaAsset
	}
	image := event.Message.GetImageMessage()
	descriptor := InboundImageDescriptor{
		DirectPath: image.GetDirectPath(), FileEncSHA256: append([]byte(nil), image.GetFileEncSHA256()...),
		FileSHA256: append([]byte(nil), image.GetFileSHA256()...), MediaKey: append([]byte(nil), image.GetMediaKey()...),
		MIMEType: image.GetMimetype(), SizeBytes: int64(image.GetFileLength()), Width: int(image.GetWidth()), Height: int(image.GetHeight()),
	}
	if err := validateInboundDescriptor(descriptor, s.settings.MaxBytes, s.settings.MaxPixels); err != nil {
		return "", true, err
	}
	expiresAt := event.Info.Timestamp.UTC().Add(s.settings.Retention)
	if !expiresAt.After(s.now()) {
		return "", true, ErrInvalidMediaAsset
	}
	assetID := uuid.NewString()
	encrypted, err := s.cipher.Encrypt(instanceID, string(event.Info.ID), assetID, descriptor)
	if err != nil {
		return "", true, err
	}
	asset, _, err := s.repository.Capture(ctx, media_repository.CaptureInboundInput{
		AssetID: assetID, JobID: uuid.NewString(), InstanceID: instanceID, MessageID: string(event.Info.ID),
		DescriptorCiphertext: encrypted.Ciphertext, DescriptorNonce: encrypted.Nonce,
		DescriptorKeyVersion: encrypted.KeyVersion, MaxAttempts: s.settings.MaxAttempts, ExpiresAt: expiresAt,
	})
	if err != nil {
		return "", true, err
	}
	if asset == nil || uuid.Validate(asset.ID) != nil || asset.InstanceID != instanceID {
		return "", true, errors.New("captured inbound media asset identity is invalid")
	}
	return asset.ID, true, nil
}

func (s *InboundCaptureService) validate(ctx context.Context) error {
	if s == nil || s.repository == nil || s.cipher == nil || s.now == nil || ctx == nil || s.settings.MaxBytes < 1 ||
		s.settings.MaxBytes > 64*1024*1024 || s.settings.MaxPixels < 1 || s.settings.MaxPixels > 100_000_000 ||
		s.settings.MaxAttempts < 1 || s.settings.MaxAttempts > 20 || s.settings.Retention <= 0 {
		return errors.New("bounded inbound media capture dependencies are required")
	}
	return nil
}
