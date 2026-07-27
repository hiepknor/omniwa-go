package campaign_service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	campaign_repository "github.com/evolution-foundation/evolution-go/pkg/campaign/repository"
	"github.com/google/uuid"
)

type managementRepositoryStub struct {
	campaign_repository.CampaignRepository
	directCalls int
	groupCalls  int
	directInput campaign_repository.DraftInput
	groupInput  campaign_repository.GroupDraftInput
	campaigns   []campaign_model.Campaign
	snapshots   map[string]campaign_repository.RecipientProgress
	stored      *campaign_model.Campaign
}

func (stub *managementRepositoryStub) GetCampaign(context.Context, string, string) (*campaign_model.Campaign, error) {
	return stub.stored, nil
}

func (stub *managementRepositoryStub) CreateDraft(_ context.Context, instanceID string, input campaign_repository.DraftInput) (*campaign_model.Campaign, []campaign_model.Recipient, error) {
	stub.directCalls++
	stub.directInput = input
	campaign := &campaign_model.Campaign{ID: uuid.NewString(), InstanceID: instanceID, ContentType: input.ContentType, TextBody: input.TextBody, TargetType: campaign_model.CampaignTargetDirect, UpdatedAt: time.Unix(100, 0)}
	if input.ContentType == campaign_model.CampaignContentImage {
		campaign.MediaAssetID = &input.MediaAssetID
	}
	return campaign, []campaign_model.Recipient{{Status: campaign_model.RecipientStatusPending}}, nil
}

func (stub *managementRepositoryStub) CreateGroupDraft(_ context.Context, instanceID string, input campaign_repository.GroupDraftInput) (*campaign_model.Campaign, []campaign_model.Recipient, error) {
	stub.groupCalls++
	stub.groupInput = input
	name, version := "Branches", input.GroupListVersion
	campaign := &campaign_model.Campaign{
		ID: uuid.NewString(), InstanceID: instanceID, ContentType: input.ContentType, TextBody: input.TextBody, TargetType: campaign_model.CampaignTargetGroupList,
		GroupListID: &input.GroupListID, GroupListNameSnapshot: &name, GroupListVersion: &version, UpdatedAt: time.Unix(100, 0),
	}
	if input.ContentType == campaign_model.CampaignContentImage {
		campaign.MediaAssetID = &input.MediaAssetID
	}
	return campaign, []campaign_model.Recipient{{Status: campaign_model.RecipientStatusPending}}, nil
}

func TestImageCampaignCreateIsGatedAndPreservesMediaReferenceAndCaption(t *testing.T) {
	instanceID, mediaID := uuid.NewString(), uuid.NewString()
	input := CreateCampaignInput{
		Name: "Image", ContentType: campaign_model.CampaignContentImage, MediaAssetID: mediaID, TextBody: "Optional caption",
		Target:      &GroupListTargetInput{Type: campaign_model.CampaignTargetGroupList, GroupListID: uuid.NewString(), GroupListVersion: 1},
		InstanceJID: "1@s.whatsapp.net", Actor: campaign_repository.Actor{Type: "system"},
	}
	repository := &managementRepositoryStub{}
	if _, err := NewManagementService(repository, WithGroupTargetsEnabled(true)).Create(context.Background(), instanceID, input); !errors.Is(err, ErrImageCampaignContentDisabled) || repository.groupCalls != 0 {
		t.Fatalf("disabled image create err=%v calls=%d", err, repository.groupCalls)
	}
	detail, err := NewManagementService(repository, WithGroupTargetsEnabled(true), WithImageContentEnabled(true)).Create(context.Background(), instanceID, input)
	if err != nil || repository.groupInput.MediaAssetID != mediaID || repository.groupInput.TextBody != "Optional caption" ||
		detail.Content.Type != campaign_model.CampaignContentImage || detail.Content.MediaID == nil || *detail.Content.MediaID != mediaID ||
		detail.Content.Caption == nil || *detail.Content.Caption != "Optional caption" {
		t.Fatalf("detail=%+v input=%+v err=%v", detail, repository.groupInput, err)
	}
	direct := input
	direct.Target = nil
	direct.Recipients = []campaign_repository.RecipientConsent{{JID: "1@s.whatsapp.net"}}
	if _, err := NewManagementService(repository, WithImageContentEnabled(true)).Create(context.Background(), instanceID, direct); !errors.Is(err, campaign_repository.ErrInvalidCampaignInput) {
		t.Fatalf("direct image create error=%v", err)
	}
}

