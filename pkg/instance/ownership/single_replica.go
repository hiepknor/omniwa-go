package ownership

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	lockSQL          = `SELECT pg_try_advisory_lock(hashtextextended(current_database() || ':omniwa-go:runtime-owner', 0))`
	activateEpochSQL = `WITH side_effect_fence AS MATERIALIZED (
    SELECT pg_advisory_xact_lock(hashtextextended(current_database() || ':omniwa-go:external-side-effects', 0))
)
INSERT INTO runtime_ownership_epochs (scope, epoch, activated_at)
SELECT 'application', 1, NOW() FROM side_effect_fence
ON CONFLICT (scope) DO UPDATE
SET epoch = runtime_ownership_epochs.epoch + 1,
    activated_at = EXCLUDED.activated_at
RETURNING epoch`
	currentEpochSQL = `SELECT epoch FROM runtime_ownership_epochs WHERE scope = 'application'`
	unlockSQL       = `SELECT pg_advisory_unlock(hashtextextended(current_database() || ':omniwa-go:runtime-owner', 0))`
)

type Epoch int64

var (
	ErrAlreadyRunning    = errors.New("another OmniWA GO application replica already owns this users database")
	ErrEpochNotActivated = errors.New("application ownership epoch is not activated")
	ErrEpochStale        = errors.New("application ownership epoch is stale")
)

type lockSession interface {
	TryLock(context.Context) (bool, error)
	ActivateEpoch(context.Context) (Epoch, error)
	CurrentEpoch(context.Context) (Epoch, error)
	Unlock(context.Context) (bool, error)
	Close() error
}

type postgresSession struct {
	conn *sql.Conn
}

func (session *postgresSession) TryLock(ctx context.Context) (bool, error) {
	var acquired bool
	err := session.conn.QueryRowContext(ctx, lockSQL).Scan(&acquired)
	return acquired, err
}

func (session *postgresSession) ActivateEpoch(ctx context.Context) (Epoch, error) {
	var epoch Epoch
	err := session.conn.QueryRowContext(ctx, activateEpochSQL).Scan(&epoch)
	return epoch, err
}

func (session *postgresSession) CurrentEpoch(ctx context.Context) (Epoch, error) {
	var epoch Epoch
	err := session.conn.QueryRowContext(ctx, currentEpochSQL).Scan(&epoch)
	return epoch, err
}

func (session *postgresSession) Unlock(ctx context.Context) (bool, error) {
	var released bool
	err := session.conn.QueryRowContext(ctx, unlockSQL).Scan(&released)
	return released, err
}

func (session *postgresSession) Close() error {
	return session.conn.Close()
}

// Guard holds a database-scoped PostgreSQL advisory lock for the process
// lifetime. It is a containment boundary, not a distributed instance lease.
type Guard struct {
	session      lockSession
	activateOnce sync.Once
	activationMu sync.RWMutex
	activateErr  error
	epoch        Epoch
	closeOnce    sync.Once
	closeErr     error
}

// Activate issues a new durable, monotonically increasing epoch on the same
// PostgreSQL session that owns the process advisory lock. It is a single
// fail-closed attempt: callers must discard the guard after any activation
// error rather than retrying with ambiguous ownership state.
func (guard *Guard) Activate(ctx context.Context) (Epoch, error) {
	if guard == nil || guard.session == nil || ctx == nil {
		return 0, errors.New("ownership guard and activation context are required")
	}
	guard.activateOnce.Do(func() {
		epoch, err := guard.session.ActivateEpoch(ctx)
		if err != nil {
			err = fmt.Errorf("activate application ownership epoch: %w", err)
		} else if epoch <= 0 {
			err = errors.New("activate application ownership epoch: database returned an invalid epoch")
			epoch = 0
		}
		guard.activationMu.Lock()
		guard.epoch = epoch
		guard.activateErr = err
		guard.activationMu.Unlock()
	})
	// Activation state is immutable after activateOnce completes. The lock also
	// makes Epoch safe during the one startup activation attempt.
	guard.activationMu.RLock()
	defer guard.activationMu.RUnlock()
	return guard.epoch, guard.activateErr
}

func (guard *Guard) Epoch() (Epoch, bool) {
	if guard == nil {
		return 0, false
	}
	guard.activationMu.RLock()
	defer guard.activationMu.RUnlock()
	if guard.epoch <= 0 || guard.activateErr != nil {
		return 0, false
	}
	return guard.epoch, true
}

// Validate checks the durable epoch through the dedicated ownership session.
// A connection failure or a changed epoch fails closed.
func (guard *Guard) Validate(ctx context.Context) error {
	if guard == nil || guard.session == nil || ctx == nil {
		return errors.New("ownership guard and validation context are required")
	}
	epoch, ok := guard.Epoch()
	if !ok {
		return ErrEpochNotActivated
	}
	current, err := guard.session.CurrentEpoch(ctx)
	if err != nil {
		return fmt.Errorf("validate application ownership epoch: %w", err)
	}
	if current != epoch {
		return fmt.Errorf("%w: active=%d current=%d", ErrEpochStale, epoch, current)
	}
	return nil
}

func Acquire(ctx context.Context, db *sql.DB) (*Guard, error) {
	if db == nil {
		return nil, errors.New("users database is required for single-replica ownership")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("reserve ownership connection: %w", err)
	}
	return acquireSession(ctx, &postgresSession{conn: conn})
}

func acquireSession(ctx context.Context, session lockSession) (*Guard, error) {
	if session == nil {
		return nil, errors.New("ownership session is required")
	}
	acquired, err := session.TryLock(ctx)
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("acquire application ownership lock: %w", err)
	}
	if !acquired {
		_ = session.Close()
		return nil, ErrAlreadyRunning
	}
	return &Guard{session: session}, nil
}

// Monitor verifies the durable epoch through the dedicated PostgreSQL session.
// A session-level advisory lock cannot survive connection loss, so a query
// failure or epoch mismatch must stop the application before work continues.
func (guard *Guard) Monitor(ctx context.Context, interval time.Duration) error {
	if guard == nil || guard.session == nil || ctx == nil {
		return errors.New("ownership guard is not initialized")
	}
	if interval <= 0 {
		return errors.New("ownership monitor interval must be positive")
	}
	if err := ctx.Err(); err != nil {
		return nil
	}
	if _, ok := guard.Epoch(); !ok {
		return ErrEpochNotActivated
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, interval)
			err := guard.Validate(pingCtx)
			cancel()
			if err != nil {
				return fmt.Errorf("application ownership validation failed: %w", err)
			}
		}
	}
}

func (guard *Guard) Close(ctx context.Context) error {
	if guard == nil || guard.session == nil {
		return nil
	}
	guard.closeOnce.Do(func() {
		released, unlockErr := guard.session.Unlock(ctx)
		closeErr := guard.session.Close()
		switch {
		case unlockErr != nil:
			guard.closeErr = fmt.Errorf("release application ownership lock: %w", unlockErr)
		case !released:
			guard.closeErr = errors.New("application ownership lock was not held by this session")
		case closeErr != nil:
			guard.closeErr = fmt.Errorf("close application ownership session: %w", closeErr)
		}
	})
	return guard.closeErr
}
