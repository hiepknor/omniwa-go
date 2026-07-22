package projection_service

import (
	"context"
	"errors"
	"testing"
	"time"

	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"github.com/evolution-foundation/evolution-go/pkg/waquery"
	"go.mau.fi/whatsmeow"
)

type healthRepositoryStub struct {
	instanceID string
	records    []projection_repository.InstanceHealthRecord
	err        error
}

func (s *healthRepositoryStub) ListInstances(_ context.Context, instanceID string) ([]projection_repository.InstanceHealthRecord, error) {
	s.instanceID = instanceID
	return s.records, s.err
}

func TestServerHealthSeparatesConnectionProjectionAndThrottling(t *testing.T) {
	states := NewStateService(newMemoryRepository())
	if err := states.MarkReady("instance-a", "groups", GroupsProjectionSchemaVersion, time.Now()); err != nil {
		t.Fatal(err)
	}
	guard, err := waquery.New(waquery.Settings{RatePerSecond: 1, Burst: 1, MaxWait: time.Second, Cooldown: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	guard.ObserveError("instance-a", whatsmeow.ErrIQRateOverLimit)
	repository := &healthRepositoryStub{records: []projection_repository.InstanceHealthRecord{
		{InstanceID: "instance-a", Connected: true}, {InstanceID: "instance-b", Connected: false},
	}}
	service := NewServerHealthService(repository, states, guard)
	health, err := service.Snapshot(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if health.API.Status != "healthy" || len(health.Instances) != 2 || health.Instances[0].Connection.Status != "connected" ||
		health.Instances[0].Projection.Status != "healthy" || health.Instances[0].Throttling.Status != "throttled" || health.Instances[0].Throttling.RetryAfterSeconds < 1 ||
		health.Instances[1].Connection.Status != "disconnected" || health.Instances[1].Projection.Status != "not_started" || health.Instances[1].Throttling.Observed {
		t.Fatalf("health = %#v", health)
	}
}

func TestServerHealthScopesRepositoryAndPropagatesFailures(t *testing.T) {
	guard, _ := waquery.New(waquery.Settings{RatePerSecond: 1, Burst: 1, MaxWait: time.Second, Cooldown: time.Second})
	repository := &healthRepositoryStub{records: []projection_repository.InstanceHealthRecord{}}
	service := NewServerHealthService(repository, NewStateService(newMemoryRepository()), guard)
	if _, err := service.Snapshot(context.Background(), "instance-a"); err != nil || repository.instanceID != "instance-a" {
		t.Fatalf("scoped snapshot id=%q err=%v", repository.instanceID, err)
	}
	want := errors.New("database unavailable")
	service = NewServerHealthService(&healthRepositoryStub{err: want}, NewStateService(newMemoryRepository()), guard)
	if _, err := service.Snapshot(context.Background(), ""); !errors.Is(err, want) {
		t.Fatalf("repository error = %v", err)
	}
}
