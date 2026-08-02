package ownership

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeSession struct {
	acquired      bool
	lockErr       error
	activate      Epoch
	activateErr   error
	current       Epoch
	currentErr    error
	released      bool
	unlockErr     error
	closeErr      error
	lockCalls     atomic.Int32
	activateCalls atomic.Int32
	currentCalls  atomic.Int32
	unlockCalls   atomic.Int32
	closeCalls    atomic.Int32
}

func (session *fakeSession) TryLock(context.Context) (bool, error) {
	session.lockCalls.Add(1)
	return session.acquired, session.lockErr
}

func (session *fakeSession) ActivateEpoch(context.Context) (Epoch, error) {
	session.activateCalls.Add(1)
	return session.activate, session.activateErr
}

func (session *fakeSession) CurrentEpoch(context.Context) (Epoch, error) {
	session.currentCalls.Add(1)
	return session.current, session.currentErr
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

func TestActivateIssuesEpochExactlyOnce(t *testing.T) {
	session := &fakeSession{acquired: true, activate: 7, current: 7}
	guard, err := acquireSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	const callers = 16
	var wait sync.WaitGroup
	wait.Add(callers)
	errorsSeen := make(chan error, callers)
	for range callers {
		go func() {
			defer wait.Done()
			epoch, activateErr := guard.Activate(context.Background())
			if activateErr != nil {
				errorsSeen <- activateErr
				return
			}
			if epoch != 7 {
				errorsSeen <- errors.New("unexpected epoch")
			}
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for activateErr := range errorsSeen {
		t.Fatal(activateErr)
	}
	if session.activateCalls.Load() != 1 {
		t.Fatalf("activate calls=%d, want 1", session.activateCalls.Load())
	}
	if epoch, ok := guard.Epoch(); !ok || epoch != 7 {
		t.Fatalf("Epoch()=(%d,%t), want (7,true)", epoch, ok)
	}
}

func TestActivateFailureIsFailClosedAndNotRetried(t *testing.T) {
	wantErr := errors.New("activation unavailable")
	session := &fakeSession{acquired: true, activateErr: wantErr}
	guard, err := acquireSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := guard.Activate(context.Background()); !errors.Is(err, wantErr) {
			t.Fatalf("Activate() error=%v, want %v", err, wantErr)
		}
	}
	if session.activateCalls.Load() != 1 {
		t.Fatalf("activate calls=%d, want 1", session.activateCalls.Load())
	}
	if _, ok := guard.Epoch(); ok {
		t.Fatal("failed activation exposed an epoch")
	}
}

func TestMonitorReportsOwnershipConnectionLoss(t *testing.T) {
	session := &fakeSession{acquired: true, activate: 3, currentErr: errors.New("connection lost")}
	guard, err := acquireSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	err = guard.Monitor(context.Background(), time.Millisecond)
	if err == nil || session.currentCalls.Load() == 0 {
		t.Fatalf("Monitor() error=%v current_calls=%d", err, session.currentCalls.Load())
	}
}

func TestMonitorRequiresActivatedEpoch(t *testing.T) {
	guard, err := acquireSession(context.Background(), &fakeSession{acquired: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.Monitor(context.Background(), time.Second); !errors.Is(err, ErrEpochNotActivated) {
		t.Fatalf("Monitor() error=%v, want ErrEpochNotActivated", err)
	}
}

func TestMonitorRejectsNilContext(t *testing.T) {
	guard, err := acquireSession(context.Background(), &fakeSession{acquired: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.Monitor(nil, time.Second); err == nil {
		t.Fatal("Monitor() accepted a nil context")
	}
}

func TestValidateRejectsStaleEpoch(t *testing.T) {
	session := &fakeSession{acquired: true, activate: 3, current: 4}
	guard, err := acquireSession(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := guard.Validate(context.Background()); !errors.Is(err, ErrEpochStale) {
		t.Fatalf("Validate() error=%v, want ErrEpochStale", err)
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
