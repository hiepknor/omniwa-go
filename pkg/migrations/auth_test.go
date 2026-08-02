package migrations

import (
	"strings"
	"testing"
)

func registeredAuthMigration(t *testing.T, version int64) Migration {
	t.Helper()
	for _, migration := range authRegistry {
		if migration.Version == version {
			return migration
		}
	}
	t.Fatalf("auth migration version %d is not registered", version)
	return Migration{}
}

func TestAuthRegistryIsOrderedAndPollMigrationIsAdditive(t *testing.T) {
	if err := validateRegistry(authRegistry); err != nil {
		t.Fatal(err)
	}
	migration := registeredAuthMigration(t, 1)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS poll_votes",
		"CONSTRAINT unique_vote_per_poll UNIQUE (poll_message_id, voter_jid)",
		"selected_options TEXT[] NOT NULL DEFAULT '{}'",
		"CREATE INDEX IF NOT EXISTS idx_poll_votes_instance",
		"CREATE INDEX IF NOT EXISTS idx_poll_votes_poll_message",
	} {
		if !strings.Contains(migration.SQL, required) {
			t.Fatalf("auth poll migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"DROP TABLE", "ALTER TABLE", "DELETE FROM", "TRUNCATE"} {
		if strings.Contains(migration.SQL, forbidden) {
			t.Fatalf("auth poll migration contains unsafe SQL %q", forbidden)
		}
	}
}

func TestRunAuthValidatesDependencies(t *testing.T) {
	if err := RunAuth(nil, nil); err == nil {
		t.Fatal("nil auth migration dependencies were accepted")
	}
}
