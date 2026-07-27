package media_service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	media_repository "github.com/evolution-foundation/evolution-go/pkg/media/repository"
	send_service "github.com/evolution-foundation/evolution-go/pkg/sendMessage/service"
	storage_interfaces "github.com/evolution-foundation/evolution-go/pkg/storage/interfaces"
	"github.com/google/uuid"
)

var (
	ErrMediaAssetNotReady  = errors.New("media asset is not ready")
	ErrMediaAssetIntegrity = errors.New("media asset integrity check failed")
)

type outboundImageSender interface {
	SendImageOnce(context.Context, *send_service.MediaStruct, []byte, *instance_model.Instance) (*send_service.MessageSendStruct, error)
}

type OutboundImageService struct {
	repository media_repository.Repository
	store      storage_interfaces.MediaAssetStore
	sender     outboundImageSender
	maxBytes   int64
	retention  time.Duration
	now        func() time.Time
}

func NewOutboundImageService(repository media_repository.Repository, store storage_interfaces.MediaAssetStore, sender outboundImageSender, maxBytes int64, retention time.Duration) *OutboundImageService {
	return &OutboundImageService{repository: repository, store: store, sender: sender, maxBytes: maxBytes, retention: retention, now: time.Now}
}

func (s *OutboundImageService) Send(ctx context.Context, data *send_service.MediaStruct, instance *instance_model.Instance) (*send_service.MessageSendStruct, error) {
	if s == nil || s.repository == nil || s.store == nil || s.sender == nil || s.now == nil || ctx == nil || data == nil || instance == nil ||
		uuid.Validate(instance.Id) != nil || uuid.Validate(data.MediaAssetID) != nil || strings.TrimSpace(data.Number) == "" ||
		data.Type != "image" || data.Url != "" || s.maxBytes < 1 || s.retention <= 0 || !utf8.ValidString(data.Caption) || utf8.RuneCountInString(data.Caption) > 1024 {
		return nil, ErrInvalidMediaAsset
	}
	asset, err := s.repository.Get(ctx, instance.Id, data.MediaAssetID)
	if err != nil {
		return nil, err
	}
	if asset.ID != data.MediaAssetID || asset.InstanceID != instance.Id || asset.Status != media_model.AssetStatusReady || asset.Canonical == nil || asset.CleanupClaimToken != nil || asset.DeletedAt != nil {
		return nil, ErrMediaAssetNotReady
	}
	canonical := asset.Canonical
	if canonical.MediaAssetID != asset.ID || canonical.InstanceID != instance.Id || canonical.Kind != media_model.VariantCanonical ||
		canonical.MIMEType != "image/jpeg" && canonical.MIMEType != "image/png" || canonical.SizeBytes < 1 || canonical.SizeBytes > s.maxBytes {
		return nil, ErrMediaAssetIntegrity
	}
	if strings.TrimSpace(data.Id) == "" {
		data.Id = strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))
	}
	retentionUntil := s.now().UTC().Add(s.retention)
	reference := media_model.AssetReference{
		InstanceID: instance.Id, MediaAssetID: asset.ID, OwnerType: media_model.ReferenceOwnerMessage,
		OwnerID: data.Id, RetentionUntil: &retentionUntil,
	}
	if err := s.repository.AddReference(ctx, reference); err != nil {
		return nil, err
	}
	release := func() {
		compensation, cancel := compensationContext(ctx)
		defer cancel()
		_ = s.repository.RemoveReference(compensation, instance.Id, asset.ID, media_model.ReferenceOwnerMessage, data.Id)
	}
	object, err := s.store.Open(ctx, canonical.ObjectKey)
	if err != nil {
		release()
		return nil, ErrMediaAssetStorage
	}
	bytes, readErr := io.ReadAll(io.LimitReader(object, s.maxBytes+1))
	closeErr := object.Close()
	if readErr != nil || closeErr != nil {
		release()
		return nil, ErrMediaAssetStorage
	}
	if int64(len(bytes)) != canonical.SizeBytes || int64(len(bytes)) > s.maxBytes || sha256Bytes(bytes) != canonical.SHA256 {
		release()
		return nil, ErrMediaAssetIntegrity
	}
	result, err := s.sender.SendImageOnce(ctx, data, bytes, instance)
	if err != nil {
		var providerSend *send_service.ProviderSendError
		if !errors.As(err, &providerSend) {
			release()
		}
		return nil, err
	}
	if result == nil || result.Info.ID == "" || string(result.Info.ID) != data.Id {
		return nil, &send_service.ProviderSendError{Cause: errors.New("provider acknowledgement is missing a message ID")}
	}
	return result, nil
}

func sha256Bytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
