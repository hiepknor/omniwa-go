package projection_service

import (
	"context"
	"errors"
	"testing"
	"time"

	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
)

type overviewRepositoryStub struct {
	instanceID string
	start      time.Time
	end        time.Time
	counts     *projection_repository.OverviewCounts
	err        error
}

func (s *overviewRepositoryStub) Snapshot(_ context.Context, instanceID string, start, end time.Time) (*projection_repository.OverviewCounts, error) {
	s.instanceID, s.start, s.end = instanceID, start, end
	return s.counts, s.err
}

func TestOverviewServiceDefinesWindowScopeAndPersistedCounts(t *testing.T) {
	repository := &overviewRepositoryStub{counts: &projection_repository.OverviewCounts{
		InstancesTotal: 1, InstancesConnected: 1, Groups: 2, Contacts: 3, Chats: 4,
		Messages: 5, MessagesIncoming: 3, MessagesOutgoing: 2, Events: 6,
	}}
	service := NewOverviewService(repository)
	service.now = func() time.Time { return time.Unix(10_000, 0).UTC() }
	overview, err := service.Snapshot(context.Background(), "instance-a", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if repository.instanceID != "instance-a" || !repository.end.Equal(service.now()) || !repository.start.Equal(service.now().Add(-24*time.Hour)) {
		t.Fatalf("repository window = %q %v..%v", repository.instanceID, repository.start, repository.end)
	}
	if overview.Scope.Type != "instance" || overview.Scope.InstanceID != "instance-a" || overview.Window.DurationSeconds != 86400 ||
		overview.Instances.Disconnected != 0 || overview.Projections.Messages != 5 || overview.Messages.Incoming != 3 || overview.Messages.Outgoing != 2 || overview.Projections.Events != 6 {
		t.Fatalf("overview = %#v", overview)
	}
}

func TestOverviewServiceRejectsUnsafeWindowAndPropagatesRepositoryError(t *testing.T) {
	service := NewOverviewService(&overviewRepositoryStub{counts: &projection_repository.OverviewCounts{}})
	for _, window := range []time.Duration{0, MaximumOverviewWindow + time.Second} {
		if _, err := service.Snapshot(context.Background(), "", window); err == nil {
			t.Fatalf("window %v was accepted", window)
		}
	}
	want := errors.New("database unavailable")
	service = NewOverviewService(&overviewRepositoryStub{err: want})
	if _, err := service.Snapshot(context.Background(), "", time.Hour); !errors.Is(err, want) {
		t.Fatalf("repository error = %v", err)
	}
}
