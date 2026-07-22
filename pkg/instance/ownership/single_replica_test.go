package ownership

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type fakeSession struct {
	acquired    bool
	lockErr     error
	pingErr     error
	released    bool
	unlockErr   error
	closeErr    error
	lockCalls   atomic.Int32
	pingCalls   atomic.Int32
	unlockCalls atomic.Int32
	closeCalls  atomic.Int32
}

func (session *fakeSession) TryLock(context.Context) (bool, error) {
	session.lockCalls.Add(1)
	return session.acquired, session.lockErr
}

func (session *fakeSession) Ping(context.Context) error {
	session.pingCalls.Add(1)
	return session.pingErr
}

func (session *fakeSession) Unlock(context.Context) (bool, error) {
	session.unlockCalls.Add(1)
	return session.released, session.unlockErr
}

func (session *fakeSession) Close() error {
	session.closeCalls.Add(1)
	return session.closeErr
}

func TestAcquireRejectsSecondReplicaAndClosesSession(t *testing.T) {
	session := &fakeSession{}
	guard, err := acquireSession(context.Background(), session)
	if !errors.Is(err, ErrAlreadyRunning) || guard != nil {
		t.Fatalf("guard=%v error=%v, want ErrAlreadyRunning", guard, err)
	}
	if session.closeCalls.Load() != 1 {
		t.Fatalf("close calls=%d, want 1", session.closeCalls.Load())
	}
}

func TestMonitorReportsOwnershipConnectionLoss(t *testing.T) {
	session := &fakeSession{acquired: true, pingErr: errors.New("connection lost")}
	guard, err := acquireSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	err = guard.Monitor(context.Background(), time.Millisecond)
	if err == nil || session.pingCalls.Load() == 0 {
		t.Fatalf("Monitor() error=%v ping_calls=%d", err, session.pingCalls.Load())
	}
}

func TestCloseReleasesAndClosesExactlyOnce(t *testing.T) {
	session := &fakeSession{acquired: true, released: true}
	guard, err := acquireSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := guard.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if session.unlockCalls.Load() != 1 || session.closeCalls.Load() != 1 {
		t.Fatalf("unlock_calls=%d close_calls=%d, want 1 each", session.unlockCalls.Load(), session.closeCalls.Load())
	}
}

func TestMonitorStopsCleanlyWithContext(t *testing.T) {
	session := &fakeSession{acquired: true}
	guard, err := acquireSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := guard.Monitor(ctx, time.Second); err != nil {
		t.Fatal(err)
	}
}
