package projection_service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"github.com/google/uuid"
)

var ErrInvalidDurableEventCursor = errors.New("invalid durable event cursor")

const durableEventCursorVersion = 1

type durableEventReadRepository interface {
	List(context.Context, string, string, int, *projection_repository.DurableEventCursor) (*projection_repository.DurableEventPage, error)
}

type DurableEventReader struct {
	repository durableEventReadRepository
	retention  time.Duration
	now        func() time.Time
}

type DurableEventHistoryItem struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	OccurredAt time.Time      `json:"occurredAt"`
	IngestedAt time.Time      `json:"ingestedAt"`
	Summary    map[string]any `json:"summary"`
}

type DurableEventHistoryMeta struct {
	Source           string    `json:"source"`
	NextCursor       string    `json:"nextCursor,omitempty"`
	GeneratedAt      time.Time `json:"generatedAt"`
	RetentionSeconds int64     `json:"retentionSeconds"`
	Backfill         bool      `json:"backfill"`
}

type durableEventCursorEnvelope struct {
	Version    int       `json:"v"`
	Kind       string    `json:"kind"`
	OccurredAt time.Time `json:"occurredAt"`
	ID         string    `json:"id"`
}

func NewDurableEventReader(repository durableEventReadRepository, retention time.Duration) *DurableEventReader {
	return &DurableEventReader{repository: repository, retention: retention, now: time.Now}
}

func (r *DurableEventReader) List(ctx context.Context, instanceID, eventType string, limit int, encodedCursor string) ([]DurableEventHistoryItem, *DurableEventHistoryMeta, error) {
	if r == nil || r.repository == nil || r.now == nil || r.retention <= 0 || ctx == nil {
		return nil, nil, errors.New("durable event reader configuration is invalid")
	}
	cursor, err := decodeDurableEventCursor(encodedCursor)
	if err != nil {
		return nil, nil, err
	}
	page, err := r.repository.List(ctx, instanceID, eventType, limit, cursor)
	if err != nil {
		return nil, nil, err
	}
	items := make([]DurableEventHistoryItem, len(page.Items))
	for index := range page.Items {
		items[index], err = publicDurableEvent(page.Items[index])
		if err != nil {
			return nil, nil, err
		}
	}
	meta := &DurableEventHistoryMeta{
		Source: "projection", GeneratedAt: r.now().UTC(), RetentionSeconds: int64(r.retention / time.Second), Backfill: false,
	}
	if page.NextCursor != nil {
		meta.NextCursor, err = encodeDurableEventCursor(page.NextCursor)
		if err != nil {
			return nil, nil, err
		}
	}
	return items, meta, nil
}

func publicDurableEvent(event projection_model.DurableEvent) (DurableEventHistoryItem, error) {
	summary := make(map[string]any)
	if err := json.Unmarshal(event.Summary, &summary); err != nil {
		return DurableEventHistoryItem{}, errors.New("durable event summary is invalid")
	}
	return DurableEventHistoryItem{
		ID: event.ID, Type: event.Type, OccurredAt: event.OccurredAt.UTC(), IngestedAt: event.IngestedAt.UTC(),
		Summary: summary,
	}, nil
}

func decodeDurableEventCursor(value string) (*projection_repository.DurableEventCursor, error) {
	if value == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, ErrInvalidDurableEventCursor
	}
	var envelope durableEventCursorEnvelope
	if err := json.Unmarshal(decoded, &envelope); err != nil || envelope.Version != durableEventCursorVersion || envelope.Kind != "events" ||
		envelope.OccurredAt.IsZero() || uuid.Validate(envelope.ID) != nil {
		return nil, ErrInvalidDurableEventCursor
	}
	return &projection_repository.DurableEventCursor{OccurredAt: envelope.OccurredAt.UTC(), ID: envelope.ID}, nil
}

func encodeDurableEventCursor(cursor *projection_repository.DurableEventCursor) (string, error) {
	if cursor == nil || cursor.OccurredAt.IsZero() || uuid.Validate(cursor.ID) != nil {
		return "", ErrInvalidDurableEventCursor
	}
	payload, err := json.Marshal(durableEventCursorEnvelope{
		Version: durableEventCursorVersion, Kind: "events", OccurredAt: cursor.OccurredAt.UTC(), ID: cursor.ID,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}
