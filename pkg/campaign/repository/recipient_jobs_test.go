package campaign_repository

import (
	"context"
	"strings"
	"testing"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestRecipientJobInputBounds(t *testing.T) {
	if boundedProviderMessageID("  provider-id  ") != "provider-id" {
		t.Fatal("provider message ID was not normalized")
	}
	if boundedProviderMessageID(strings.Repeat("x", 256)) != "" {
		t.Fatal("oversized provider message ID was accepted")
	}
	for _, code := range []string{"temporary_failure", "upstream_429", "send2"} {
		if !safeCampaignErrorCode.MatchString(code) {
			t.Fatalf("safe error code rejected: %s", code)
		}
	}
	for _, code := range []string{"", "contains space", "TOKEN=secret", strings.Repeat("x", 65)} {
		if safeCampaignErrorCode.MatchString(code) {
			t.Fatalf("unsafe error code accepted: %q", code)
		}
	}
}

func TestClaimMutationRequiresClaimedIdentity(t *testing.T) {
	repository := &campaignRepository{db: &gorm.DB{}, now: time.Now}
	recipient := &campaign_model.Recipient{ID: uuid.NewString(), CampaignID: uuid.NewString(), InstanceID: uuid.NewString()}
	if err := repository.validateClaimMutation(context.Background(), recipient); err == nil {
		t.Fatal("recipient without a claim token was accepted")
	}
	token := uuid.NewString()
	recipient.ClaimToken = &token
	if err := repository.validateClaimMutation(context.Background(), recipient); err != nil {
		t.Fatalf("valid claimed recipient rejected: %v", err)
	}
}