func TestImageCampaignTransitionIsGatedBeforeRepositoryMutation(t *testing.T) {
	repository := &managementRepositoryStub{stored: &campaign_model.Campaign{ContentType: campaign_model.CampaignContentImage}}
	_, err := NewManagementService(repository).Transition(
		context.Background(), uuid.NewString(), uuid.NewString(), "1@s.whatsapp.net", campaign_model.CampaignStatusRunning, nil, campaign_repository.Actor{Type: "system"},
	)
	if !errors.Is(err, ErrImageCampaignContentDisabled) {
		t.Fatalf("disabled image transition error=%v", err)
	}
}

func (stub *managementRepositoryStub) ListCampaigns(_ context.Context, _ string, _ campaign_model.CampaignStatus, _ int, _ *campaign_repository.CampaignCursor) (*campaign_repository.CampaignPage, error) {
	return &campaign_repository.CampaignPage{Items: stub.campaigns}, nil
}

func (stub *managementRepositoryStub) ProgressSnapshots(_ context.Context, _ string, _ []string) (map[string]campaign_repository.RecipientProgress, error) {
	return stub.snapshots, nil
}

func TestCampaignCursorRoundTripIsTypedAndOpaque(t *testing.T) {
	at, id := time.Unix(100, 0).UTC(), uuid.NewString()
	scope := campaignCursorScope("instance-a", "running")
	encoded, err := encodeCursor("campaigns", scope, at, id)
	if err != nil || encoded == "" || encoded == id {
		t.Fatalf("encodeCursor() = %q, %v", encoded, err)
	}
	decoded, err := decodeCursor(encoded, "campaigns", scope)
	if err != nil || decoded.ID != id || !decoded.At.Equal(at) {
		t.Fatalf("decodeCursor() = %#v, %v", decoded, err)
	}
	if _, err := decodeCursor(encoded, "campaign_recipients", scope); !errors.Is(err, ErrInvalidCampaignCursor) {
		t.Fatalf("cross-resource cursor error = %v", err)
	}
	if _, err := decodeCursor(encoded, "campaigns", campaignCursorScope("instance-b", "running")); !errors.Is(err, ErrInvalidCampaignCursor) {
		t.Fatalf("cross-scope cursor error = %v", err)
	}
	if _, err := decodeCursor("forged", "campaigns", scope); !errors.Is(err, ErrInvalidCampaignCursor) {
		t.Fatalf("forged cursor error = %v", err)
	}
}

func TestManagementCampaignStatusIsStrict(t *testing.T) {
	for _, status := range []campaign_model.CampaignStatus{
		campaign_model.CampaignStatusDraft, campaign_model.CampaignStatusScheduled, campaign_model.CampaignStatusRunning,
		campaign_model.CampaignStatusPaused, campaign_model.CampaignStatusCompleted, campaign_model.CampaignStatusAborted, campaign_model.CampaignStatusFailed,
	} {
		if !managementCampaignStatus(status) {
			t.Fatalf("valid status rejected: %s", status)
		}
	}
	if managementCampaignStatus("unknown") {
		t.Fatal("unknown status accepted")
	}
}

