// Package bootstrap owns process composition and background lifecycle wiring.
// Domain packages remain responsible for worker behavior.
package bootstrap

import (
	"context"
	"errors"
	"sync"

	instance_runtime "github.com/evolution-foundation/evolution-go/pkg/instance/runtime"
	whatsmeow_service "github.com/evolution-foundation/evolution-go/pkg/whatsmeow/service"
)

type Work func(context.Context) error
type ErrorReporter func(name string, err error)

type Supervisor struct {
	ctx      context.Context
	reporter ErrorReporter
	mu       sync.Mutex
	sealed   bool
	wait     sync.WaitGroup
	stopOnce sync.Once
	done     chan struct{}
}

func NewSupervisor(ctx context.Context, reporter ErrorReporter) *Supervisor {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Supervisor{ctx: ctx, reporter: reporter, done: make(chan struct{})}
}

// Start registers work before shutdown waiting begins. This serialization
// prevents the WaitGroup Add/Wait race that ad-hoc composition can introduce.
func (s *Supervisor) Start(name string, work Work) error {
	if s == nil || name == "" || work == nil {
		return errors.New("supervisor, worker name, and work are required")
	}
	s.mu.Lock()
	if s.sealed {
		s.mu.Unlock()
		return errors.New("background worker registration is closed")
	}
	s.wait.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wait.Done()
		if err := work(s.ctx); err != nil && s.reporter != nil {
			s.reporter(name, err)
		}
	}()
	return nil
}

// Wait seals registration and waits for every registered worker. It is safe to
// call from multiple shutdown observers.
func (s *Supervisor) Wait() {
	if s == nil {
		return
	}
	<-s.Stopped()
}

// Stopped seals registration and returns a channel closed after all workers
// exit, allowing bounded shutdown selection without ad-hoc waiter goroutines.
func (s *Supervisor) Stopped() <-chan struct{} {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.sealed = true
		s.mu.Unlock()
		go func() {
			s.wait.Wait()
			close(s.done)
		}()
	})
	return s.done
}

func NewInstanceRuntime(ctx context.Context) *instance_runtime.Registry[*whatsmeow_service.MyClient] {
	return instance_runtime.NewRegistry[*whatsmeow_service.MyClient](ctx)
}
