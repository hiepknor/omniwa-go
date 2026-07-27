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

var (
	ErrInvalidCampaignCursor        = errors.New("invalid campaign cursor")
	ErrDirectCampaignCreateDisabled = errors.New("direct campaign creation is disabled")
	ErrGroupCampaignTargetsDisabled = errors.New("group campaign targets are not enabled")
)

const campaignCursorVersion = 1

type ManagementService struct {
	repository          campaign_repository.CampaignRepository
	directCreateEnabled bool
	groupTargetsEnabled bool
}

type ManagementOption func(*ManagementService)

func WithDirectCreateEnabled(enabled bool) ManagementOption {
	return func(service *ManagementService) { service.directCreateEnabled = enabled }
}

func WithGroupTargetsEnabled(enabled bool) ManagementOption {
	return func(service *ManagementService) { service.groupTargetsEnabled = enabled }
}

type GroupListTargetInput struct {
	Type             campaign_model.CampaignTargetType
	GroupListID      string
	GroupListVersion int64
}

type CreateCampaignInput struct {
	Name        string
	TextBody    string
	Target      *GroupListTargetInput
	Recipients  []campaign_repository.RecipientConsent
	InstanceJID string
	Actor       campaign_repository.Actor
}

type CampaignTarget struct {
	Type             campaign_model.CampaignTargetType `json:"type"`
	GroupListID      *string                           `json:"groupListId,omitempty"`
	GroupListName    *string                           `json:"groupListName,omitempty"`
	GroupListVersion *int64                            `json:"groupListVersion,omitempty"`
	TargetCount      int64                             `json:"targetCount"`
}

