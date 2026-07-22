package campaign_model

import "testing"

func TestCampaignTableNamesAreStable(t *testing.T) {
	if (Campaign{}).TableName() != "campaigns" || (Recipient{}).TableName() != "campaign_recipients" || (AuditEvent{}).TableName() != "campaign_audit_events" {
		t.Fatal("campaign persistence table names changed")
	}
}
