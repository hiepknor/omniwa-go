package projection_service

import (
	"context"
	"errors"
	"math"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"github.com/evolution-foundation/evolution-go/pkg/waquery"
)

type healthInstanceRepository interface {
	ListInstances(context.Context, string) ([]projection_repository.InstanceHealthRecord, error)
}

type queryGuardSnapshotter interface {
	Snapshot(string) (waquery.Snapshot, bool)
}

type ServerHealthService struct {
	repository healthInstanceRepository
	projection StateService
	guard      queryGuardSnapshotter
	now        func() time.Time
}

type HealthDimension struct {
	Status string `json:"status"`
}

type ConnectionHealth struct {
	Status    string `json:"status"`
	Connected bool   `json:"connected"`
}

type ThrottlingHealth struct {
	Status            string     `json:"status"`
	Observed          bool       `json:"observed"`
	CircuitState      string     `json:"circuitState"`
	OpenUntil         *time.Time `json:"openUntil,omitempty"`
	RetryAfterSeconds int64      `json:"retryAfterSeconds,omitempty"`
}

type InstanceHealth struct {
	InstanceID string           `json:"instanceId"`
	Connection ConnectionHealth `json:"connection"`
	Projection ProjectionHealth `json:"projection"`
	Throttling ThrottlingHealth `json:"throttling"`
}

type ServerHealth struct {
	GeneratedAt time.Time        `json:"generatedAt"`
	API         HealthDimension  `json:"api"`
	Instances   []InstanceHealth `json:"instances"`
}

func NewServerHealthService(repository healthInstanceRepository, projection StateService, guard queryGuardSnapshotter) *ServerHealthService {
	return &ServerHealthService{repository: repository, projection: projection, guard: guard, now: time.Now}
}

func (s *ServerHealthService) Snapshot(ctx context.Context, instanceID string) (*ServerHealth, error) {
	if s == nil || s.repository == nil || s.projection == nil || s.guard == nil || s.now == nil || ctx == nil {
		return nil, errors.New("server health service configuration is invalid")
	}
	now := s.now().UTC()
	instances, err := s.repository.ListInstances(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	projectionHealth, err := s.projection.Health(instanceID)
	if err != nil {
		return nil, err
	}
	result := &ServerHealth{GeneratedAt: now, API: HealthDimension{Status: "healthy"}, Instances: make([]InstanceHealth, len(instances))}
	for index, instance := range instances {
		connectionStatus := "disconnected"
		if instance.Connected {
			connectionStatus = "connected"
		}
		result.Instances[index] = InstanceHealth{
			InstanceID: instance.InstanceID,
			Connection: ConnectionHealth{Status: connectionStatus, Connected: instance.Connected},
			Projection: projectionHealthForInstance(projectionHealth.Resources, instance.InstanceID, now),
			Throttling: throttlingHealth(s.guard, instance.InstanceID, now),
		}
	}
	return result, nil
}

func projectionHealthForInstance(resources []ProjectionResourceHealth, instanceID string, now time.Time) ProjectionHealth {
	result := ProjectionHealth{Status: "not_started", GeneratedAt: now, ByStatus: map[string]int{}, Resources: []ProjectionResourceHealth{}}
	for _, resource := range resources {
		if resource.InstanceID != instanceID {
			continue
		}
		result.Resources = append(result.Resources, resource)
		result.Total++
		result.ByStatus[string(resource.SyncStatus)]++
		switch resource.SyncStatus {
		case projection_model.SyncStatusFailed, projection_model.SyncStatusStale:
			result.Status = "degraded"
		case projection_model.SyncStatusSyncing, projection_model.SyncStatusNotStarted:
			if result.Status != "degraded" {
				result.Status = "syncing"
			}
		case projection_model.SyncStatusReady:
			if result.Status == "not_started" {
				result.Status = "healthy"
			}
		}
	}
	return result
}

func throttlingHealth(guard queryGuardSnapshotter, instanceID string, now time.Time) ThrottlingHealth {
	snapshot, observed := guard.Snapshot(instanceID)
	result := ThrottlingHealth{Status: "healthy", Observed: observed, CircuitState: string(waquery.CircuitClosed)}
	if !observed {
		return result
	}
	result.CircuitState = string(snapshot.CircuitState)
	if snapshot.CircuitState == waquery.CircuitOpen || snapshot.CircuitState == waquery.CircuitHalfOpen {
		result.Status = "throttled"
	}
	if snapshot.OpenUntil.After(now) {
		openUntil := snapshot.OpenUntil.UTC()
		result.OpenUntil = &openUntil
		result.RetryAfterSeconds = int64(math.Ceil(openUntil.Sub(now).Seconds()))
		if result.RetryAfterSeconds < 1 {
			result.RetryAfterSeconds = 1
		}
	}
	return result
}
