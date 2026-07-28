package campaign_repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	group_list_model "github.com/evolution-foundation/evolution-go/pkg/groupList/model"
	group_list_repository "github.com/evolution-foundation/evolution-go/pkg/groupList/repository"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *campaignRepository) CreateGroupDraft(ctx context.Context, instanceID string, input GroupDraftInput) (*campaign_model.Campaign, []campaign_model.Recipient, error) {
	if r == nil || r.db == nil || r.now == nil || r.groupEligibility == nil || ctx == nil || uuid.Validate(instanceID) != nil || uuid.Validate(input.GroupListID) != nil {
		return nil, nil, errors.New("campaign repository, group eligibility, and identities are required")
	}
	if input.GroupListVersion < 1 || strings.TrimSpace(input.InstanceJID) == "" {
		return nil, nil, fmt.Errorf("%w: group list version and instance JID are required", ErrInvalidCampaignInput)
	}
	campaignID := uuid.NewString()
	input.Actor.Type = strings.TrimSpace(input.Actor.Type)
	name, actorHash, err := validateCampaignIdentity(input.Name, input.Actor, campaignID)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalidCampaignInput, err)
	}
	if err := validateCampaignContentInput(input.ContentType, input.TextBody, input.MediaAssetID); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalidCampaignInput, err)
	}
	now := r.now().UTC()
	var campaign campaign_model.Campaign
	var recipients []campaign_model.Recipient
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var list group_list_model.GroupList
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("instance_id = ? AND id = ? AND deleted_at IS NULL", instanceID, input.GroupListID).
			First(&list).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return group_list_repository.ErrNotFound
			}
			return err
		}
		if list.Version != input.GroupListVersion {
			return group_list_repository.ErrVersionConflict
		}
		var entries []group_list_model.Entry
		if err := tx.Where("instance_id = ? AND group_list_id = ?", instanceID, list.ID).
			Order("group_jid ASC").Find(&entries).Error; err != nil {
			return err
		}
		if len(entries) == 0 {
			return ErrGroupListEmpty
		}
		if len(entries) > maxCampaignRecipients {
			return fmt.Errorf("%w: group list exceeds campaign target limit", ErrInvalidCampaignInput)
		}
		groupJIDs := make([]string, len(entries))
		for index := range entries {
			groupJIDs[index] = entries[index].GroupJID
		}
		targets, err := r.groupEligibility(ctx, tx, instanceID, input.InstanceJID, groupJIDs)
		if err != nil {
			return err
		}
		if len(targets) != len(entries) {
			return errors.New("group eligibility result does not match group list snapshot")
		}
		eligibilityResults := make([]group_list_repository.EligibilityMutationResult, len(targets))
		for index, target := range targets {
			var reason *string
			if target.Reason != "" {
				value := target.Reason
				reason = &value
			}
			eligibilityResults[index] = group_list_repository.EligibilityMutationResult{
				GroupJID: target.GroupJID, CurrentName: target.TargetLabel, Eligibility: target.Eligibility,
				EligibilityReason: reason, CanSend: target.Eligibility == "eligible", CheckedAt: target.CheckedAt,
			}
		}
		if _, err := group_list_repository.MutationEntries(eligibilityResults, ErrGroupUnavailable, ErrGroupProjectionNotReady); err != nil {
			return err
		}
		campaign = campaign_model.Campaign{
			ID: campaignID, InstanceID: instanceID, Name: name, Status: campaign_model.CampaignStatusDraft,
			TargetType:  campaign_model.CampaignTargetGroupList,
			GroupListID: &list.ID, GroupListNameSnapshot: &list.Name, GroupListVersion: &list.Version,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		if err := applyCampaignContent(tx, instanceID, &campaign, input.ContentType, input.TextBody, input.MediaAssetID); err != nil {
			return err
		}
		recipients = make([]campaign_model.Recipient, len(targets))
		seen := make(map[string]struct{}, len(targets))
		for index, target := range targets {
			jid, err := canonicalGroupTargetJID(target.GroupJID)
			if err != nil || jid != groupJIDs[index] {
				return errors.New("group eligibility result identity does not match group list snapshot")
			}
			if _, duplicate := seen[jid]; duplicate {
				return errors.New("group eligibility result contains a duplicate target")
			}
			seen[jid] = struct{}{}
			label := strings.TrimSpace(target.TargetLabel)
			if label == "" {
				label = jid
			}
			if len([]rune(label)) > 255 {
				return errors.New("group target label exceeds 255 characters")
			}
			recipients[index] = campaign_model.Recipient{
				ID: uuid.NewString(), CampaignID: campaign.ID, InstanceID: instanceID, RecipientJID: jid,
				TargetType: campaign_model.RecipientTargetGroup, TargetLabel: &label, Status: campaign_model.RecipientStatusPending,
				OptInSource: list.AuthorizationSource, OptInReferenceHash: list.AuthorizationReferenceHash, OptedInAt: list.AuthorizedAt.UTC(),
				NextAttemptAt: now, CreatedAt: now, UpdatedAt: now,
			}
		}
		if err := tx.Create(&campaign).Error; err != nil {
			return err
		}
		if err := addSharedCampaignMediaReference(tx, &campaign); err != nil {
			return err
		}
		if err := tx.CreateInBatches(&recipients, 500).Error; err != nil {
			return err
		}
		guards := make([]groupDeliveryGuard, len(recipients))
		for index := range recipients {
			guards[index] = groupDeliveryGuard{InstanceID: instanceID, GroupJID: recipients[index].RecipientJID, UpdatedAt: now}
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(&guards, 500).Error; err != nil {
			return err
		}
		metadata, err := json.Marshal(map[string]any{
			"targetType": "group_list", "groupListId": list.ID, "groupListVersion": list.Version, "targetCount": len(recipients),
			"contentType": campaign.ContentType, "mediaAssetId": campaign.MediaAssetID,
		})
		if err != nil {
			return err
		}
		audit := newAuditEvent(&campaign, nil, "created", input.Actor.Type, actorHash, "", string(campaign.Status), now)
		audit.Metadata = metadata
		return tx.Create(audit).Error
	})
	if err != nil {
		return nil, nil, err
	}
	return &campaign, recipients, nil
}

func canonicalGroupTargetJID(value string) (string, error) {
	jid, err := types.ParseJID(strings.TrimSpace(value))
	if err != nil || jid.User == "" || jid.Server != types.GroupServer {
		return "", errors.New("campaign group target must be a WhatsApp group JID")
	}
	canonical := jid.ToNonAD().String()
	if canonical == "" || len(canonical) > 255 {
		return "", errors.New("campaign group target JID is invalid")
	}
	return canonical, nil
}

func validateCampaignIdentity(name string, actor Actor, scope string) (string, *string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 255 {
		return "", nil, errors.New("bounded campaign name is required")
	}
	actor.Type = strings.TrimSpace(actor.Type)
	actorHash, err := validateActor(actor, scope)
	return name, actorHash, err
}
