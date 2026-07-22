package campaign_repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrCampaignConflict          = errors.New("campaign state changed concurrently")
	ErrInvalidCampaignTransition = errors.New("invalid campaign status transition")
)

type Actor struct {
	Type      string
	Reference string
}

type RecipientConsent struct {
	JID               string
	OptInSource       string
	EvidenceReference string
	OptedInAt         time.Time
}

type DraftInput struct {
	Name       string
	TextBody   string
	Recipients []RecipientConsent
	Actor      Actor
}

type CampaignRepository interface {
	CreateDraft(context.Context, string, DraftInput) (*campaign_model.Campaign, []campaign_model.Recipient, error)
	Get(context.Context, string, string) (*campaign_model.Campaign, []campaign_model.Recipient, error)
	Transition(context.Context, string, string, campaign_model.CampaignStatus, *time.Time, Actor) (*campaign_model.Campaign, error)
	ListAudit(context.Context, string, string) ([]campaign_model.AuditEvent, error)
}

type campaignRepository struct {
	db  *gorm.DB
	now func() time.Time
}

func NewCampaignRepository(db *gorm.DB) CampaignRepository {
	return &campaignRepository{db: db, now: time.Now}
}

func (r *campaignRepository) CreateDraft(ctx context.Context, instanceID string, input DraftInput) (*campaign_model.Campaign, []campaign_model.Recipient, error) {
	if r == nil || r.db == nil || r.now == nil || ctx == nil || uuid.Validate(instanceID) != nil {
		return nil, nil, errors.New("campaign repository and instance identity are required")
	}
	campaignID := uuid.NewString()
	name, actorHash, err := validateDraftInput(&input, campaignID)
	if err != nil {
		return nil, nil, err
	}
	now := r.now().UTC()
	campaign := &campaign_model.Campaign{
		ID: campaignID, InstanceID: instanceID, Name: name, Status: campaign_model.CampaignStatusDraft,
		ContentType: "text", TextBody: input.TextBody, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	recipients, err := buildRecipients(campaign, input.Recipients, now)
	if err != nil {
		return nil, nil, err
	}
	audit := newAuditEvent(campaign, nil, "created", input.Actor.Type, actorHash, "", string(campaign.Status), now)
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(campaign).Error; err != nil {
			return err
		}
		if err := tx.Create(&recipients).Error; err != nil {
			return err
		}
		return tx.Create(audit).Error
	})
	if err != nil {
		return nil, nil, err
	}
	return campaign, recipients, nil
}

func (r *campaignRepository) Get(ctx context.Context, instanceID, campaignID string) (*campaign_model.Campaign, []campaign_model.Recipient, error) {
	if r == nil || r.db == nil || ctx == nil || uuid.Validate(instanceID) != nil || uuid.Validate(campaignID) != nil {
		return nil, nil, errors.New("campaign repository and identities are required")
	}
	var campaign campaign_model.Campaign
	if err := r.db.WithContext(ctx).Where("instance_id = ? AND id = ?", instanceID, campaignID).First(&campaign).Error; err != nil {
		return nil, nil, err
	}
	var recipients []campaign_model.Recipient
	if err := r.db.WithContext(ctx).Where("instance_id = ? AND campaign_id = ?", instanceID, campaignID).
		Order("created_at ASC, id ASC").Find(&recipients).Error; err != nil {
		return nil, nil, err
	}
	return &campaign, recipients, nil
}

func (r *campaignRepository) Transition(ctx context.Context, instanceID, campaignID string, target campaign_model.CampaignStatus, startsAt *time.Time, actor Actor) (*campaign_model.Campaign, error) {
	if r == nil || r.db == nil || r.now == nil || ctx == nil || uuid.Validate(instanceID) != nil || uuid.Validate(campaignID) != nil {
		return nil, errors.New("campaign repository and identities are required")
	}
	actor.Type = strings.TrimSpace(actor.Type)
	actorHash, err := validateActor(actor, campaignID)
	if err != nil {
		return nil, err
	}
	now := r.now().UTC()
	var campaign campaign_model.Campaign
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id = ? AND id = ?", instanceID, campaignID).First(&campaign).Error; err != nil {
			return err
		}
		if !canTransitionCampaign(campaign.Status, target) {
			return ErrInvalidCampaignTransition
		}
		updates := map[string]any{"status": target, "version": gorm.Expr("version + 1"), "updated_at": now}
		if target == campaign_model.CampaignStatusScheduled {
			if startsAt == nil || startsAt.IsZero() {
				return errors.New("scheduled campaign requires a start time")
			}
			start := startsAt.UTC()
			updates["starts_at"] = start
			if err := tx.Model(&campaign_model.Recipient{}).
				Where("instance_id = ? AND campaign_id = ? AND status = ?", instanceID, campaignID, campaign_model.RecipientStatusPending).
				Update("next_attempt_at", start).Error; err != nil {
				return err
			}
		}
		if isTerminalCampaignStatus(target) {
			updates["finished_at"] = now
		}
		result := tx.Model(&campaign_model.Campaign{}).
			Where("instance_id = ? AND id = ? AND status = ? AND version = ?", instanceID, campaignID, campaign.Status, campaign.Version).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrCampaignConflict
		}
		from, to := string(campaign.Status), string(target)
		if err := tx.Create(newAuditEvent(&campaign, nil, "status_changed", actor.Type, actorHash, from, to, now)).Error; err != nil {
			return err
		}
		return tx.Where("instance_id = ? AND id = ?", instanceID, campaignID).First(&campaign).Error
	})
	return &campaign, err
}

