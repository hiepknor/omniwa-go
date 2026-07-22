// Package migrations applies ordered, immutable PostgreSQL schema migrations.
package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"
)

const advisoryLockKey int64 = 0x4f4d4e495741 // "OMNIWA"

type Migration struct {
	Version int64
	Name    string
	SQL     string
}

type appliedMigration struct {
	Version   int64     `gorm:"primaryKey"`
	Name      string    `gorm:"not null"`
	Checksum  string    `gorm:"not null"`
	AppliedAt time.Time `gorm:"not null"`
}

func (appliedMigration) TableName() string { return "schema_migrations" }

var registry = []Migration{
	{
		Version: 1,
		Name:    "create_projection_states",
		SQL: `CREATE TABLE projection_states (
    instance_id UUID NOT NULL,
    resource VARCHAR(64) NOT NULL,
    sync_status VARCHAR(32) NOT NULL DEFAULT 'not_started',
    last_event_at TIMESTAMPTZ NULL,
    last_reconciled_at TIMESTAMPTZ NULL,
    stale_since TIMESTAMPTZ NULL,
    schema_version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (instance_id, resource),
    CONSTRAINT projection_states_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE,
    CONSTRAINT projection_states_status_check CHECK (sync_status IN ('not_started', 'syncing', 'ready', 'stale', 'failed'))
);
CREATE INDEX projection_states_status_idx ON projection_states (sync_status, updated_at);`,
	},
	{
		Version: 2,
		Name:    "create_projection_event_inbox",
		SQL: `CREATE TABLE projection_event_inbox (
    instance_id UUID NOT NULL,
    resource VARCHAR(64) NOT NULL,
    event_key VARCHAR(255) NOT NULL,
    entity_key VARCHAR(255) NOT NULL,
    event_type VARCHAR(64) NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    ingested_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    payload JSONB NOT NULL,
    claim_token VARCHAR(64) NULL,
    lease_until TIMESTAMPTZ NULL,
    processed_at TIMESTAMPTZ NULL,
    retry_count INTEGER NOT NULL DEFAULT 0,
    last_error_code VARCHAR(64) NULL,
    PRIMARY KEY (instance_id, resource, event_key),
    CONSTRAINT projection_event_inbox_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE,
    CONSTRAINT projection_event_inbox_status_check CHECK (status IN ('pending', 'processing', 'processed', 'failed')),
    CONSTRAINT projection_event_inbox_retry_count_check CHECK (retry_count >= 0)
);
CREATE INDEX projection_event_inbox_work_idx ON projection_event_inbox (available_at, occurred_at, ingested_at)
    WHERE status IN ('pending', 'failed');
CREATE INDEX projection_event_inbox_expired_lease_idx ON projection_event_inbox (lease_until)
    WHERE status = 'processing';`,
	},
	{
		Version: 3,
		Name:    "create_groups_projection",
		SQL: `CREATE TABLE projected_groups (
    instance_id UUID NOT NULL,
    group_id VARCHAR(255) NOT NULL,
    name TEXT NULL,
    topic TEXT NULL,
    owner_jid VARCHAR(255) NULL,
    owner_phone_jid VARCHAR(255) NULL,
    locked BOOLEAN NULL,
    announce BOOLEAN NULL,
    ephemeral_enabled BOOLEAN NULL,
    ephemeral_timer BIGINT NULL,
    join_approval_required BOOLEAN NULL,
    suspended BOOLEAN NULL,
    participant_version VARCHAR(255) NULL,
    addressing_mode VARCHAR(32) NULL,
    member_add_mode VARCHAR(32) NULL,
    parent_group_id VARCHAR(255) NULL,
    is_parent BOOLEAN NULL,
    is_default_subgroup BOOLEAN NULL,
    invite_link TEXT NULL,
    invite_link_updated_at TIMESTAMPTZ NULL,
    provider_created_at TIMESTAMPTZ NULL,
    source_occurred_at TIMESTAMPTZ NOT NULL,
    source_event_key VARCHAR(255) NOT NULL,
    last_synced_at TIMESTAMPTZ NOT NULL,
    tombstoned_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (instance_id, group_id),
    CONSTRAINT projected_groups_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE,
    CONSTRAINT projected_groups_ephemeral_timer_check CHECK (ephemeral_timer IS NULL OR ephemeral_timer >= 0)
);
CREATE INDEX projected_groups_list_idx ON projected_groups (instance_id, name, group_id) WHERE tombstoned_at IS NULL;
CREATE INDEX projected_groups_freshness_idx ON projected_groups (instance_id, last_synced_at);

CREATE TABLE projected_group_participants (
    instance_id UUID NOT NULL,
    group_id VARCHAR(255) NOT NULL,
    participant_id VARCHAR(255) NOT NULL,
    phone_number_jid VARCHAR(255) NULL,
    lid VARCHAR(255) NULL,
    display_name TEXT NULL,
    role VARCHAR(32) NOT NULL DEFAULT 'member',
    source_occurred_at TIMESTAMPTZ NOT NULL,
    source_event_key VARCHAR(255) NOT NULL,
    last_synced_at TIMESTAMPTZ NOT NULL,
    tombstoned_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (instance_id, group_id, participant_id),
    CONSTRAINT projected_group_participants_group_fk FOREIGN KEY (instance_id, group_id) REFERENCES projected_groups(instance_id, group_id) ON DELETE CASCADE,
    CONSTRAINT projected_group_participants_role_check CHECK (role IN ('member', 'admin', 'super_admin'))
);
CREATE INDEX projected_group_participants_list_idx ON projected_group_participants (instance_id, group_id, role, participant_id) WHERE tombstoned_at IS NULL;`,
	},
	{
		Version: 4,
		Name:    "add_group_field_versions",
		SQL: `ALTER TABLE projected_groups ADD COLUMN field_versions JSONB NOT NULL DEFAULT '{}'::jsonb;
UPDATE projected_groups
SET field_versions = jsonb_build_object(
    '_snapshot', jsonb_build_object('occurredAt', source_occurred_at, 'eventKey', source_event_key)
);`,
	},
}

