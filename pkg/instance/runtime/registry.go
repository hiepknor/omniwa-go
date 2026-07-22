// Package runtime owns process-local WhatsApp client runtimes. It provides
// generation fencing so cleanup from an older connection cannot remove a newer
// connection for the same instance.
package runtime

import (
	"context"
	"errors"
	"sync"

	"go.mau.fi/whatsmeow"
	"golang.org/x/sync/singleflight"
)

// ClientProvider is the narrow dependency used by domain services. Callers can
// read a client, but cannot mutate runtime ownership.
type ClientProvider interface {
	Get(instanceID string) *whatsmeow.Client
}

// Snapshot is an immutable view of an installed runtime. State is owned by the
// registry lifecycle even when T itself is a pointer.
type Snapshot[T any] struct {
	InstanceID string
	Generation uint64
	Client     *whatsmeow.Client
	State      T
	Context    context.Context
}

type entry[T any] struct {
	snapshot Snapshot[T]
	cancel   context.CancelFunc
	cleanup  func()
	once     sync.Once
}

func (entry *entry[T]) stop() {
	if entry == nil {
		return
	}
	entry.once.Do(func() {
		entry.cancel()
		if entry.cleanup != nil {
			entry.cleanup()
		}
	})
}

// Registry serializes runtime installation and removal. Generations increase
// monotonically for the process and are never reused.
type Registry[T any] struct {
	parent     context.Context
	mu         sync.RWMutex
	next       uint64
	entries    map[string]*entry[T]
	closed     bool
	reconnects singleflight.Group
	closeOnce  sync.Once
}

func NewRegistry[T any](parent context.Context) *Registry[T] {
	if parent == nil {
		parent = context.Background()
	}
	registry := &Registry[T]{parent: parent, entries: make(map[string]*entry[T])}
	go func() {
		<-parent.Done()
		registry.Close()
	}()
	return registry
}

// Install atomically publishes a runtime and retires the previous generation
// after releasing the registry lock. The caller must fully initialize state
// before installation.
func (registry *Registry[T]) Install(instanceID string, client *whatsmeow.Client, state T, cleanup func()) (Snapshot[T], error) {
	var zero Snapshot[T]
	if registry == nil || instanceID == "" || client == nil {
		return zero, errors.New("runtime registry, instance identity, and client are required")
	}

	ctx, cancel := context.WithCancel(registry.parent)
	registry.mu.Lock()
	if registry.closed {
		registry.mu.Unlock()
		cancel()
		return zero, errors.New("runtime registry is closed")
	}
	registry.next++
	snapshot := Snapshot[T]{InstanceID: instanceID, Generation: registry.next, Client: client, State: state, Context: ctx}
	replacement := &entry[T]{snapshot: snapshot, cancel: cancel, cleanup: cleanup}
	previous := registry.entries[instanceID]
	registry.entries[instanceID] = replacement
	registry.mu.Unlock()

	previous.stop()
	return snapshot, nil
}

func (registry *Registry[T]) Lookup(instanceID string) (Snapshot[T], bool) {
	var zero Snapshot[T]
	if registry == nil {
		return zero, false
	}
	registry.mu.RLock()
	entry, ok := registry.entries[instanceID]
	if ok {
		zero = entry.snapshot
	}
	registry.mu.RUnlock()
	return zero, ok
}

func (registry *Registry[T]) Get(instanceID string) *whatsmeow.Client {
	snapshot, ok := registry.Lookup(instanceID)
	if !ok {
		return nil
	}
	return snapshot.Client
}

// RemoveIfCurrent retires a runtime only when its generation still owns the
// instance. It is the required cleanup operation for asynchronous owners.
func (registry *Registry[T]) RemoveIfCurrent(instanceID string, generation uint64) bool {
	if registry == nil || instanceID == "" || generation == 0 {
		return false
	}
	registry.mu.Lock()
	current, ok := registry.entries[instanceID]
	if !ok || current.snapshot.Generation != generation {
		registry.mu.Unlock()
		return false
	}
	delete(registry.entries, instanceID)
	registry.mu.Unlock()
	current.stop()
	return true
}

// RemoveCurrent is reserved for explicit administrative lifecycle operations.
// Asynchronous cleanup must use RemoveIfCurrent.
func (registry *Registry[T]) RemoveCurrent(instanceID string) bool {
	if registry == nil || instanceID == "" {
		return false
	}
	registry.mu.Lock()
	current, ok := registry.entries[instanceID]
	if ok {
		delete(registry.entries, instanceID)
	}
	registry.mu.Unlock()
	if ok {
		current.stop()
	}
	return ok
}

// Reconnect coalesces concurrent reconnect attempts for one instance without
// blocking reconnects for other instances.
func (registry *Registry[T]) Reconnect(instanceID string, reconnect func() error) error {
	if registry == nil || instanceID == "" || reconnect == nil {
		return errors.New("runtime registry, instance identity, and reconnect callback are required")
	}
	_, err, _ := registry.reconnects.Do(instanceID, func() (any, error) {
		return nil, reconnect()
	})
	return err
}

func (registry *Registry[T]) Close() {
	if registry == nil {
		return
	}
	registry.closeOnce.Do(func() {
		registry.mu.Lock()
		registry.closed = true
		entries := registry.entries
		registry.entries = make(map[string]*entry[T])
		registry.mu.Unlock()
		for _, entry := range entries {
			entry.stop()
		}
	})
}
