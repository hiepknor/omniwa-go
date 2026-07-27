package campaign_service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	campaign_repository "github.com/evolution-foundation/evolution-go/pkg/campaign/repository"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	send_service "github.com/evolution-foundation/evolution-go/pkg/sendMessage/service"
	storage_interfaces "github.com/evolution-foundation/evolution-go/pkg/storage/interfaces"
)

type imageSendService interface {
	SendImageOnce(context.Context, *send_service.MediaStruct, []byte, *instance_model.Instance) (*send_service.MessageSendStruct, error)
}

type ImageSender struct {
	instances instanceReader
	media     campaign_repository.MediaAssetRepository
	store     storage_interfaces.CampaignMediaStore
	sends     imageSendService
	maxBytes  int64
}

func NewImageSender(instances instanceReader, media campaign_repository.MediaAssetRepository, store storage_interfaces.CampaignMediaStore, sends imageSendService, maxBytes int64) *ImageSender {
	return &ImageSender{instances: instances, media: media, store: store, sends: sends, maxBytes: maxBytes}
}

func (s *ImageSender) Send(ctx context.Context, campaign *campaign_model.Campaign, recipient *campaign_model.Recipient) (string, error) {
	if s == nil || s.instances == nil || s.media == nil || s.store == nil || s.sends == nil || s.maxBytes < 1 || ctx == nil ||
		campaign == nil || recipient == nil || campaign.ContentType != campaign_model.CampaignContentImage ||
		campaign.ID == "" || campaign.InstanceID == "" || campaign.InstanceID != recipient.InstanceID || campaign.ID != recipient.CampaignID ||
		campaign.MediaAssetID == nil || campaign.MediaMIMEType == nil || campaign.MediaSizeBytes == nil || campaign.MediaSHA256 == nil ||
		*campaign.MediaSizeBytes < 1 || *campaign.MediaSizeBytes > s.maxBytes || recipient.RecipientJID == "" {
		return "", errors.New("campaign image sender dependencies and normalized job are required")
	}
	if recipient.TargetType != campaign_model.RecipientTargetGroup {
		return "", &DeliveryError{Kind: DeliveryFailureTerminal, Code: "image_target_not_supported"}
	}
	if err := validateGroupDeliveryRecipient(recipient.RecipientJID); err != nil {
		return "", &DeliveryError{Kind: DeliveryFailureTerminal, Code: "invalid_group_jid", Cause: err}
	}
	asset, err := s.media.Get(ctx, campaign.InstanceID, *campaign.MediaAssetID)
	if err != nil {
		if errors.Is(err, campaign_repository.ErrMediaAssetNotFound) {
			return "", &DeliveryError{Kind: DeliveryFailureTerminal, Code: "campaign_media_missing", Cause: err}
		}
		return "", &DeliveryError{Kind: DeliveryFailureTransient, Code: "campaign_media_read_failed", Cause: err}
	}
	if !campaignMediaSnapshotMatches(campaign, asset) {
		return "", &DeliveryError{Kind: DeliveryFailureTerminal, Code: "campaign_media_integrity_failed"}
	}
	object, err := s.store.Open(ctx, asset.ObjectKey)
	if err != nil {
		return "", &DeliveryError{Kind: DeliveryFailureTransient, Code: "campaign_media_read_failed", Cause: err}
	}
	defer object.Close()
	bytes, err := io.ReadAll(io.LimitReader(object, s.maxBytes+1))
	if err != nil {
		return "", &DeliveryError{Kind: DeliveryFailureTransient, Code: "campaign_media_read_failed", Cause: err}
	}
	if int64(len(bytes)) != *campaign.MediaSizeBytes || int64(len(bytes)) > s.maxBytes || sha256Hex(bytes) != *campaign.MediaSHA256 {
		return "", &DeliveryError{Kind: DeliveryFailureTerminal, Code: "campaign_media_integrity_failed"}
	}
	instance, err := s.instances.GetInstanceByID(recipient.InstanceID)
	if err != nil {
		return "", &dependencyUnavailableError{cause: err}
	}
	result, err := s.sends.SendImageOnce(ctx, &send_service.MediaStruct{
		Number: recipient.RecipientJID, Type: "image", Caption: campaign.TextBody, Id: deterministicMessageID(recipient.ID),
	}, bytes, instance)
	if err != nil {
		var upload *send_service.ProviderMediaUploadError
		if errors.As(err, &upload) {
			classified := classifyGroupDeliveryError(upload.Cause)
			var delivery *DeliveryError
			if errors.As(classified, &delivery) && delivery.Kind == DeliveryFailureRateLimit {
				return "", classified
			}
			return "", &DeliveryError{Kind: DeliveryFailureTransient, Code: "provider_media_upload_failed", Cause: err}
		}
		return "", classifyGroupDeliveryError(err)
	}
	if result == nil || result.Info.ID == "" {
		return "", &DeliveryError{Kind: DeliveryFailureUnknown, Code: "unknown_send_outcome"}
	}
	return string(result.Info.ID), nil
}

func campaignMediaSnapshotMatches(campaign *campaign_model.Campaign, asset *campaign_model.MediaAsset) bool {
	return asset != nil && asset.Status == campaign_model.MediaAssetStatusReady && asset.DeletedAt == nil && asset.CleanupClaimToken == nil && asset.CleanupLeaseUntil == nil &&
		asset.ID == *campaign.MediaAssetID && asset.InstanceID == campaign.InstanceID && asset.MIMEType != nil && *asset.MIMEType == *campaign.MediaMIMEType &&
		asset.SizeBytes != nil && *asset.SizeBytes == *campaign.MediaSizeBytes && asset.SHA256 != nil && strings.EqualFold(*asset.SHA256, *campaign.MediaSHA256)
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

type ContentSender struct {
	text  Sender
	image Sender
}

func NewContentSender(text, image Sender) *ContentSender {
	return &ContentSender{text: text, image: image}
}

func (s *ContentSender) Send(ctx context.Context, campaign *campaign_model.Campaign, recipient *campaign_model.Recipient) (string, error) {
	if s == nil || campaign == nil {
		return "", errors.New("campaign content sender and campaign are required")
	}
	switch campaign.ContentType {
	case campaign_model.CampaignContentText:
		if s.text == nil {
			return "", errors.New("campaign text sender is unavailable")
		}
		return s.text.Send(ctx, campaign, recipient)
	case campaign_model.CampaignContentImage:
		if s.image == nil {
			return "", &DeliveryError{Kind: DeliveryFailureTerminal, Code: "campaign_image_content_disabled"}
		}
		return s.image.Send(ctx, campaign, recipient)
	default:
		return "", &DeliveryError{Kind: DeliveryFailureTerminal, Code: "unsupported_campaign_content"}
	}
}