func (r *campaignRepository) ListAudit(ctx context.Context, instanceID, campaignID string) ([]campaign_model.AuditEvent, error) {
	if r == nil || r.db == nil || ctx == nil || uuid.Validate(instanceID) != nil || uuid.Validate(campaignID) != nil {
		return nil, errors.New("campaign repository and identities are required")
	}
	var events []campaign_model.AuditEvent
	err := r.db.WithContext(ctx).Where("instance_id = ? AND campaign_id = ?", instanceID, campaignID).
		Order("occurred_at ASC, id ASC").Find(&events).Error
	return events, err
}

func buildRecipients(campaign *campaign_model.Campaign, inputs []RecipientConsent, now time.Time) ([]campaign_model.Recipient, error) {
	result := make([]campaign_model.Recipient, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		jid, err := canonicalDirectJID(input.JID)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[jid]; duplicate {
			return nil, errors.New("campaign contains a duplicate recipient")
		}
		seen[jid] = struct{}{}
		source := strings.TrimSpace(input.OptInSource)
		if source == "" || len(source) > 64 || strings.TrimSpace(input.EvidenceReference) == "" || len(input.EvidenceReference) > 4096 || input.OptedInAt.IsZero() || input.OptedInAt.After(now) {
			return nil, errors.New("each recipient requires valid opt-in evidence")
		}
		result = append(result, campaign_model.Recipient{
			ID: uuid.NewString(), CampaignID: campaign.ID, InstanceID: campaign.InstanceID, RecipientJID: jid,
			Status: campaign_model.RecipientStatusPending, OptInSource: source,
			OptInReferenceHash: hashReference(campaign.ID, input.EvidenceReference), OptedInAt: input.OptedInAt.UTC(),
			NextAttemptAt: now, AttemptCount: 0, CreatedAt: now, UpdatedAt: now,
		})
	}
	return result, nil
}

func canonicalDirectJID(value string) (string, error) {
	jid, err := types.ParseJID(strings.TrimSpace(value))
	if err != nil || jid.User == "" || jid.Server != types.DefaultUserServer {
		return "", errors.New("campaign recipient must be a direct WhatsApp JID")
	}
	canonical := jid.ToNonAD().String()
	if canonical == "" || len(canonical) > 255 {
		return "", errors.New("campaign recipient JID is invalid")
	}
	return canonical, nil
}

func validateActor(actor Actor, scope string) (*string, error) {
	if actor.Type != "admin" && actor.Type != "instance" && actor.Type != "system" {
		return nil, errors.New("campaign actor type is invalid")
	}
	if actor.Type == "system" && actor.Reference == "" {
		return nil, nil
	}
	if strings.TrimSpace(actor.Reference) == "" || len(actor.Reference) > 4096 {
		return nil, errors.New("non-system campaign actor reference is required")
	}
	hashed := hashReference(scope, actor.Reference)
	return &hashed, nil
}

func validateDraftInput(input *DraftInput, scope string) (string, *string, error) {
	if input == nil {
		return "", nil, errors.New("campaign draft is required")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > 255 || strings.TrimSpace(input.TextBody) == "" || len([]rune(input.TextBody)) > 4096 || len(input.Recipients) == 0 {
		return "", nil, errors.New("campaign name, bounded text, and recipients are required")
	}
	input.Actor.Type = strings.TrimSpace(input.Actor.Type)
	actorHash, err := validateActor(input.Actor, scope)
	return name, actorHash, err
}

func hashReference(scope, value string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + value))
	return hex.EncodeToString(sum[:])
}

func newAuditEvent(campaign *campaign_model.Campaign, recipientID *string, eventType, actorType string, actorHash *string, from, to string, occurredAt time.Time) *campaign_model.AuditEvent {
	var fromStatus, toStatus *string
	if from != "" {
		fromStatus = &from
	}
	if to != "" {
		toStatus = &to
	}
	return &campaign_model.AuditEvent{
		ID: uuid.NewString(), CampaignID: campaign.ID, InstanceID: campaign.InstanceID, RecipientID: recipientID,
		EventType: eventType, ActorType: actorType, ActorReferenceHash: actorHash,
		FromStatus: fromStatus, ToStatus: toStatus, Metadata: json.RawMessage(`{}`), OccurredAt: occurredAt,
	}
}

func canTransitionCampaign(from, to campaign_model.CampaignStatus) bool {
	allowed := map[campaign_model.CampaignStatus]map[campaign_model.CampaignStatus]bool{
		campaign_model.CampaignStatusDraft:     {campaign_model.CampaignStatusScheduled: true, campaign_model.CampaignStatusAborted: true},
		campaign_model.CampaignStatusScheduled: {campaign_model.CampaignStatusRunning: true, campaign_model.CampaignStatusPaused: true, campaign_model.CampaignStatusAborted: true, campaign_model.CampaignStatusFailed: true},
		campaign_model.CampaignStatusRunning:   {campaign_model.CampaignStatusPaused: true, campaign_model.CampaignStatusCompleted: true, campaign_model.CampaignStatusAborted: true, campaign_model.CampaignStatusFailed: true},
		campaign_model.CampaignStatusPaused:    {campaign_model.CampaignStatusRunning: true, campaign_model.CampaignStatusAborted: true, campaign_model.CampaignStatusFailed: true},
	}
	return allowed[from][to]
}

func isTerminalCampaignStatus(status campaign_model.CampaignStatus) bool {
	return status == campaign_model.CampaignStatusCompleted || status == campaign_model.CampaignStatusAborted || status == campaign_model.CampaignStatusFailed
}
