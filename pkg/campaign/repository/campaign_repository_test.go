package campaign_repository

import (
	"strings"
	"testing"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	"github.com/google/uuid"
)

func TestBuildRecipientsNormalizesAndHashesConsent(t *testing.T) {
	now := time.Unix(200, 0).UTC()
	campaign := &campaign_model.Campaign{ID: uuid.NewString(), InstanceID: uuid.NewString()}
	recipients, err := buildRecipients(campaign, validRecipientConsent(), now)
	if err != nil || len(recipients) != 1 {
		t.Fatalf("buildRecipients() = %#v, %v", recipients, err)
	}
	if recipients[0].OptInReferenceHash == "consent-record-1" || len(recipients[0].OptInReferenceHash) != 64 || recipients[0].RecipientJID != "15550001@s.whatsapp.net" {
		t.Fatalf("stored recipient = %#v", recipients[0])
	}
	if recipients[0].Status != campaign_model.RecipientStatusPending || recipients[0].CampaignID != campaign.ID || recipients[0].InstanceID != campaign.InstanceID {
		t.Fatalf("recipient identity/state = %#v", recipients[0])
	}
}

func TestBuildRecipientsRejectsMissingDuplicateOrUnsafeConsent(t *testing.T) {
	now := time.Unix(200, 0).UTC()
	campaign := &campaign_model.Campaign{ID: uuid.NewString(), InstanceID: uuid.NewString()}
	tests := []struct {
		name  string
		input []RecipientConsent
	}{
		{name: "missing evidence", input: []RecipientConsent{{JID: "15550001@s.whatsapp.net", OptInSource: "api", OptedInAt: time.Unix(100, 0)}}},
		{name: "future opt in", input: []RecipientConsent{{JID: "15550001@s.whatsapp.net", OptInSource: "api", EvidenceReference: "one", OptedInAt: time.Unix(300, 0)}}},
		{name: "group recipient", input: []RecipientConsent{{JID: "group@g.us", OptInSource: "api", EvidenceReference: "one", OptedInAt: time.Unix(100, 0)}}},
		{name: "duplicate canonical recipient", input: append(validRecipientConsent(), RecipientConsent{JID: "15550001:2@s.whatsapp.net", OptInSource: "api", EvidenceReference: "two", OptedInAt: time.Unix(100, 0)})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := buildRecipients(campaign, test.input, now); err == nil {
				t.Fatal("unsafe consent was accepted")
			}
		})
	}
}

func TestCampaignTransitionsAreStrict(t *testing.T) {
	valid := [][2]campaign_model.CampaignStatus{
		{campaign_model.CampaignStatusDraft, campaign_model.CampaignStatusScheduled},
		{campaign_model.CampaignStatusScheduled, campaign_model.CampaignStatusRunning},
		{campaign_model.CampaignStatusRunning, campaign_model.CampaignStatusPaused},
		{campaign_model.CampaignStatusPaused, campaign_model.CampaignStatusRunning},
		{campaign_model.CampaignStatusRunning, campaign_model.CampaignStatusCompleted},
	}
	for _, transition := range valid {
		if !canTransitionCampaign(transition[0], transition[1]) {
			t.Fatalf("valid transition rejected: %s -> %s", transition[0], transition[1])
		}
	}
	invalid := [][2]campaign_model.CampaignStatus{
		{campaign_model.CampaignStatusDraft, campaign_model.CampaignStatusRunning},
		{campaign_model.CampaignStatusCompleted, campaign_model.CampaignStatusRunning},
		{campaign_model.CampaignStatusAborted, campaign_model.CampaignStatusScheduled},
		{campaign_model.CampaignStatusFailed, campaign_model.CampaignStatusRunning},
	}
	for _, transition := range invalid {
		if canTransitionCampaign(transition[0], transition[1]) {
			t.Fatalf("invalid transition accepted: %s -> %s", transition[0], transition[1])
		}
	}
	for _, status := range []campaign_model.CampaignStatus{campaign_model.CampaignStatusCompleted, campaign_model.CampaignStatusAborted, campaign_model.CampaignStatusFailed} {
		if !isTerminalCampaignStatus(status) {
			t.Fatalf("terminal status not recognized: %s", status)
		}
	}
}

func TestActorReferencesAreRequiredAndHashed(t *testing.T) {
	if _, err := validateActor(Actor{Type: "admin"}, "scope-a"); err == nil {
		t.Fatal("unattributed admin actor was accepted")
	}
	if hash, err := validateActor(Actor{Type: "system"}, "scope-a"); err != nil || hash != nil {
		t.Fatalf("system actor = %v, %v", hash, err)
	}
	hash, err := validateActor(Actor{Type: "instance", Reference: "instance-token"}, "scope-a")
	if err != nil || hash == nil || *hash == "instance-token" || len(*hash) != 64 {
		t.Fatalf("instance actor hash = %v, %v", hash, err)
	}
	otherHash, err := validateActor(Actor{Type: "instance", Reference: "instance-token"}, "scope-b")
	if err != nil || otherHash == nil || *otherHash == *hash {
		t.Fatalf("actor hash was correlatable across scopes: %v, %v", otherHash, err)
	}
}

func TestDraftValidationBoundsContentAndAttribution(t *testing.T) {
	tests := []struct {
		name  string
		input *DraftInput
	}{
		{name: "missing", input: nil},
		{name: "blank name", input: &DraftInput{Name: " ", TextBody: "hello", Recipients: validRecipientConsent(), Actor: Actor{Type: "system"}}},
		{name: "blank text", input: &DraftInput{Name: "name", TextBody: "\n", Recipients: validRecipientConsent(), Actor: Actor{Type: "system"}}},
		{name: "oversized text", input: &DraftInput{Name: "name", TextBody: strings.Repeat("x", 4097), Recipients: validRecipientConsent(), Actor: Actor{Type: "system"}}},
		{name: "no recipients", input: &DraftInput{Name: "name", TextBody: "hello", Actor: Actor{Type: "system"}}},
		{name: "too many recipients", input: &DraftInput{Name: "name", TextBody: "hello", Recipients: make([]RecipientConsent, maxCampaignRecipients+1), Actor: Actor{Type: "system"}}},
		{name: "unattributed admin", input: &DraftInput{Name: "name", TextBody: "hello", Recipients: validRecipientConsent(), Actor: Actor{Type: "admin"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := validateDraftInput(test.input, "scope-a"); err == nil {
				t.Fatal("invalid draft was accepted")
			}
		})
	}
}

func validRecipientConsent() []RecipientConsent {
	return []RecipientConsent{{
		JID: "15550001@s.whatsapp.net", OptInSource: "api_attestation",
		EvidenceReference: "consent-record-1", OptedInAt: time.Unix(100, 0).UTC(),
	}}
}
