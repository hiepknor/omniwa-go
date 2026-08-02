package runtime_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	instance_ownership "github.com/evolution-foundation/evolution-go/pkg/instance/ownership"
	instance_runtime "github.com/evolution-foundation/evolution-go/pkg/instance/runtime"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	_ "github.com/lib/pq"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestProviderCommandsRejectStaleRuntimeEpoch(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	gormDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := gormDB.AutoMigrate(&instance_model.Instance{}); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Run(gormDB); err != nil {
		t.Fatal(err)
	}
	firstDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer firstDB.Close()
	secondDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer secondDB.Close()
	commandDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer commandDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := instance_ownership.Acquire(ctx, firstDB)
	if err != nil {
		t.Fatal(err)
	}
	epoch, err := first.Activate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fencer, err := instance_ownership.NewSideEffectFencer(commandDB, epoch)
	if err != nil {
		t.Fatal(err)
	}
	registry := instance_runtime.NewRegistry[struct{}](ctx, fencer)
	defer registry.Close()
	firstCalls := 0
	if err := registry.Do(ctx, func(context.Context) error { firstCalls++; return nil }); err != nil {
		t.Fatal(err)
	}
	if firstCalls != 1 {
		t.Fatalf("current command calls=%d, want 1", firstCalls)
	}
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := instance_ownership.Acquire(ctx, secondDB)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close(context.Background()) }()
	if _, err := second.Activate(ctx); err != nil {
		t.Fatal(err)
	}
	staleCalls := 0
	err = registry.Do(ctx, func(context.Context) error { staleCalls++; return nil })
	if !errors.Is(err, instance_ownership.ErrEpochStale) {
		t.Fatalf("stale command error=%v, want ErrEpochStale", err)
	}
	if staleCalls != 0 {
		t.Fatalf("stale command calls=%d, want 0", staleCalls)
	}
}
