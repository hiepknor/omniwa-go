package ownership

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	_ "github.com/lib/pq"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const splitBrainDrillRepetitions = 25

type splitBrainActivationResult struct {
	epoch Epoch
	err   error
}

func TestPostgresOwnershipFenceRepeatedSplitBrainDrill(t *testing.T) {
	dsn := prepareSplitBrainDrillDatabase(t)
	ownerDB := openSplitBrainDrillDB(t, dsn)
	successorDB := openSplitBrainDrillDB(t, dsn)
	commandDB := openSplitBrainDrillDB(t, dsn)
	observerDB := openSplitBrainDrillDB(t, dsn)

	drillStarted := time.Now()
	var maximumDrain time.Duration
	for iteration := 1; iteration <= splitBrainDrillRepetitions; iteration++ {
		drainDuration := runPostgresSplitBrainTransition(t, ownerDB, successorDB, commandDB, observerDB, iteration)
		if drainDuration > maximumDrain {
			maximumDrain = drainDuration
		}
	}

	t.Logf("split-brain ownership fence drill passed transitions=%d total_duration=%s maximum_callback_drain=%s",
		splitBrainDrillRepetitions, time.Since(drillStarted), maximumDrain)
}

func runPostgresSplitBrainTransition(t *testing.T, ownerDB, successorDB, commandDB, observerDB *sql.DB, iteration int) time.Duration {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	releaseCallback := make(chan struct{})
	var releaseOnce sync.Once
	var owner, successor *Guard
	defer func() {
		cancel()
		releaseOnce.Do(func() { close(releaseCallback) })
		if successor != nil {
			closeSplitBrainDrillGuard(t, successor)
		}
		if owner != nil {
			closeSplitBrainDrillGuard(t, owner)
		}
	}()

	var err error
	owner, err = Acquire(ctx, ownerDB)
	if err != nil {
		t.Fatalf("iteration %d acquire owner: %v", iteration, err)
	}
	oldEpoch, err := owner.Activate(ctx)
	if err != nil {
		t.Fatalf("iteration %d activate owner: %v", iteration, err)
	}
	oldFencer, err := NewSideEffectFencer(commandDB, oldEpoch)
	if err != nil {
		t.Fatalf("iteration %d create old fencer: %v", iteration, err)
	}

	callbackEntered := make(chan struct{})
	callbackDone := make(chan error, 1)
	go func() {
		callbackDone <- oldFencer.Do(ctx, func(context.Context) error {
			close(callbackEntered)
			select {
			case <-releaseCallback:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	waitForSplitBrainSignal(t, ctx, callbackEntered, iteration, "old callback admission")

	if err := owner.Close(ctx); err != nil {
		t.Fatalf("iteration %d release old owner: %v", iteration, err)
	}
	successor, err = Acquire(ctx, successorDB)
	if err != nil {
		t.Fatalf("iteration %d acquire successor: %v", iteration, err)
	}
	waitingBefore := advisoryWaiterCount(t, ctx, observerDB)
	activationDone := make(chan splitBrainActivationResult, 1)
	go func() {
		epoch, activateErr := successor.Activate(ctx)
		activationDone <- splitBrainActivationResult{epoch: epoch, err: activateErr}
	}()
	waitForAdditionalAdvisoryWaiter(t, ctx, observerDB, waitingBefore, activationDone, iteration)

	select {
	case result := <-activationDone:
		t.Fatalf("iteration %d successor activated before old callback drained: epoch=%d error=%v", iteration, result.epoch, result.err)
	default:
	}
	drainStarted := time.Now()
	releaseOnce.Do(func() { close(releaseCallback) })
	if err := <-callbackDone; err != nil {
		t.Fatalf("iteration %d drain old callback: %v", iteration, err)
	}
	var activated splitBrainActivationResult
	select {
	case activated = <-activationDone:
	case <-ctx.Done():
		t.Fatalf("iteration %d wait for successor activation: %v", iteration, ctx.Err())
	}
	if activated.err != nil || activated.epoch != oldEpoch+1 {
		t.Fatalf("iteration %d successor activation epoch=%d error=%v, want epoch=%d", iteration, activated.epoch, activated.err, oldEpoch+1)
	}

	staleCallbackCalled := false
	err = oldFencer.Do(ctx, func(context.Context) error {
		staleCallbackCalled = true
		return nil
	})
	if !errors.Is(err, ErrEpochStale) || staleCallbackCalled {
		t.Fatalf("iteration %d stale command error=%v callback_called=%t", iteration, err, staleCallbackCalled)
	}
	newFencer, err := NewSideEffectFencer(commandDB, activated.epoch)
	if err != nil {
		t.Fatalf("iteration %d create successor fencer: %v", iteration, err)
	}
	newCallbackCalls := 0
	if err := newFencer.Do(ctx, func(context.Context) error { newCallbackCalls++; return nil }); err != nil || newCallbackCalls != 1 {
		t.Fatalf("iteration %d successor command error=%v callback_calls=%d", iteration, err, newCallbackCalls)
	}
	if err := successor.Close(ctx); err != nil {
		t.Fatalf("iteration %d release successor: %v", iteration, err)
	}
	return time.Since(drainStarted)
}

func TestPostgresSideEffectFenceReportsUnknownOutcomeAfterConnectionLoss(t *testing.T) {
	dsn := prepareSplitBrainDrillDatabase(t)
	ownerDB := openSplitBrainDrillDB(t, dsn)
	commandDB := openSplitBrainDrillDB(t, dsn)
	observerDB := openSplitBrainDrillDB(t, dsn)
	commandDB.SetMaxOpenConns(1)
	commandDB.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	owner, err := Acquire(ctx, ownerDB)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSplitBrainDrillGuard(t, owner)
	epoch, err := owner.Activate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	applicationName := fmt.Sprintf("omniwa_fence_drill_%d", time.Now().UnixNano())
	if _, err := commandDB.ExecContext(ctx, `SELECT set_config('application_name', $1, false)`, applicationName); err != nil {
		t.Fatal(err)
	}
	fencer, err := NewSideEffectFencer(commandDB, epoch)
	if err != nil {
		t.Fatal(err)
	}

	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	commandDone := make(chan error, 1)
	var callbackCalls atomic.Int32
	go func() {
		commandDone <- fencer.Do(ctx, func(context.Context) error {
			callbackCalls.Add(1)
			close(callbackEntered)
			select {
			case <-releaseCallback:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	waitForSplitBrainSignal(t, ctx, callbackEntered, 1, "ambiguous callback admission")

	var backendPID int
	if err := observerDB.QueryRowContext(ctx, `
		SELECT pid FROM pg_stat_activity
		WHERE datname = current_database() AND application_name = $1 AND state = 'idle in transaction'
		ORDER BY backend_start DESC LIMIT 1`, applicationName).Scan(&backendPID); err != nil {
		t.Fatal(err)
	}
	var terminated bool
	if err := observerDB.QueryRowContext(ctx, `SELECT pg_terminate_backend($1)`, backendPID).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("terminate fenced command backend pid=%d terminated=%t error=%v", backendPID, terminated, err)
	}
	close(releaseCallback)
	select {
	case err = <-commandDone:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !errors.Is(err, ErrSideEffectOutcomeUnknown) {
		t.Fatalf("connection-loss command error=%v, want ErrSideEffectOutcomeUnknown", err)
	}
	if callbackCalls.Load() != 1 {
		t.Fatalf("connection-loss callback calls=%d, want 1", callbackCalls.Load())
	}
	t.Log("fenced command connection loss classified outcome_unknown=true callback_calls=1")
}

func TestPostgresSideEffectFenceDrainsBoundedPoolSaturation(t *testing.T) {
	dsn := prepareSplitBrainDrillDatabase(t)
	ownerDB := openSplitBrainDrillDB(t, dsn)
	commandDB := openSplitBrainDrillDB(t, dsn)
	commandDB.SetMaxOpenConns(2)
	commandDB.SetMaxIdleConns(2)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	owner, err := Acquire(ctx, ownerDB)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSplitBrainDrillGuard(t, owner)
	epoch, err := owner.Activate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fencer, err := NewSideEffectFencer(commandDB, epoch)
	if err != nil {
		t.Fatal(err)
	}

	const commands = 8
	entered := make(chan struct{}, commands)
	release := make(chan struct{})
	errorsByCommand := make(chan error, commands)
	var callbackCalls atomic.Int32
	var wait sync.WaitGroup
	wait.Add(commands)
	for range commands {
		go func() {
			defer wait.Done()
			errorsByCommand <- fencer.Do(ctx, func(context.Context) error {
				callbackCalls.Add(1)
				entered <- struct{}{}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}()
	}
	for index := 0; index < 2; index++ {
		waitForSplitBrainSignal(t, ctx, entered, index+1, "saturated command admission")
	}
	waitForPoolWaiters(t, ctx, commandDB, commands-2)
	stats := commandDB.Stats()
	if stats.MaxOpenConnections != 2 || stats.InUse != 2 || stats.OpenConnections > 2 {
		t.Fatalf("unexpected saturated pool stats: %+v", stats)
	}
	close(release)
	wait.Wait()
	close(errorsByCommand)
	for commandErr := range errorsByCommand {
		if commandErr != nil {
			t.Fatal(commandErr)
		}
	}
	if callbackCalls.Load() != commands {
		t.Fatalf("drained callback calls=%d, want %d", callbackCalls.Load(), commands)
	}
	finalStats := commandDB.Stats()
	if finalStats.InUse != 0 || finalStats.WaitCount < commands-2 {
		t.Fatalf("pool did not drain or record bounded waiters: %+v", finalStats)
	}
	t.Logf("bounded pool saturation max_open=%d saturated_in_use=%d wait_count=%d final_in_use=%d",
		stats.MaxOpenConnections, stats.InUse, finalStats.WaitCount, finalStats.InUse)
}

func prepareSplitBrainDrillDatabase(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	gormDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := gormDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := gormDB.AutoMigrate(&instance_model.Instance{}); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Run(gormDB); err != nil {
		t.Fatal(err)
	}
	return dsn
}

func openSplitBrainDrillDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func closeSplitBrainDrillGuard(t *testing.T, guard *Guard) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := guard.Close(ctx); err != nil {
		t.Errorf("close ownership drill guard: %v", err)
	}
}

func waitForSplitBrainSignal(t *testing.T, ctx context.Context, signal <-chan struct{}, iteration int, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatalf("iteration %d wait for %s: %v", iteration, name, ctx.Err())
	}
}

func advisoryWaiterCount(t *testing.T, ctx context.Context, db *sql.DB) int {
	t.Helper()
	var waiting int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&waiting); err != nil {
		t.Fatal(err)
	}
	return waiting
}

func waitForAdditionalAdvisoryWaiter(t *testing.T, ctx context.Context, db *sql.DB, baseline int, activationDone <-chan splitBrainActivationResult, iteration int) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if advisoryWaiterCount(t, ctx, db) > baseline {
			return
		}
		select {
		case result := <-activationDone:
			t.Fatalf("iteration %d activation did not wait for old callback: epoch=%d error=%v", iteration, result.epoch, result.err)
		case <-ctx.Done():
			t.Fatalf("iteration %d wait for blocked activation: %v", iteration, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForPoolWaiters(t *testing.T, ctx context.Context, db *sql.DB, minimum int64) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if db.Stats().WaitCount >= minimum {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for bounded pool saturation: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}
