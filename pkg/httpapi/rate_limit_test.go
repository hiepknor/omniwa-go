package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/outbound"
	"github.com/evolution-foundation/evolution-go/pkg/waquery"
	"github.com/gin-gonic/gin"
)

func TestWriteRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	err := errors.Join(errors.New("query failed"), &waquery.RateLimitError{
		RetryAfter: 1500 * time.Millisecond,
		Source:     waquery.LimitSourceUpstream,
	})
	if !WriteRateLimit(ctx, err) {
		t.Fatal("expected rate-limit response")
	}
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want 2", got)
	}
	if got := recorder.Body.String(); got != "{\"code\":\"rate_limited\",\"error\":\"rate_limited\",\"retryAfter\":2}" {
		t.Fatalf("body = %s", got)
	}
}

func TestWriteRateLimitOutbound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	err := errors.Join(errors.New("send failed"), &outbound.RateLimitError{RetryAfter: 2500 * time.Millisecond})
	if !WriteRateLimit(ctx, err) {
		t.Fatal("expected outbound rate-limit response")
	}
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "3" {
		t.Fatalf("Retry-After = %q, want 3", got)
	}
	if got := recorder.Body.String(); got != "{\"code\":\"outbound_rate_limited\",\"error\":\"outbound_rate_limited\",\"retryAfter\":3}" {
		t.Fatalf("body = %s", got)
	}
}

func TestWriteRateLimitIgnoresOtherErrors(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	if WriteRateLimit(ctx, errors.New("boom")) {
		t.Fatal("unexpected response for non-rate-limit error")
	}
	if recorder.Code != http.StatusOK || recorder.Body.Len() != 0 {
		t.Fatalf("response was modified: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}
