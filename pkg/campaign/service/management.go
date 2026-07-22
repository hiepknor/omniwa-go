package campaign_service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	campaign_repository "github.com/evolution-foundation/evolution-go/pkg/campaign/repository"
	"github.com/google/uuid"
)

var ErrInvalidCampaignCursor = errors.New("invalid campaign cursor")

const campaignCursorVersion = 1

type ManagementService struct {
	repository campaign_repository.CampaignRepository
}

type CreateCampaignInput struct {
	Name       string
	TextBody   string
	Recipients []campaign_repository.RecipientConsent
	Actor      campaign_repository.Actor
}

type CampaignDetail struct {
	Campaign       *campaign_model.Campaign                 `json:"campaign"`
	RecipientCount int64                                    `json:"recipientCount"`
	ByStatus       map[campaign_model.RecipientStatus]int64 `json:"byStatus"`
}

type CampaignList struct {
	Items      []campaign_model.Campaign `json:"items"`
	NextCursor string                    `json:"nextCursor,omitempty"`
}

type RecipientList struct {
	Items      []campaign_model.Recipient `json:"items"`
	NextCursor string                     `json:"nextCursor,omitempty"`
}

type AuditList struct {
	Items      []campaign_model.AuditEvent `json:"items"`
	NextCursor string                      `json:"nextCursor,omitempty"`
}

type cursorEnvelope struct {
	Version int       `json:"v"`
	Kind    string    `json:"kind"`
	At      time.Time `json:"at"`
	ID      string    `json:"id"`
	Scope   string    `json:"scope"`
}

func NewManagementService(repository campaign_repository.CampaignRepository) *ManagementService {
	return &ManagementService{repository: repository}
}

func (s *ManagementService) Create(ctx context.Context, instanceID string, input CreateCampaignInput) (*CampaignDetail, error) {
	if s == nil || s.repository == nil || ctx == nil {
		return nil, errors.New("campaign management service is unavailable")
	}
	campaign, recipients, err := s.repository.CreateDraft(ctx, instanceID, campaign_repository.DraftInput{
		Name: input.Name, TextBody: input.TextBody, Recipients: input.Recipients, Actor: input.Actor,
	})
	if err != nil {
		return nil, err
	}
	counts := map[campaign_model.RecipientStatus]int64{campaign_model.RecipientStatusPending: int64(len(recipients))}
	return campaignDetail(campaign, counts), nil
}

func (s *ManagementService) Get(ctx context.Context, instanceID, campaignID string) (*CampaignDetail, error) {
	if s == nil || s.repository == nil || ctx == nil {
		return nil, errors.New("campaign management service is unavailable")
	}
	campaign, err := s.repository.GetCampaign(ctx, instanceID, campaignID)
	if err != nil {
		return nil, err
	}
	counts, err := s.repository.RecipientCounts(ctx, instanceID, campaignID)
	if err != nil {
		return nil, err
	}
	return campaignDetail(campaign, counts), nil
}

func (s *ManagementService) List(ctx context.Context, instanceID string, status campaign_model.CampaignStatus, limit int, encodedCursor string) (*CampaignList, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}
	if status != "" && !managementCampaignStatus(status) {
		return nil, campaign_repository.ErrInvalidCampaignInput
	}
	scope := campaignCursorScope(instanceID, string(status))
	cursor, err := decodeCursor(encodedCursor, "campaigns", scope)
	if err != nil {
		return nil, err
	}
	var repositoryCursor *campaign_repository.CampaignCursor
	if cursor != nil {
		repositoryCursor = &campaign_repository.CampaignCursor{CreatedAt: cursor.At, ID: cursor.ID}
	}
	page, err := s.repository.ListCampaigns(ctx, instanceID, status, limit, repositoryCursor)
	if err != nil {
		return nil, err
	}
	result := &CampaignList{Items: page.Items}
	if page.NextCursor != nil {
		result.NextCursor, err = encodeCursor("campaigns", scope, page.NextCursor.CreatedAt, page.NextCursor.ID)
	}
	return result, err
}

