package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	label_model "github.com/evolution-foundation/evolution-go/pkg/label/model"
	message_model "github.com/evolution-foundation/evolution-go/pkg/message/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"gorm.io/gorm"
)

type CoreMigration func(*gorm.DB) error

type MigrationDependencies struct {
	UsersDB     *gorm.DB
	AuthDialect string
	AuthAddress string
	MigrateCore CoreMigration
}

// RunMigrations upgrades all database schemas owned by this process. Callers
// must hold the application ownership lock for the complete operation.
func RunMigrations(ctx context.Context, dependencies MigrationDependencies) error {
	if ctx == nil {
		return errors.New("migration context is required")
	}
	if dependencies.UsersDB == nil {
		return errors.New("users migration database is required")
	}
	if dependencies.AuthDialect == "" || dependencies.AuthAddress == "" {
		return errors.New("auth migration dialect and address are required")
	}
	if dependencies.AuthDialect != "postgres" && dependencies.AuthDialect != "sqlite" {
		return errors.New("auth migration dialect must be postgres or sqlite")
	}
	usersDB := dependencies.UsersDB.WithContext(ctx)
	if err := usersDB.AutoMigrate(&instance_model.Instance{}, &message_model.Message{}, &label_model.Label{}); err != nil {
		return fmt.Errorf("migrate base users schema: %w", err)
	}
	if err := migrations.Run(usersDB); err != nil {
		return fmt.Errorf("run versioned users migrations: %w", err)
	}

	authDB, err := sql.Open(dependencies.AuthDialect, dependencies.AuthAddress)
	if err != nil {
		return fmt.Errorf("open WhatsApp auth migration database: %w", err)
	}
	authStore := sqlstore.NewWithDB(authDB, dependencies.AuthDialect, nil)
	if err := authStore.Upgrade(ctx); err != nil {
		_ = authStore.Close()
		return fmt.Errorf("migrate WhatsApp auth schema: %w", err)
	}
	if dependencies.AuthDialect == "postgres" {
		if err := migrations.RunAuth(ctx, authDB); err != nil {
			_ = authStore.Close()
			return fmt.Errorf("run application auth migrations: %w", err)
		}
	}
	if err := authStore.Close(); err != nil {
		return fmt.Errorf("close WhatsApp auth migration store: %w", err)
	}
	if dependencies.MigrateCore != nil {
		if err := dependencies.MigrateCore(usersDB); err != nil {
			return fmt.Errorf("migrate licensed runtime schema: %w", err)
		}
	}
	return nil
}
