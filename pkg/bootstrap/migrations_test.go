package bootstrap

import (
	"context"
	"testing"

	"gorm.io/gorm"
)

func TestRunMigrationsValidatesDependencies(t *testing.T) {
	tests := []struct {
		name         string
		ctx          context.Context
		dependencies MigrationDependencies
	}{
		{name: "context", dependencies: MigrationDependencies{}},
		{name: "users database", ctx: context.Background(), dependencies: MigrationDependencies{}},
		{name: "auth store", ctx: context.Background(), dependencies: MigrationDependencies{UsersDB: &gorm.DB{}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := RunMigrations(test.ctx, test.dependencies); err == nil {
				t.Fatal("invalid migration dependencies were accepted")
			}
		})
	}
}
