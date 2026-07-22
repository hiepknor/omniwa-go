package projection_service

import (
	"context"
	"errors"
	"time"

	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
)

const (
	DefaultOverviewWindow = 24 * time.Hour
	MaximumOverviewWindow = 30 * 24 * time.Hour
)

type overviewReadRepository interface {
	Snapshot(context.Context, string, time.Time, time.Time) (*projection_repository.OverviewCounts, error)
}

type OverviewService struct {
	repository overviewReadRepository
	now        func() time.Time
}

type OverviewWindow struct {
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
	DurationSeconds int64     `json:"durationSeconds"`
}

type OverviewScope struct {
	Type       string `json:"type"`
	InstanceID string `json:"instanceId,omitempty"`
}

type OverviewInstances struct {
	Total        int64 `json:"total"`
	Connected    int64 `json:"connected"`
	Disconnected int64 `json:"disconnected"`
}

type OverviewMessages struct {
	Total    int64 `json:"total"`
	Incoming int64 `json:"incoming"`
	Outgoing int64 `json:"outgoing"`
}

type OverviewProjectionCounts struct {
	Groups   int64 `json:"groups"`
	Contacts int64 `json:"contacts"`
	Chats    int64 `json:"chats"`
	Messages int64 `json:"messages"`
	Events   int64 `json:"events"`
}

type Overview struct {
	GeneratedAt time.Time                `json:"generatedAt"`
	Window      OverviewWindow           `json:"window"`
	Scope       OverviewScope            `json:"scope"`
	Instances   OverviewInstances        `json:"instances"`
	Projections OverviewProjectionCounts `json:"projections"`
	Messages    OverviewMessages         `json:"messages"`
}

func NewOverviewService(repository overviewReadRepository) *OverviewService {
	return &OverviewService{repository: repository, now: time.Now}
}

func (s *OverviewService) Snapshot(ctx context.Context, instanceID string, window time.Duration) (*Overview, error) {
	if s == nil || s.repository == nil || s.now == nil || ctx == nil || window <= 0 || window > MaximumOverviewWindow {
		return nil, errors.New("overview service parameters are invalid")
	}
	end := s.now().UTC()
	start := end.Add(-window)
	counts, err := s.repository.Snapshot(ctx, instanceID, start, end)
	if err != nil {
		return nil, err
	}
	scope := OverviewScope{Type: "server"}
	if instanceID != "" {
		scope = OverviewScope{Type: "instance", InstanceID: instanceID}
	}
	return &Overview{
		GeneratedAt: end, Window: OverviewWindow{Start: start, End: end, DurationSeconds: int64(window / time.Second)}, Scope: scope,
		Instances:   OverviewInstances{Total: counts.InstancesTotal, Connected: counts.InstancesConnected, Disconnected: counts.InstancesTotal - counts.InstancesConnected},
		Projections: OverviewProjectionCounts{Groups: counts.Groups, Contacts: counts.Contacts, Chats: counts.Chats, Messages: counts.Messages, Events: counts.Events},
		Messages:    OverviewMessages{Total: counts.Messages, Incoming: counts.MessagesIncoming, Outgoing: counts.MessagesOutgoing},
	}, nil
}