func (s *ManagementService) Recipients(ctx context.Context, instanceID, campaignID string, limit int, encodedCursor string) (*RecipientList, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}
	if _, err := s.repository.GetCampaign(ctx, instanceID, campaignID); err != nil {
		return nil, err
	}
	scope := campaignCursorScope(instanceID, campaignID)
	cursor, err := decodeCursor(encodedCursor, "campaign_recipients", scope)
	if err != nil {
		return nil, err
	}
	var repositoryCursor *campaign_repository.RecipientCursor
	if cursor != nil {
		repositoryCursor = &campaign_repository.RecipientCursor{CreatedAt: cursor.At, ID: cursor.ID}
	}
	page, err := s.repository.ListRecipients(ctx, instanceID, campaignID, limit, repositoryCursor)
	if err != nil {
		return nil, err
	}
	result := &RecipientList{Items: page.Items}
	if page.NextCursor != nil {
		result.NextCursor, err = encodeCursor("campaign_recipients", scope, page.NextCursor.CreatedAt, page.NextCursor.ID)
	}
	return result, err
}

func (s *ManagementService) Audit(ctx context.Context, instanceID, campaignID string, limit int, encodedCursor string) (*AuditList, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}
	if _, err := s.repository.GetCampaign(ctx, instanceID, campaignID); err != nil {
		return nil, err
	}
	scope := campaignCursorScope(instanceID, campaignID)
	cursor, err := decodeCursor(encodedCursor, "campaign_audit", scope)
	if err != nil {
		return nil, err
	}
	var repositoryCursor *campaign_repository.AuditCursor
	if cursor != nil {
		repositoryCursor = &campaign_repository.AuditCursor{OccurredAt: cursor.At, ID: cursor.ID}
	}
	page, err := s.repository.ListAuditPage(ctx, instanceID, campaignID, limit, repositoryCursor)
	if err != nil {
		return nil, err
	}
	result := &AuditList{Items: page.Items}
	if page.NextCursor != nil {
		result.NextCursor, err = encodeCursor("campaign_audit", scope, page.NextCursor.OccurredAt, page.NextCursor.ID)
	}
	return result, err
}

func (s *ManagementService) Transition(ctx context.Context, instanceID, campaignID string, target campaign_model.CampaignStatus, startsAt *time.Time, actor campaign_repository.Actor) (*CampaignDetail, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}
	campaign, err := s.repository.Transition(ctx, instanceID, campaignID, target, startsAt, actor)
	if err != nil {
		return nil, err
	}
	counts, err := s.repository.RecipientCounts(ctx, instanceID, campaignID)
	if err != nil {
		return nil, err
	}
	return campaignDetail(campaign, counts), nil
}

func (s *ManagementService) validate(ctx context.Context) error {
	if s == nil || s.repository == nil || ctx == nil {
		return errors.New("campaign management service is unavailable")
	}
	return nil
}

func managementCampaignStatus(status campaign_model.CampaignStatus) bool {
	switch status {
	case campaign_model.CampaignStatusDraft, campaign_model.CampaignStatusScheduled, campaign_model.CampaignStatusRunning,
		campaign_model.CampaignStatusPaused, campaign_model.CampaignStatusCompleted, campaign_model.CampaignStatusAborted, campaign_model.CampaignStatusFailed:
		return true
	default:
		return false
	}
}

func campaignDetail(campaign *campaign_model.Campaign, counts map[campaign_model.RecipientStatus]int64) *CampaignDetail {
	var total int64
	for _, count := range counts {
		total += count
	}
	return &CampaignDetail{Campaign: campaign, RecipientCount: total, ByStatus: counts}
}

func decodeCursor(value, kind, scope string) (*cursorEnvelope, error) {
	if value == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, ErrInvalidCampaignCursor
	}
	var cursor cursorEnvelope
	if err := json.Unmarshal(decoded, &cursor); err != nil || cursor.Version != campaignCursorVersion || cursor.Kind != kind || cursor.Scope != scope ||
		cursor.At.IsZero() || uuid.Validate(cursor.ID) != nil {
		return nil, ErrInvalidCampaignCursor
	}
	cursor.At = cursor.At.UTC()
	return &cursor, nil
}

func encodeCursor(kind, scope string, at time.Time, id string) (string, error) {
	if strings.TrimSpace(kind) == "" || scope == "" || at.IsZero() || uuid.Validate(id) != nil {
		return "", ErrInvalidCampaignCursor
	}
	payload, err := json.Marshal(cursorEnvelope{Version: campaignCursorVersion, Kind: kind, At: at.UTC(), ID: id, Scope: scope})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func campaignCursorScope(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}
