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

func TestPollVotesMigrationAdoptsPopulatedLegacyTable(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	schema := "migration_41_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	err = db.Connection(func(connection *gorm.DB) error {
		if err := connection.Exec(fmt.Sprintf(`CREATE SCHEMA "%s"`, schema)).Error; err != nil {
			return err
		}
		defer connection.Exec(fmt.Sprintf(`DROP SCHEMA "%s" CASCADE`, schema))
		if err := connection.Exec(fmt.Sprintf(`SET search_path TO "%s", public`, schema)).Error; err != nil {
			return err
		}
		if err := connection.Exec(`CREATE TABLE poll_votes (
id VARCHAR(255) PRIMARY KEY,
company_id VARCHAR(255) NOT NULL,
instance_id VARCHAR(255) NOT NULL,
poll_message_id VARCHAR(255) NOT NULL,
poll_chat_jid VARCHAR(255) NOT NULL,
vote_message_id VARCHAR(255) NOT NULL,
voter_jid VARCHAR(255) NOT NULL,
voter_phone VARCHAR(255),
voter_name VARCHAR(255),
selected_options TEXT[] NOT NULL DEFAULT '{}',
voted_at TIMESTAMP NOT NULL DEFAULT NOW(),
received_at TIMESTAMP NOT NULL DEFAULT NOW(),
CONSTRAINT unique_vote_per_poll UNIQUE (poll_message_id, voter_jid)
);
INSERT INTO poll_votes (
id, company_id, instance_id, poll_message_id, poll_chat_jid,
vote_message_id, voter_jid, selected_options
) VALUES ('vote-1', 'company-1', 'instance-1', 'poll-1', 'chat-1', 'message-1', 'voter-1', '{}');`).Error; err != nil {
			return err
		}
		migration := registeredAuthMigration(t, 1)
		if err := connection.Exec(migration.SQL).Error; err != nil {
			return err
		}
		if err := connection.Exec(migration.SQL).Error; err != nil {
			return fmt.Errorf("repeat poll migration: %w", err)
		}
		var votes, indexes int64
		if err := connection.Raw(`SELECT COUNT(*) FROM poll_votes`).Scan(&votes).Error; err != nil {
			return err
		}
		if err := connection.Raw(`SELECT COUNT(*) FROM pg_indexes WHERE schemaname = ? AND tablename = 'poll_votes' AND indexname LIKE 'idx_poll_votes_%'`, schema).Scan(&indexes).Error; err != nil {
			return err
		}
		if votes != 1 || indexes != 5 {
			return fmt.Errorf("poll adoption votes=%d indexes=%d", votes, indexes)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
