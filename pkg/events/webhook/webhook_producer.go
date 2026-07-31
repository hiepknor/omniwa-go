package webhook_producer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	eventpayload "github.com/evolution-foundation/evolution-go/pkg/events/payload"
	"github.com/evolution-foundation/evolution-go/pkg/netguard"
	"github.com/google/uuid"
)

// ConfirmedDeliveryError classifies one synchronous webhook attempt without
// exposing the destination URL or response body.
type ConfirmedDeliveryError struct {
	Code       string
	Retryable  bool
	StatusCode int
}

func (e *ConfirmedDeliveryError) Error() string           { return "webhook delivery was not confirmed" }
func (e *ConfirmedDeliveryError) DeliveryCode() string    { return e.Code }
func (e *ConfirmedDeliveryError) DeliveryRetryable() bool { return e.Retryable }

// Producer performs one confirmed attempt. PostgreSQL owns admission, retry
// scheduling, concurrency limits, and shutdown recovery through the outbox.
type Producer struct {
	requester netguard.Requester
}

func NewWebhookProducer(requester netguard.Requester) *Producer {
	return &Producer{requester: requester}
}

func (p *Producer) sendWebhook(ctx context.Context, url string, body []byte, deliveryID string) (error, int) {
	if p.requester == nil {
		return fmt.Errorf("%w: webhook host is not configured", netguard.ErrUnsafeTarget), 0
	}
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	header.Set("X-Omniwa-Delivery-ID", deliveryID)
	resp, err := p.requester.Do(ctx, http.MethodPost, url, header, body)
	if err != nil {
		return err, 0
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("received non-2xx webhook response"), resp.StatusCode
	}
	return nil, resp.StatusCode
}

// DeliverConfirmed succeeds only after the configured target returns 2xx.
// The durable worker owns retry scheduling in PostgreSQL.
func (p *Producer) DeliverConfirmed(ctx context.Context, target string, payload []byte, deliveryID string) error {
	if p == nil || ctx == nil || strings.TrimSpace(target) == "" || uuid.Validate(deliveryID) != nil {
		return &ConfirmedDeliveryError{Code: "invalid_delivery", Retryable: false}
	}
	safePayload, err := eventpayload.SanitizeJSON(payload)
	if err != nil {
		return &ConfirmedDeliveryError{Code: "invalid_payload", Retryable: false}
	}
	err, statusCode := p.sendWebhook(ctx, target, safePayload, deliveryID)
	if err == nil {
		return nil
	}
	return &ConfirmedDeliveryError{Code: errorCode(err), Retryable: isRetryable(ctx, err, statusCode), StatusCode: statusCode}
}

func isRetryable(ctx context.Context, err error, statusCode int) bool {
	if ctx.Err() != nil {
		return false
	}
	if errors.Is(err, netguard.ErrUnsafeTarget) || errors.Is(err, netguard.ErrResponseLarge) ||
		errors.Is(err, context.Canceled) {
		return false
	}
	if statusCode == 0 {
		return true
	}
	return statusCode == http.StatusRequestTimeout || statusCode == http.StatusTooEarly ||
		statusCode == http.StatusTooManyRequests || statusCode >= http.StatusInternalServerError
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, netguard.ErrUnsafeTarget):
		return "unsafe_target"
	case errors.Is(err, netguard.ErrResponseLarge):
		return "response_too_large"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "cancelled"
	default:
		return "delivery_error"
	}
}
