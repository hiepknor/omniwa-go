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
