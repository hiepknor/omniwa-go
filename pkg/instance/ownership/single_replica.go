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
	lockSQL   = `SELECT pg_try_advisory_lock(hashtextextended(current_database() || ':omniwa-go:runtime-owner', 0))`
	unlockSQL = `SELECT pg_advisory_unlock(hashtextextended(current_database() || ':omniwa-go:runtime-owner', 0))`
)

var ErrAlreadyRunning = errors.New("another OmniWA GO application replica already owns this users database")

type lockSession interface {
	TryLock(context.Context) (bool, error)
	Ping(context.Context) error
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

func (session *postgresSession) Ping(ctx context.Context) error {
	return session.conn.PingContext(ctx)
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
	session   lockSession
	closeOnce sync.Once
	closeErr  error
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

// Monitor verifies that the dedicated PostgreSQL session is still alive. A
// session-level advisory lock cannot survive connection loss, so any ping
// failure must stop the application before another replica can take ownership.
func (guard *Guard) Monitor(ctx context.Context, interval time.Duration) error {
	if guard == nil || guard.session == nil {
		return errors.New("ownership guard is not initialized")
	}
	if interval <= 0 {
		return errors.New("ownership monitor interval must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, interval)
			err := guard.session.Ping(pingCtx)
			cancel()
			if err != nil {
				return fmt.Errorf("application ownership lock session lost: %w", err)
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
