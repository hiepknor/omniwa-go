package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/waquery"
	"github.com/gin-gonic/gin"
	"go.mau.fi/whatsmeow"
)

func TestUpstreamRateLimitOpensCircuitAndReturnsPublicContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	guard, err := waquery.New(waquery.Settings{
		RatePerSecond: 100,
		Burst:         10,
		MaxWait:       time.Second,
		Cooldown:      90 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	var upstreamCalls atomic.Int32
	router := gin.New()
	router.GET("/query", func(ctx *gin.Context) {
		_, queryErr := waquery.Do(ctx.Request.Context(), guard, "instance-a", waquery.OperationGroupInfo, "group-a", func(context.Context) (string, error) {
			upstreamCalls.Add(1)
			return "", whatsmeow.ErrIQRateOverLimit
		})
		if queryErr != nil && WriteRateLimit(ctx, queryErr) {
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "unexpected"})
	})

	for requestNumber := 1; requestNumber <= 2; requestNumber++ {
		request := httptest.NewRequest(http.MethodGet, "/query", nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)

		if response.Code != http.StatusTooManyRequests {
			t.Fatalf("request %d status = %d, want 429", requestNumber, response.Code)
		}
		if got := response.Header().Get("Retry-After"); got != "90" {
			t.Fatalf("request %d Retry-After = %q, want 90", requestNumber, got)
		}
		if got := response.Body.String(); got != "{\"code\":\"rate_limited\",\"error\":\"rate_limited\",\"retryAfter\":90}" {
			t.Fatalf("request %d body = %s", requestNumber, got)
		}
	}

	if got := upstreamCalls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 during circuit cooldown", got)
	}
}