func Run(db *gorm.DB) error {
	if db == nil {
		return errors.New("migration database is required")
	}
	if db.Dialector.Name() != "postgres" {
		return fmt.Errorf("versioned migrations require PostgreSQL, got %s", db.Dialector.Name())
	}
	if err := validateRegistry(registry); err != nil {
		return err
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", advisoryLockKey).Error; err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
		if err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
    version BIGINT PRIMARY KEY,
    name TEXT NOT NULL,
    checksum TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`).Error; err != nil {
			return fmt.Errorf("create schema_migrations: %w", err)
		}

		var applied []appliedMigration
		if err := tx.Order("version ASC").Find(&applied).Error; err != nil {
			return fmt.Errorf("read applied migrations: %w", err)
		}
		byVersion := make(map[int64]appliedMigration, len(applied))
		for _, item := range applied {
			byVersion[item.Version] = item
		}

		for _, migration := range registry {
			checksum := migrationChecksum(migration)
			if existing, ok := byVersion[migration.Version]; ok {
				if existing.Name != migration.Name || existing.Checksum != checksum {
					return fmt.Errorf("migration %d was modified after application", migration.Version)
				}
				continue
			}
			if err := tx.Exec(migration.SQL).Error; err != nil {
				return fmt.Errorf("apply migration %d %s: %w", migration.Version, migration.Name, err)
			}
			record := appliedMigration{Version: migration.Version, Name: migration.Name, Checksum: checksum, AppliedAt: time.Now().UTC()}
			if err := tx.Create(&record).Error; err != nil {
				return fmt.Errorf("record migration %d: %w", migration.Version, err)
			}
		}
		return nil
	})
}

func validateRegistry(migrations []Migration) error {
	if len(migrations) == 0 {
		return errors.New("migration registry is empty")
	}
	copyOfRegistry := append([]Migration(nil), migrations...)
	sort.Slice(copyOfRegistry, func(i, j int) bool { return copyOfRegistry[i].Version < copyOfRegistry[j].Version })
	for index, migration := range copyOfRegistry {
		if migration.Version <= 0 || migration.Name == "" || migration.SQL == "" {
			return fmt.Errorf("migration at index %d is incomplete", index)
		}
		if index > 0 && copyOfRegistry[index-1].Version == migration.Version {
			return fmt.Errorf("duplicate migration version %d", migration.Version)
		}
		if migration.Version != migrations[index].Version {
			return errors.New("migration registry must be ordered by version")
		}
	}
	return nil
}

func migrationChecksum(migration Migration) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", migration.Version, migration.Name, migration.SQL)))
	return hex.EncodeToString(sum[:])
}