func TestCampaignCreateGatesDirectAndGroupContracts(t *testing.T) {
	instanceID, groupListID := uuid.NewString(), uuid.NewString()
	actor := campaign_repository.Actor{Type: "system"}
	directInput := CreateCampaignInput{Name: "Direct", TextBody: "hello", Recipients: []campaign_repository.RecipientConsent{{JID: "1@s.whatsapp.net"}}, Actor: actor}
	groupInput := CreateCampaignInput{Name: "Groups", TextBody: "hello", Target: &GroupListTargetInput{Type: campaign_model.CampaignTargetGroupList, GroupListID: groupListID, GroupListVersion: 2}, InstanceJID: "1@s.whatsapp.net", Actor: actor}

	repository := &managementRepositoryStub{}
	service := NewManagementService(repository)
	if _, err := service.Create(context.Background(), instanceID, groupInput); !errors.Is(err, ErrGroupCampaignTargetsDisabled) || repository.groupCalls != 0 {
		t.Fatalf("disabled group create = %v calls=%d", err, repository.groupCalls)
	}
	if _, err := service.Create(context.Background(), instanceID, directInput); !errors.Is(err, ErrDirectCampaignCreateDisabled) || repository.directCalls != 0 {
		t.Fatalf("default direct create = %v calls=%d", err, repository.directCalls)
	}

	repository = &managementRepositoryStub{}
	service = NewManagementService(repository, WithGroupTargetsEnabled(true))
	if _, err := service.Create(context.Background(), instanceID, directInput); !errors.Is(err, ErrDirectCampaignCreateDisabled) || repository.directCalls != 0 {
		t.Fatalf("disabled direct create = %v calls=%d", err, repository.directCalls)
	}
	detail, err := service.Create(context.Background(), instanceID, groupInput)
	if err != nil || repository.groupCalls != 1 || detail.Target.Type != campaign_model.CampaignTargetGroupList || detail.Target.TargetCount != 1 {
		t.Fatalf("enabled group create = %#v, %v calls=%d", detail, err, repository.groupCalls)
	}
	if _, err := service.Create(context.Background(), instanceID, CreateCampaignInput{Name: "ambiguous", TextBody: "hello", Target: groupInput.Target, Recipients: directInput.Recipients}); !errors.Is(err, campaign_repository.ErrInvalidCampaignInput) {
		t.Fatalf("ambiguous create error = %v", err)
	}

	repository = &managementRepositoryStub{}
	service = NewManagementService(repository, WithDirectCreateEnabled(true))
	if _, err := service.Create(context.Background(), instanceID, directInput); err != nil || repository.directCalls != 1 {
		t.Fatalf("emergency direct create = %v calls=%d", err, repository.directCalls)
	}
}

func TestCampaignListUsesTerminalProgressDefinition(t *testing.T) {
	campaignID := uuid.NewString()
	updatedAt := time.Unix(200, 0).UTC()
	repository := &managementRepositoryStub{
		campaigns: []campaign_model.Campaign{{ID: campaignID, TargetType: campaign_model.CampaignTargetDirect, UpdatedAt: time.Unix(100, 0).UTC()}},
		snapshots: map[string]campaign_repository.RecipientProgress{campaignID: {
			Counts: map[campaign_model.RecipientStatus]int64{
				campaign_model.RecipientStatusPending: 2, campaign_model.RecipientStatusProcessing: 1,
				campaign_model.RecipientStatusSent: 3, campaign_model.RecipientStatusDelivered: 4,
				campaign_model.RecipientStatusRead: 5, campaign_model.RecipientStatusFailed: 6,
				campaign_model.RecipientStatusSkipped: 7, campaign_model.RecipientStatusAborted: 8,
			}, UpdatedAt: updatedAt,
		}},
	}
	result, err := NewManagementService(repository).List(context.Background(), uuid.NewString(), "", 10, "")
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("List() = %#v, %v", result, err)
	}
	progress := result.Items[0].Progress
	if progress.Total != 36 || progress.Processed != 33 || progress.Pending != 2 || progress.Processing != 1 || !progress.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("progress = %#v", progress)
	}
	payload, err := json.Marshal(result.Items[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"statusReason":null`, `"pauseReason":null`, `"retryAt":null`, `"needsAttention":false`, `"progress":`, `"target":`} {
		if !strings.Contains(string(payload), field) {
			t.Fatalf("campaign summary omitted %s: %s", field, payload)
		}
	}
}
