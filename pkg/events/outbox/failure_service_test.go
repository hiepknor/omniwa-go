package outbox

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type failureRepositoryStub struct {
	page      *DeadLetterPage
	operation ReplayOperation
}

func (r *failureRepositoryStub) ListDeadLetters(context.Context, string, Transport, int, *DeadLetterCursor) (*DeadLetterPage, error) {
	return r.page, nil
}
func (r *failureRepositoryStub) ReplayDeadLetter(_ context.Context, operation ReplayOperation) error {
	r.operation = operation
	return nil
}

func TestFailureServiceListNeverSerializesDeliverySecrets(t *testing.T) {
	when := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	repository := &failureRepositoryStub{page: &DeadLetterPage{Items: []DeadLetterRecord{{
		ID: uuid.NewString(), InstanceID: uuid.NewString(), Transport: TransportWebhook, Destination: DestinationInstance,
		AttemptCount: 12, MaxAttempts: 12, DeadLetteredAt: &when, CreatedAt: when,
	}}}}
	service := NewFailureService(repository)
	page, err := service.List(context.Background(), "", "", 50, "")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	value := string(encoded)
	for _, forbidden := range []string{"payload", "routingKey", "durableEventId", "claimToken", "leaseUntil"} {
		if strings.Contains(value, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, value)
		}
	}
}

func TestFailureServiceReplayHashesCredential(t *testing.T) {
	repository := &failureRepositoryStub{}
	service := NewFailureService(repository)
	when := time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return when }
	id := uuid.NewString()
	result, err := service.Replay(context.Background(), id, "transport recovered", "admin-secret", "request-replay-0002")
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != id || result.Status != StatusPending || repository.operation.ActorReferenceHash == "" ||
		strings.Contains(repository.operation.ActorReferenceHash, "admin-secret") {
		t.Fatalf("result=%#v operation=%#v", result, repository.operation)
	}
}

func TestDeadLetterCursorIsBoundToFilterScope(t *testing.T) {
	cursor := &DeadLetterCursor{DeadLetteredAt: time.Now().UTC(), ID: uuid.NewString()}
	encoded, err := encodeDeadLetterCursor(deadLetterScope("", TransportWebhook), cursor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeDeadLetterCursor(encoded, deadLetterScope("", TransportRabbitMQ)); err != ErrInvalidDeadLetterCursor {
		t.Fatalf("error=%v", err)
	}
}
