package projection_service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
)

type durableEventReadStub struct {
	instanceID string
	eventType  string
	limit      int
	cursor     *projection_repository.DurableEventCursor
	page       *projection_repository.DurableEventPage
	err        error
}

func (s *durableEventReadStub) List(_ context.Context, instanceID, eventType string, limit int, cursor *projection_repository.DurableEventCursor) (*projection_repository.DurableEventPage, error) {
	s.instanceID, s.eventType, s.limit, s.cursor = instanceID, eventType, limit, cursor
	return s.page, s.err
}

func TestDurableEventReaderReturnsSafePageAndRoundTripsOpaqueCursor(t *testing.T) {
	firstAt := time.Unix(300, 0).UTC()
	secondAt := time.Unix(200, 0).UTC()
	nextID := "00000000-0000-0000-0000-000000000002"
	repository := &durableEventReadStub{page: &projection_repository.DurableEventPage{
		Items: []projection_model.DurableEvent{
			{ID: "00000000-0000-0000-0000-000000000003", Type: "Message", OccurredAt: firstAt, IngestedAt: firstAt.Add(time.Second), Summary: json.RawMessage(`{"messageId":"safe"}`)},
			{ID: nextID, Type: "Receipt", OccurredAt: secondAt, IngestedAt: secondAt.Add(time.Second), Summary: json.RawMessage(`{"count":2}`)},
		},
		NextCursor: &projection_repository.DurableEventCursor{OccurredAt: secondAt, ID: nextID},
	}}
	reader := NewDurableEventReader(repository, 30*24*time.Hour)
	reader.now = func() time.Time { return time.Unix(400, 0).UTC() }
	items, meta, err := reader.List(context.Background(), "instance-a", "Message", 2, "")
	if err != nil || len(items) != 2 || items[0].ID == "" || items[0].Summary["messageId"] != "safe" {
		t.Fatalf("List() items=%#v meta=%#v err=%v", items, meta, err)
	}
	if repository.instanceID != "instance-a" || repository.eventType != "Message" || repository.limit != 2 || repository.cursor != nil {
		t.Fatalf("repository call = %#v", repository)
	}
	if meta.Source != "projection" || meta.NextCursor == "" || meta.RetentionSeconds != int64(30*24*time.Hour/time.Second) || meta.Backfill || !meta.GeneratedAt.Equal(time.Unix(400, 0)) {
		t.Fatalf("meta = %#v", meta)
	}

	secondRepository := &durableEventReadStub{page: &projection_repository.DurableEventPage{Items: []projection_model.DurableEvent{}}}
	secondReader := NewDurableEventReader(secondRepository, time.Hour)
	if _, _, err := secondReader.List(context.Background(), "instance-a", "", 10, meta.NextCursor); err != nil {
		t.Fatal(err)
	}
	if secondRepository.cursor == nil || secondRepository.cursor.ID != nextID || !secondRepository.cursor.OccurredAt.Equal(secondAt) {
		t.Fatalf("decoded cursor = %#v", secondRepository.cursor)
	}
}

func TestDurableEventReaderRejectsForgedCursorAndPropagatesRepositoryError(t *testing.T) {
	reader := NewDurableEventReader(&durableEventReadStub{}, time.Hour)
	for _, cursor := range []string{"not-base64!", base64.RawURLEncoding.EncodeToString([]byte(`{"v":2,"kind":"events"}`))} {
		if _, _, err := reader.List(context.Background(), "instance-a", "", 10, cursor); !errors.Is(err, ErrInvalidDurableEventCursor) {
			t.Fatalf("cursor %q error = %v", cursor, err)
		}
	}
	want := errors.New("database unavailable")
	reader = NewDurableEventReader(&durableEventReadStub{err: want}, time.Hour)
	if _, _, err := reader.List(context.Background(), "instance-a", "", 10, ""); !errors.Is(err, want) {
		t.Fatalf("repository error = %v", err)
	}
	reader = NewDurableEventReader(&durableEventReadStub{page: &projection_repository.DurableEventPage{Items: []projection_model.DurableEvent{{Summary: json.RawMessage(`[]`)}}}}, time.Hour)
	if _, _, err := reader.List(context.Background(), "instance-a", "", 10, ""); err == nil {
		t.Fatal("non-object durable summary was exposed")
	}
}
