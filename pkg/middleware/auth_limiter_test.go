package auth_middleware

import (
	"testing"
	"time"
)

func TestAuthFailureLimiterBlocksAndRecovers(t *testing.T) {
	now := time.Unix(100, 0)
	limiter := newAuthFailureLimiter(authFailureLimiterSettings{
		MaxFailures: 2,
		Window:      time.Minute,
		BlockFor:    30 * time.Second,
		EntryTTL:    time.Minute,
		MaxEntries:  10,
	})
	limiter.now = func() time.Time { return now }

	if retry, limited := limiter.recordFailure("source"); limited || retry != 0 {
		t.Fatalf("first failure unexpectedly limited: retry=%s", retry)
	}
	if retry, limited := limiter.recordFailure("source"); !limited || retry != 30*time.Second {
		t.Fatalf("second failure should block: limited=%t retry=%s", limited, retry)
	}
	if retry, limited := limiter.retryAfter("source"); !limited || retry != 30*time.Second {
		t.Fatalf("blocked source should be rejected: limited=%t retry=%s", limited, retry)
	}

	now = now.Add(31 * time.Second)
	if retry, limited := limiter.retryAfter("source"); limited || retry != 0 {
		t.Fatalf("expired block should recover: limited=%t retry=%s", limited, retry)
	}
}

func TestAuthFailureLimiterUsesBoundedState(t *testing.T) {
	limiter := newAuthFailureLimiter(authFailureLimiterSettings{
		MaxFailures: 2,
		Window:      time.Minute,
		BlockFor:    time.Minute,
		EntryTTL:    time.Hour,
		MaxEntries:  2,
	})
	limiter.recordFailure("source-a")
	limiter.recordFailure("source-b")
	limiter.recordFailure("source-c")
	if len(limiter.entries) > limiter.settings.MaxEntries {
		t.Fatalf("limiter state exceeded bound: entries=%d max=%d", len(limiter.entries), limiter.settings.MaxEntries)
	}
}
