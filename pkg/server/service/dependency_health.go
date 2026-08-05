package server_service

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

type DependencyName string

const (
	DependencyUsersDatabase       DependencyName = "users_database"
	DependencyExternalEventOutbox DependencyName = "external_event_outbox"
	DependencyRabbitMQ            DependencyName = "rabbitmq"
	DependencyLegacyMedia         DependencyName = "legacy_media"
	DependencyMediaAssets         DependencyName = "media_assets"
	DependencyCampaignMedia       DependencyName = "campaign_media"
)

type DependencyStatus string

const (
	DependencyUnknown     DependencyStatus = "unknown"
	DependencyHealthy     DependencyStatus = "healthy"
	DependencyUnavailable DependencyStatus = "unavailable"
)

type DependencyHealth struct {
	Name          DependencyName   `json:"name"`
	Status        DependencyStatus `json:"status"`
	CheckedAt     *time.Time       `json:"checkedAt,omitempty"`
	LastSuccessAt *time.Time       `json:"lastSuccessAt,omitempty"`
	ErrorCode     string           `json:"errorCode,omitempty"`
	stableHealthy bool
	successes     int
	failures      int
}

type DependencyHealthObserver interface {
	ObserveDependencyHealth(DependencyName, DependencyStatus, time.Time)
}

type noopDependencyHealthObserver struct{}

func (noopDependencyHealthObserver) ObserveDependencyHealth(DependencyName, DependencyStatus, time.Time) {
}

type DependencyHealthRegistry struct {
	mu       sync.RWMutex
	health   map[DependencyName]DependencyHealth
	observer DependencyHealthObserver
	now      func() time.Time
}

func NewDependencyHealthRegistry(observer DependencyHealthObserver, names ...DependencyName) (*DependencyHealthRegistry, error) {
	if observer == nil {
		observer = noopDependencyHealthObserver{}
	}
	registry := &DependencyHealthRegistry{health: make(map[DependencyName]DependencyHealth, len(names)), observer: observer, now: time.Now}
	for _, name := range names {
		if !ValidDependencyName(name) {
			return nil, errors.New("dependency health name is invalid")
		}
		if _, exists := registry.health[name]; exists {
			return nil, errors.New("dependency health name is duplicated")
		}
		registry.health[name] = DependencyHealth{Name: name, Status: DependencyUnknown}
		observer.ObserveDependencyHealth(name, DependencyUnknown, time.Time{})
	}
	return registry, nil
}

func (r *DependencyHealthRegistry) Observe(name DependencyName, err error, timedOut bool) error {
	if r == nil || r.now == nil || !ValidDependencyName(name) {
		return errors.New("dependency health observation is invalid")
	}
	now := r.now().UTC()
	r.mu.Lock()
	current, exists := r.health[name]
	if !exists {
		r.mu.Unlock()
		return errors.New("dependency health observation is not registered")
	}
	current.CheckedAt = timePointer(now)
	current.ErrorCode = ""
	if err == nil {
		current.Status = DependencyHealthy
		current.LastSuccessAt = timePointer(now)
		current.successes++
		current.failures = 0
		if current.successes >= 2 {
			current.stableHealthy = true
		}
	} else {
		current.Status = DependencyUnavailable
		current.failures++
		current.successes = 0
		if current.failures >= 3 {
			current.stableHealthy = false
		}
		current.ErrorCode = "probe_failed"
		if timedOut {
			current.ErrorCode = "probe_timeout"
		}
	}
	r.health[name] = current
	r.mu.Unlock()
	r.observer.ObserveDependencyHealth(name, current.Status, now)
	return nil
}

func (r *DependencyHealthRegistry) Snapshot() []DependencyHealth {
	if r == nil {
		return []DependencyHealth{}
	}
	r.mu.RLock()
	result := make([]DependencyHealth, 0, len(r.health))
	for _, health := range r.health {
		result = append(result, cloneDependencyHealth(health))
	}
	r.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func ValidDependencyName(name DependencyName) bool {
	switch name {
	case DependencyUsersDatabase, DependencyExternalEventOutbox, DependencyRabbitMQ, DependencyLegacyMedia, DependencyMediaAssets, DependencyCampaignMedia:
		return true
	default:
		return false
	}
}

func ValidDependencyStatus(status DependencyStatus) bool {
	return status == DependencyUnknown || status == DependencyHealthy || status == DependencyUnavailable
}

type DependencyProbe func(context.Context) error

const (
	DefaultDependencyProbeInterval = 15 * time.Second
	DefaultDependencyProbeTimeout  = 5 * time.Second
)

type DependencyProbeWorker struct {
	name     DependencyName
	probe    DependencyProbe
	registry *DependencyHealthRegistry
	interval time.Duration
	timeout  time.Duration
}

func NewDependencyProbeWorker(name DependencyName, probe DependencyProbe, registry *DependencyHealthRegistry, interval, timeout time.Duration) (*DependencyProbeWorker, error) {
	if !ValidDependencyName(name) || probe == nil || registry == nil || interval <= 0 || timeout <= 0 || timeout >= interval || interval > 10*time.Minute || timeout > time.Minute {
		return nil, errors.New("dependency probe worker configuration is invalid")
	}
	return &DependencyProbeWorker{name: name, probe: probe, registry: registry, interval: interval, timeout: timeout}, nil
}

func (w *DependencyProbeWorker) Run(ctx context.Context) error {
	if w == nil || ctx == nil {
		return errors.New("dependency probe worker and context are required")
	}
	if ctx.Err() != nil {
		return nil
	}
	w.check(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			w.check(ctx)
		}
	}
}

func (w *DependencyProbeWorker) check(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, w.timeout)
	err := w.probe(ctx)
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	if timedOut && err == nil {
		err = context.DeadlineExceeded
	}
	cancel()
	if parent.Err() != nil {
		return
	}
	_ = w.registry.Observe(w.name, err, timedOut)
}

func cloneDependencyHealth(value DependencyHealth) DependencyHealth {
	value.CheckedAt = cloneTimePointer(value.CheckedAt)
	value.LastSuccessAt = cloneTimePointer(value.LastSuccessAt)
	return value
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func timePointer(value time.Time) *time.Time { return &value }
