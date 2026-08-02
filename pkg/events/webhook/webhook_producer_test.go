package webhook_producer

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

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
		if header.Get(SignatureHeader) != "" || header.Get(SignatureTimestampHeader) != "" || header.Get(SignatureKeyIDHeader) != "" {
			t.Fatalf("unsigned compatibility headers = %#v", header)
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

func TestDeliverConfirmedSignsSanitizedPayload(t *testing.T) {
	secret := bytes.Repeat([]byte{7}, sha256.Size)
	deliveryID := uuid.NewString()
	timestamp := "1785657600"
	producer, err := NewSignedWebhookProducer(requesterFunc(func(_ context.Context, _ string, _ string, header http.Header, body []byte) (*netguard.Response, error) {
		if string(body) != `{"value":"ok"}` {
			t.Fatalf("signed body = %s", body)
		}
		if header.Get(SignatureTimestampHeader) != timestamp || header.Get(SignatureKeyIDHeader) != "primary-2026-08" {
			t.Fatalf("signature metadata headers = %#v", header)
		}
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write([]byte(timestamp + "." + deliveryID + "."))
		_, _ = mac.Write(body)
		expected := "v1=" + hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(header.Get(SignatureHeader)), []byte(expected)) {
			t.Fatalf("signature = %q, want %q", header.Get(SignatureHeader), expected)
		}

		tamperedMAC := hmac.New(sha256.New, secret)
		_, _ = tamperedMAC.Write([]byte(timestamp + "." + deliveryID + "."))
		_, _ = tamperedMAC.Write([]byte(`{"value":"tampered"}`))
		tampered := "v1=" + hex.EncodeToString(tamperedMAC.Sum(nil))
		if hmac.Equal([]byte(header.Get(SignatureHeader)), []byte(tampered)) {
			t.Fatal("tampered payload unexpectedly retained the signature")
		}
		return &netguard.Response{StatusCode: http.StatusNoContent}, nil
	}), secret, "primary-2026-08")
	if err != nil {
		t.Fatal(err)
	}
	producer.now = func() time.Time { return time.Unix(1785657600, 0) }
	if err := producer.DeliverConfirmed(context.Background(), "https://configured.example/events", []byte(`{"value":"ok","token":"secret"}`), deliveryID); err != nil {
		t.Fatal(err)
	}
}

func TestNewSignedWebhookProducerValidatesSigningMaterial(t *testing.T) {
	for _, test := range []struct {
		name   string
		secret []byte
		keyID  string
	}{
		{name: "short secret", secret: bytes.Repeat([]byte{1}, sha256.Size-1), keyID: "primary"},
		{name: "missing key id", secret: bytes.Repeat([]byte{1}, sha256.Size)},
		{name: "long key id", secret: bytes.Repeat([]byte{1}, sha256.Size), keyID: strings.Repeat("a", 65)},
		{name: "unsafe key id", secret: bytes.Repeat([]byte{1}, sha256.Size), keyID: "primary header"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewSignedWebhookProducer(nil, test.secret, test.keyID); err == nil {
				t.Fatal("expected signing configuration validation error")
			}
		})
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
