package ownership

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	_ "github.com/lib/pq"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresGuardExcludesSecondProcess(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	first, err := Acquire(ctx, firstDB)
	if err != nil {
		t.Fatal(err)
	}
	firstEpoch, err := first.Activate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if firstEpoch <= 0 {
		t.Fatalf("first epoch=%d, want positive", firstEpoch)
	}
	if err := first.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(ctx, secondDB)
	if !errors.Is(err, ErrAlreadyRunning) || second != nil {
		t.Fatalf("second guard=%v error=%v, want ErrAlreadyRunning", second, err)
	}
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	second, err = Acquire(ctx, secondDB)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	secondEpoch, err := second.Activate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if secondEpoch != firstEpoch+1 {
		t.Fatalf("second epoch=%d, want %d", secondEpoch, firstEpoch+1)
	}
	if err := second.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresSideEffectFenceSerializesEpochActivation(t *testing.T) {
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
	ownerDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer ownerDB.Close()
	operationDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer operationDB.Close()
	nextOwnerDB, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer nextOwnerDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := Acquire(ctx, ownerDB)
	if err != nil {
		t.Fatal(err)
	}
	firstEpoch, err := first.Activate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fencer, err := NewSideEffectFencer(operationDB, firstEpoch)
	if err != nil {
		t.Fatal(err)
	}
	wantProviderErr := errors.New("provider rejected operation")
	if err := fencer.Do(ctx, func(context.Context) error { return wantProviderErr }); !errors.Is(err, wantProviderErr) {
		t.Fatalf("provider error=%v, want %v", err, wantProviderErr)
	}

	operationEntered := make(chan struct{})
	releaseOperation := make(chan struct{})
	operationDone := make(chan error, 1)
	go func() {
		operationDone <- fencer.Do(ctx, func(context.Context) error {
			close(operationEntered)
			<-releaseOperation
			return nil
		})
	}()
	select {
	case <-operationEntered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(ctx, nextOwnerDB)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		defer closeCancel()
		_ = second.Close(closeCtx)
	}()

	type activationResult struct {
		epoch Epoch
		err   error
	}
	activationDone := make(chan activationResult, 1)
	go func() {
		epoch, activateErr := second.Activate(ctx)
		activationDone <- activationResult{epoch: epoch, err: activateErr}
	}()
	for {
		var waitingLocks int
		err := nextOwnerDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&waitingLocks)
		if err != nil {
			t.Fatal(err)
		}
		if waitingLocks > 0 {
			break
		}
		select {
		case result := <-activationDone:
			t.Fatalf("epoch activation did not wait for admitted side effect: epoch=%d error=%v", result.epoch, result.err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case result := <-activationDone:
		t.Fatalf("epoch activation completed while its exclusive fence was waiting: epoch=%d error=%v", result.epoch, result.err)
	default:
	}
	close(releaseOperation)
	if err := <-operationDone; err != nil {
		t.Fatal(err)
	}
	var activated activationResult
	select {
	case activated = <-activationDone:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if activated.err != nil {
		t.Fatal(activated.err)
	}
	if activated.epoch != firstEpoch+1 {
		t.Fatalf("activated epoch=%d, want %d", activated.epoch, firstEpoch+1)
	}

	callbackCalled := false
	err = fencer.Do(ctx, func(context.Context) error {
		callbackCalled = true
		return nil
	})
	if !errors.Is(err, ErrEpochStale) {
		t.Fatalf("stale fencer error=%v, want ErrEpochStale", err)
	}
	if callbackCalled {
		t.Fatal("stale fencer invoked the provider callback")
	}
}
