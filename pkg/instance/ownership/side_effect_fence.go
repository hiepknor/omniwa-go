package ownership

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const sideEffectSharedLockSQL = `SELECT pg_advisory_xact_lock_shared(hashtextextended(current_database() || ':omniwa-go:external-side-effects', 0))`

var ErrSideEffectOutcomeUnknown = errors.New("external side-effect outcome is unknown")

// SideEffectFencer carries one activated process epoch across a bounded
// external side effect. The PostgreSQL transaction holds a shared advisory
// fence until the callback returns, so activation of a newer epoch must wait
// for admitted older work to finish. Callers must pass the callback's context
// to every provider request and must not launch detached work from the callback.
type SideEffectFencer struct {
	db    *sql.DB
	epoch Epoch
}

func NewSideEffectFencer(db *sql.DB, epoch Epoch) (*SideEffectFencer, error) {
	if db == nil || epoch <= 0 {
		return nil, errors.New("users database and a positive ownership epoch are required")
	}
	return &SideEffectFencer{db: db, epoch: epoch}, nil
}

func (fencer *SideEffectFencer) Do(ctx context.Context, operation func(context.Context) error) error {
	if fencer == nil || fencer.db == nil || fencer.epoch <= 0 || ctx == nil || operation == nil {
		return errors.New("side-effect fencer, context, and operation are required")
	}
	tx, err := fencer.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin side-effect fence: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, sideEffectSharedLockSQL); err != nil {
		return fmt.Errorf("acquire shared side-effect fence: %w", err)
	}
	var current Epoch
	if err := tx.QueryRowContext(ctx, currentEpochSQL).Scan(&current); err != nil {
		return fmt.Errorf("read side-effect ownership epoch: %w", err)
	}
	if current != fencer.epoch {
		return fmt.Errorf("%w: active=%d current=%d", ErrEpochStale, fencer.epoch, current)
	}
	if err := operation(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: release ownership fence after provider callback: %v", ErrSideEffectOutcomeUnknown, err)
	}
	return nil
}