type Progress struct {
	Total      int64     `json:"total"`
	Processed  int64     `json:"processed"`
	Pending    int64     `json:"pending"`
	Processing int64     `json:"processing"`
	Sent       int64     `json:"sent"`
	Delivered  int64     `json:"delivered"`
	Read       int64     `json:"read"`
	Failed     int64     `json:"failed"`
	Skipped    int64     `json:"skipped"`
	Aborted    int64     `json:"aborted"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type CampaignDetail struct {
	Campaign       *campaign_model.Campaign                 `json:"campaign"`
	RecipientCount int64                                    `json:"recipientCount"`
	ByStatus       map[campaign_model.RecipientStatus]int64 `json:"byStatus"`
	Target         CampaignTarget                           `json:"target"`
	Progress       Progress                                 `json:"progress"`
	StatusReason   *string                                  `json:"statusReason"`
	PauseReason    *string                                  `json:"pauseReason"`
	RetryAt        *time.Time                               `json:"retryAt"`
	NeedsAttention bool                                     `json:"needsAttention"`
}

type CampaignSummary struct {
	campaign_model.Campaign
	Target         CampaignTarget `json:"target"`
	Progress       Progress       `json:"progress"`
	StatusReason   *string        `json:"statusReason"`
	PauseReason    *string        `json:"pauseReason"`
	RetryAt        *time.Time     `json:"retryAt"`
	NeedsAttention bool           `json:"needsAttention"`
}

type CampaignList struct {
	Items      []CampaignSummary `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
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

func NewManagementService(repository campaign_repository.CampaignRepository, options ...ManagementOption) *ManagementService {
	service := &ManagementService{repository: repository, directCreateEnabled: true}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *ManagementService) Create(ctx context.Context, instanceID string, input CreateCampaignInput) (*CampaignDetail, error) {
	if s == nil || s.repository == nil || ctx == nil {
		return nil, errors.New("campaign management service is unavailable")
	}
	if (input.Target == nil) == (len(input.Recipients) == 0) {
		return nil, campaign_repository.ErrInvalidCampaignInput
	}
	var campaign *campaign_model.Campaign
	var recipients []campaign_model.Recipient
	var err error
	if input.Target != nil {
		if !s.groupTargetsEnabled {
			return nil, ErrGroupCampaignTargetsDisabled
		}
		if input.Target.Type != campaign_model.CampaignTargetGroupList || uuid.Validate(input.Target.GroupListID) != nil || input.Target.GroupListVersion < 1 {
			return nil, campaign_repository.ErrInvalidCampaignInput
		}
		campaign, recipients, err = s.repository.CreateGroupDraft(ctx, instanceID, campaign_repository.GroupDraftInput{
			Name: input.Name, TextBody: input.TextBody, GroupListID: input.Target.GroupListID,
			GroupListVersion: input.Target.GroupListVersion, InstanceJID: input.InstanceJID, Actor: input.Actor,
		})
	} else {
		if !s.directCreateEnabled {
			return nil, ErrDirectCampaignCreateDisabled
		}
		campaign, recipients, err = s.repository.CreateDraft(ctx, instanceID, campaign_repository.DraftInput{
			Name: input.Name, TextBody: input.TextBody, Recipients: input.Recipients, Actor: input.Actor,
		})
	}
	if err != nil {
		return nil, err
	}
	counts := map[campaign_model.RecipientStatus]int64{campaign_model.RecipientStatusPending: int64(len(recipients))}
	snapshot := campaign_repository.RecipientProgress{Counts: counts, UpdatedAt: campaign.UpdatedAt}
	return campaignDetail(campaign, snapshot), nil
}

func (s *ManagementService) Get(ctx context.Context, instanceID, campaignID string) (*CampaignDetail, error) {
	if s == nil || s.repository == nil || ctx == nil {
		return nil, errors.New("campaign management service is unavailable")
	}
	campaign, err := s.repository.GetCampaign(ctx, instanceID, campaignID)
	if err != nil {
		return nil, err
	}
	snapshots, err := s.repository.ProgressSnapshots(ctx, instanceID, []string{campaignID})
	if err != nil {
		return nil, err
	}
	return campaignDetail(campaign, snapshots[campaignID]), nil
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
	ids := make([]string, len(page.Items))
	for index := range page.Items {
		ids[index] = page.Items[index].ID
	}
	result := &CampaignList{Items: make([]CampaignSummary, len(page.Items))}
	if len(ids) > 0 {
		snapshots, progressErr := s.repository.ProgressSnapshots(ctx, instanceID, ids)
		if progressErr != nil {
			return nil, progressErr
		}
		for index := range page.Items {
			campaign := page.Items[index]
			detail := campaignDetail(&campaign, snapshots[campaign.ID])
			result.Items[index] = CampaignSummary{
				Campaign: campaign, Target: detail.Target, Progress: detail.Progress,
				StatusReason: detail.StatusReason, PauseReason: detail.PauseReason, RetryAt: detail.RetryAt, NeedsAttention: detail.NeedsAttention,
			}
		}
	}
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

func (s *ManagementService) Transition(ctx context.Context, instanceID, campaignID, instanceJID string, target campaign_model.CampaignStatus, startsAt *time.Time, actor campaign_repository.Actor) (*CampaignDetail, error) {
	if err := s.validate(ctx); err != nil {
		return nil, err
	}
	stored, err := s.repository.GetCampaign(ctx, instanceID, campaignID)
	if err != nil {
		return nil, err
	}
	var campaign *campaign_model.Campaign
	if stored.TargetType == campaign_model.CampaignTargetGroupList && target == campaign_model.CampaignStatusRunning {
		if !s.groupTargetsEnabled {
			return nil, ErrGroupCampaignTargetsDisabled
		}
		campaign, err = s.repository.ActivateGroupCampaign(ctx, instanceID, campaignID, instanceJID, actor)
	} else {
		campaign, err = s.repository.Transition(ctx, instanceID, campaignID, target, startsAt, actor)
	}
	if err != nil {
		return nil, err
	}
	snapshots, err := s.repository.ProgressSnapshots(ctx, instanceID, []string{campaignID})
	if err != nil {
		return nil, err
	}
	return campaignDetail(campaign, snapshots[campaignID]), nil
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

func campaignDetail(campaign *campaign_model.Campaign, snapshot campaign_repository.RecipientProgress) *CampaignDetail {
	counts := snapshot.Counts
	if counts == nil {
		counts = map[campaign_model.RecipientStatus]int64{}
	}
	var total int64
	for _, count := range counts {
		total += count
	}
	updatedAt := campaign.UpdatedAt.UTC()
	if snapshot.UpdatedAt.After(updatedAt) {
		updatedAt = snapshot.UpdatedAt.UTC()
	}
	progress := Progress{
		Total: total, Pending: counts[campaign_model.RecipientStatusPending], Processing: counts[campaign_model.RecipientStatusProcessing],
		Sent: counts[campaign_model.RecipientStatusSent], Delivered: counts[campaign_model.RecipientStatusDelivered], Read: counts[campaign_model.RecipientStatusRead],
		Failed: counts[campaign_model.RecipientStatusFailed], Skipped: counts[campaign_model.RecipientStatusSkipped], Aborted: counts[campaign_model.RecipientStatusAborted],
		UpdatedAt: updatedAt,
	}
	progress.Processed = progress.Sent + progress.Delivered + progress.Read + progress.Failed + progress.Skipped + progress.Aborted
	retryAt := earliestTime(campaign.RetryAt, snapshot.RetryAt)
	target := CampaignTarget{Type: campaign.TargetType, TargetCount: total}
	if target.Type == "" {
		target.Type = campaign_model.CampaignTargetDirect
	}
	if target.Type == campaign_model.CampaignTargetGroupList {
		target.GroupListID = campaign.GroupListID
		target.GroupListName = campaign.GroupListNameSnapshot
		target.GroupListVersion = campaign.GroupListVersion
	}
	return &CampaignDetail{
		Campaign: campaign, RecipientCount: total, ByStatus: counts, Target: target, Progress: progress,
		StatusReason: campaign.StatusReason, PauseReason: campaign.PauseReason, RetryAt: retryAt, NeedsAttention: campaign.NeedsAttention,
	}
}

func earliestTime(values ...*time.Time) *time.Time {
	var earliest *time.Time
	for _, value := range values {
		if value != nil && (earliest == nil || value.Before(*earliest)) {
			copy := value.UTC()
			earliest = &copy
		}
	}
	return earliest
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
