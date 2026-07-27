package migrations

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestGroupCampaignMigrationBackfillsPopulatedSchema(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	schema := "migration_22_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	err = db.Connection(func(connection *gorm.DB) error {
		if err := connection.Exec(fmt.Sprintf(`CREATE SCHEMA "%s"`, schema)).Error; err != nil {
			return err
		}
		defer connection.Exec(fmt.Sprintf(`DROP SCHEMA "%s" CASCADE`, schema))
		if err := connection.Exec(fmt.Sprintf(`SET search_path TO "%s", public`, schema)).Error; err != nil {
			return err
		}
		if err := connection.Exec(`CREATE TABLE campaigns (
id UUID PRIMARY KEY, instance_id UUID NOT NULL, created_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE campaign_recipients (
id UUID PRIMARY KEY, campaign_id UUID NOT NULL, instance_id UUID NOT NULL,
recipient_jid VARCHAR(255) NOT NULL, created_at TIMESTAMPTZ NOT NULL
);`).Error; err != nil {
			return err
		}
		campaignID, recipientID, instanceID := uuid.NewString(), uuid.NewString(), uuid.NewString()
		if err := connection.Exec(`INSERT INTO campaigns (id, instance_id, created_at) VALUES (?, ?, NOW())`, campaignID, instanceID).Error; err != nil {
			return err
		}
		if err := connection.Exec(`INSERT INTO campaign_recipients (id, campaign_id, instance_id, recipient_jid, created_at) VALUES (?, ?, ?, '15550001@s.whatsapp.net', NOW())`, recipientID, campaignID, instanceID).Error; err != nil {
			return err
		}
		if err := connection.Exec(registeredMigration(t, 22).SQL).Error; err != nil {
			return err
		}
		var campaignTarget, recipientTarget string
		if err := connection.Raw(`SELECT target_type FROM campaigns WHERE id = ?`, campaignID).Scan(&campaignTarget).Error; err != nil {
			return err
		}
		if err := connection.Raw(`SELECT target_type FROM campaign_recipients WHERE id = ?`, recipientID).Scan(&recipientTarget).Error; err != nil {
			return err
		}
		if campaignTarget != "direct" || recipientTarget != "direct" {
			return fmt.Errorf("backfill targets = %q/%q", campaignTarget, recipientTarget)
		}
		invalid := connection.Exec(`INSERT INTO campaigns (id, instance_id, created_at, target_type) VALUES (?, ?, NOW(), 'group_list')`, uuid.NewString(), instanceID).Error
		if invalid == nil {
			return fmt.Errorf("migration accepted a group campaign without a complete snapshot")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
