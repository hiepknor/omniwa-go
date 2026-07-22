package campaign_service

import (
	"errors"
	"testing"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	"github.com/google/uuid"
)

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
