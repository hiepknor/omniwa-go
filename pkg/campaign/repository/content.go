package campaign_repository

import (
	"errors"
	"strings"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func applyCampaignContent(tx *gorm.DB, instanceID string, campaign *campaign_model.Campaign, contentType campaign_model.CampaignContentType, textBody, mediaAssetID string) error {
	if tx == nil || campaign == nil || uuid.Validate(instanceID) != nil {
		return errors.New("campaign content transaction and identity are required")
	}
	if contentType == "" {
		contentType = campaign_model.CampaignContentText
	}
	if err := validateCampaignContentInput(contentType, textBody, mediaAssetID); err != nil {
		return err
	}
	switch contentType {
	case campaign_model.CampaignContentText:
		campaign.ContentType = campaign_model.CampaignContentText
		campaign.TextBody = textBody
		return nil
	case campaign_model.CampaignContentImage:
		var asset campaign_model.MediaAsset
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("instance_id = ? AND id = ? AND deleted_at IS NULL", instanceID, mediaAssetID).
			First(&asset).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrMediaAssetNotFound
		}
		if err != nil {
			return err
		}
		if asset.Status != campaign_model.MediaAssetStatusReady || asset.CleanupClaimToken != nil || asset.CleanupLeaseUntil != nil ||
			asset.MIMEType == nil || asset.SizeBytes == nil || asset.Width == nil ||
			asset.Height == nil || asset.SHA256 == nil || asset.ReadyAt == nil {
			return ErrMediaAssetConflict
		}
		campaign.ContentType = campaign_model.CampaignContentImage
		campaign.TextBody = textBody
		campaign.MediaAssetID = &asset.ID
		campaign.MediaMIMEType = asset.MIMEType
		campaign.MediaSizeBytes = asset.SizeBytes
		campaign.MediaWidth = asset.Width
		campaign.MediaHeight = asset.Height
		campaign.MediaSHA256 = asset.SHA256
		return nil
	default:
		return errors.New("campaign content type is invalid")
	}
}

func addSharedCampaignMediaReference(tx *gorm.DB, campaign *campaign_model.Campaign) error {
	if tx == nil || campaign == nil || campaign.MediaAssetID == nil {
		return nil
	}
	var sharedCount int64
	if err := tx.Model(&media_model.Asset{}).
		Where("instance_id = ? AND id = ? AND status = ? AND deleted_at IS NULL", campaign.InstanceID, *campaign.MediaAssetID, media_model.AssetStatusReady).
		Count(&sharedCount).Error; err != nil {
		return err
	}
	if sharedCount == 0 {
		// The legacy campaign path remains valid during the rollback window.
		return nil
	}
	reference := media_model.AssetReference{
		InstanceID: campaign.InstanceID, MediaAssetID: *campaign.MediaAssetID,
		OwnerType: media_model.ReferenceOwnerCampaign, OwnerID: campaign.ID,
		CreatedAt: campaign.CreatedAt,
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&reference).Error
}

func validateCampaignContentInput(contentType campaign_model.CampaignContentType, textBody, mediaAssetID string) error {
	if contentType == "" {
		contentType = campaign_model.CampaignContentText
	}
	switch contentType {
	case campaign_model.CampaignContentText:
		if strings.TrimSpace(textBody) == "" || len([]rune(textBody)) > 4096 || strings.TrimSpace(mediaAssetID) != "" {
			return errors.New("bounded text campaign content is required")
		}
	case campaign_model.CampaignContentImage:
		if uuid.Validate(mediaAssetID) != nil || len([]rune(textBody)) > 1024 {
			return errors.New("ready image media and bounded caption are required")
		}
	default:
		return errors.New("campaign content type is invalid")
	}
	return nil
}
