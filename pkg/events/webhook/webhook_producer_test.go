package webhook_producer

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/evolution-foundation/evolution-go/pkg/netguard"
	"github.com/google/uuid"
)

type requesterFunc func(context.Context, string, string, http.Header, []byte) (*netguard.Response, error)

func (f requesterFunc) Do(ctx context.Context, method, rawURL string, header http.Header, body []byte) (*netguard.Response, error) {
	return f(ctx, method, rawURL, header, body)
}

func TestDeliverConfirmedSanitizesPayloadAndSetsStableIdentity(t *testing.T) {
	deliveryID := uuid.NewString()
	producer := NewWebhookProducer(requesterFunc(func(_ context.Context, method, target string, header http.Header, body []byte) (*netguard.Response, error) {
		if method != http.MethodPost || target != "https://configured.example/events" {
			t.Fatalf("request = %s %s", method, target)
		}
		if header.Get("Content-Type") != "application/json" || header.Get("X-Omniwa-Delivery-ID") != deliveryID {
			t.Fatalf("headers = %#v", header)
		}
		if string(body) != `{"value":"ok"}` {
			t.Fatalf("sanitized body = %s", body)
		}
		return &netguard.Response{StatusCode: http.StatusNoContent}, nil
	}))
	if err := producer.DeliverConfirmed(context.Background(), "https://configured.example/events", []byte(`{"value":"ok","token":"secret"}`), deliveryID); err != nil {
		t.Fatal(err)
	}
}

func TestDeliverConfirmedClassifiesResponses(t *testing.T) {
	for _, test := range []struct {
		status    int
		retryable bool
	}{
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadRequest, false},
	} {
		producer := NewWebhookProducer(requesterFunc(func(context.Context, string, string, http.Header, []byte) (*netguard.Response, error) {
			return &netguard.Response{StatusCode: test.status}, nil
		}))
		err := producer.DeliverConfirmed(context.Background(), "https://configured.example/events", []byte(`{}`), uuid.NewString())
		var classified *ConfirmedDeliveryError
		if !errors.As(err, &classified) || classified.StatusCode != test.status || classified.Retryable != test.retryable {
			t.Fatalf("status %d classification = %#v, %v", test.status, classified, err)
		}
	}
}

func TestDeliverConfirmedRejectsInvalidInputAndUnsafeTarget(t *testing.T) {
	producer := NewWebhookProducer(nil)
	for _, test := range []struct {
		name string
		ctx  context.Context
		url  string
		body []byte
		id   string
		code string
	}{
		{"invalid id", context.Background(), "https://example.com", []byte(`{}`), "bad", "invalid_delivery"},
		{"invalid payload", context.Background(), "https://example.com", []byte(`{`), uuid.NewString(), "invalid_payload"},
		{"unsafe target", context.Background(), "https://example.com", []byte(`{}`), uuid.NewString(), "unsafe_target"},
	} {
		err := producer.DeliverConfirmed(test.ctx, test.url, test.body, test.id)
		var classified *ConfirmedDeliveryError
		if !errors.As(err, &classified) || classified.Code != test.code || classified.Retryable {
			t.Fatalf("%s classification = %#v, %v", test.name, classified, err)
		}
	}
}
