package waquery

import (
	"fmt"
	"math"
	"time"
)

type LimitSource string

const (
	LimitSourceLocal       LimitSource = "local_limit"
	LimitSourceCircuitOpen LimitSource = "circuit_open"
	LimitSourceUpstream    LimitSource = "upstream_429"
)

// RateLimitError is returned when an information query cannot safely reach
// WhatsApp. Callers can use errors.As to map it to the public HTTP contract.
type RateLimitError struct {
	RetryAfter time.Duration
	Source     LimitSource
	Cause      error
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("WhatsApp information query rate limited (%s)", e.Source)
}

func (e *RateLimitError) Unwrap() error {
	return e.Cause
}

// RetryAfterSeconds returns a positive delta-seconds value suitable for the
// Retry-After header and the additive JSON retryAfter field.
func (e *RateLimitError) RetryAfterSeconds() int {
	seconds := int(math.Ceil(e.RetryAfter.Seconds()))
	if seconds < 1 {
		return 1
	}
	return seconds
}
