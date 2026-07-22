// Package httpapi contains shared public HTTP response behavior.
package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/evolution-foundation/evolution-go/pkg/waquery"
	"github.com/gin-gonic/gin"
)

// WriteRateLimit writes the stable information-query rate-limit contract when
// err contains a waquery.RateLimitError. It returns whether a response was
// written so handlers can preserve their existing treatment of other errors.
func WriteRateLimit(ctx *gin.Context, err error) bool {
	var rateLimitErr *waquery.RateLimitError
	if !errors.As(err, &rateLimitErr) {
		return false
	}

	retryAfter := rateLimitErr.RetryAfterSeconds()
	ctx.Header("Retry-After", strconv.Itoa(retryAfter))
	ctx.JSON(http.StatusTooManyRequests, gin.H{
		"error":      "rate_limited",
		"code":       "rate_limited",
		"retryAfter": retryAfter,
	})
	return true
}
