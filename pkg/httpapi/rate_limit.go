// Package httpapi contains shared public HTTP response behavior.
package httpapi

import (
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/evolution-foundation/evolution-go/pkg/outbound"
	"github.com/evolution-foundation/evolution-go/pkg/waquery"
	"github.com/gin-gonic/gin"
)

// WriteRateLimit writes a stable rate-limit contract for known information-query
// and outbound-message limits. It returns whether a response was written so
// handlers can preserve their existing treatment of other errors.
func WriteRateLimit(ctx *gin.Context, err error) bool {
	var rateLimitErr *waquery.RateLimitError
	if errors.As(err, &rateLimitErr) {
		return writeRateLimit(ctx, "rate_limited", rateLimitErr.RetryAfterSeconds())
	}

	retryAfterDuration, ok := outbound.RetryAfter(err)
	if !ok {
		return false
	}
	retryAfter := int(math.Ceil(retryAfterDuration.Seconds()))
	if retryAfter < 1 {
		retryAfter = 1
	}
	return writeRateLimit(ctx, "outbound_rate_limited", retryAfter)
}

func writeRateLimit(ctx *gin.Context, code string, retryAfter int) bool {
	ctx.Header("Retry-After", strconv.Itoa(retryAfter))
	body := gin.H{"error": code, "code": code, "retryAfter": retryAfter}
	if requestID := RequestID(ctx); requestID != "" {
		body["requestId"] = requestID
	}
	ctx.JSON(http.StatusTooManyRequests, body)
	return true
}
