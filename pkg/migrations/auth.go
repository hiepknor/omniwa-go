package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const authAdvisoryLockKey int64 = 0x4f4d4e49574141 // "OMNIWAA"

type appliedAuthMigration struct {
	Version   int64
	Name      string
	Checksum  string
	AppliedAt time.Time
}

var authRegistry = []Migration{
	{
		Version: 1,
		Name:    "adopt_poll_votes_schema",
		SQL: `CREATE TABLE IF NOT EXISTS poll_votes (
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
CREATE INDEX IF NOT EXISTS idx_poll_votes_company ON poll_votes(company_id);
CREATE INDEX IF NOT EXISTS idx_poll_votes_instance ON poll_votes(instance_id);
CREATE INDEX IF NOT EXISTS idx_poll_votes_poll_message ON poll_votes(poll_message_id);
CREATE INDEX IF NOT EXISTS idx_poll_votes_chat ON poll_votes(poll_chat_jid);
CREATE INDEX IF NOT EXISTS idx_poll_votes_voter ON poll_votes(voter_jid);`,
	},
}

// RunAuth applies OmniWA-owned migrations to the PostgreSQL whatsmeow auth
// database. Whatsmeow's own schema remains managed by its sqlstore upgrader.
func RunAuth(ctx context.Context, db *sql.DB) error {
	if ctx == nil || db == nil {
		return errors.New("auth migration context and database are required")
	}
	if err := validateRegistry(authRegistry); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin auth migration transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", authAdvisoryLockKey); err != nil {
		return fmt.Errorf("acquire auth migration lock: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS omniwa_auth_schema_migrations (
    version BIGINT PRIMARY KEY,
    name TEXT NOT NULL,
    checksum TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`); err != nil {
		return fmt.Errorf("create auth migration registry: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT version, name, checksum, applied_at
FROM omniwa_auth_schema_migrations ORDER BY version ASC`)
	if err != nil {
		return fmt.Errorf("read auth migrations: %w", err)
	}
	applied := make(map[int64]appliedAuthMigration)
	for rows.Next() {
		var item appliedAuthMigration
		if err := rows.Scan(&item.Version, &item.Name, &item.Checksum, &item.AppliedAt); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan auth migration: %w", err)
		}
		applied[item.Version] = item
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close auth migration rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate auth migrations: %w", err)
	}
	for _, migration := range authRegistry {
		checksum := migrationChecksum(migration)
		if existing, ok := applied[migration.Version]; ok {
			if existing.Name != migration.Name || existing.Checksum != checksum {
				return fmt.Errorf("auth migration %d was modified after application", migration.Version)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
			return fmt.Errorf("apply auth migration %d %s: %w", migration.Version, migration.Name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO omniwa_auth_schema_migrations
    (version, name, checksum, applied_at) VALUES ($1, $2, $3, $4)`,
			migration.Version, migration.Name, checksum, time.Now().UTC()); err != nil {
			return fmt.Errorf("record auth migration %d: %w", migration.Version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit auth migrations: %w", err)
	}
	return nil
}
