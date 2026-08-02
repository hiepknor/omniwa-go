package auth_middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const authFailureOverflowKey = "overflow"

type authFailureLimiterSettings struct {
	MaxFailures int
	Window      time.Duration
	BlockFor    time.Duration
	EntryTTL    time.Duration
	MaxEntries  int
}

type authFailureEntry struct {
	failures     []time.Time
	blockedUntil time.Time
	lastSeen     time.Time
}

type authFailureLimiter struct {
	settings    authFailureLimiterSettings
	now         func() time.Time
	entries     map[string]*authFailureEntry
	lastCleanup time.Time
	mu          sync.Mutex
}

func newAuthFailureLimiter(settings authFailureLimiterSettings) *authFailureLimiter {
	return &authFailureLimiter{
		settings: settings,
		now:      time.Now,
		entries:  make(map[string]*authFailureEntry),
	}
}

func defaultAuthFailureLimiter() *authFailureLimiter {
	return newAuthFailureLimiter(authFailureLimiterSettings{
		MaxFailures: 10,
		Window:      time.Minute,
		BlockFor:    time.Minute,
		EntryTTL:    15 * time.Minute,
		MaxEntries:  10_000,
	})
}

func (l *authFailureLimiter) retryAfter(source string) (time.Duration, bool) {
	if l == nil {
		return 0, false
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanup(now)
	entry := l.entries[source]
	if entry == nil && len(l.entries) >= l.settings.MaxEntries {
		entry = l.entries[authFailureOverflowKey]
	}
	if entry == nil || !now.Before(entry.blockedUntil) {
		return 0, false
	}
	entry.lastSeen = now
	return entry.blockedUntil.Sub(now), true
}

func (l *authFailureLimiter) recordFailure(source string) (time.Duration, bool) {
	if l == nil {
		return 0, false
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanup(now)

	key := source
	entry := l.entries[key]
	if entry == nil {
		if len(l.entries) >= l.settings.MaxEntries {
			key = authFailureOverflowKey
			entry = l.entries[key]
			if entry == nil {
				for existingKey := range l.entries {
					delete(l.entries, existingKey)
					break
				}
			}
		}
		if entry == nil {
			entry = &authFailureEntry{}
			l.entries[key] = entry
		}
	}
	entry.lastSeen = now
	if now.Before(entry.blockedUntil) {
		return entry.blockedUntil.Sub(now), true
	}

	cutoff := now.Add(-l.settings.Window)
	kept := entry.failures[:0]
	for _, failure := range entry.failures {
		if failure.After(cutoff) {
			kept = append(kept, failure)
		}
	}
	entry.failures = append(kept, now)
	if len(entry.failures) < l.settings.MaxFailures {
		return 0, false
	}
	entry.failures = nil
	entry.blockedUntil = now.Add(l.settings.BlockFor)
	return l.settings.BlockFor, true
}

func (l *authFailureLimiter) cleanup(now time.Time) {
	if len(l.entries) < l.settings.MaxEntries && !l.lastCleanup.IsZero() && now.Sub(l.lastCleanup) < l.settings.EntryTTL {
		return
	}
	cutoff := now.Add(-l.settings.EntryTTL)
	for key, entry := range l.entries {
		if entry.lastSeen.Before(cutoff) && !now.Before(entry.blockedUntil) {
			delete(l.entries, key)
		}
	}
	l.lastCleanup = now
}

func authSourceKey(ctx *gin.Context) string {
	clientIP := ctx.ClientIP()
	peerIP := ctx.Request.RemoteAddr
	if host, _, err := net.SplitHostPort(peerIP); err == nil {
		peerIP = host
	}
	sum := sha256.Sum256([]byte(clientIP + "\x00" + peerIP))
	return hex.EncodeToString(sum[:16])
}

func writeAuthRateLimit(ctx *gin.Context, retryAfter time.Duration) {
	seconds := int(math.Ceil(retryAfter.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	ctx.Header("Retry-After", strconv.Itoa(seconds))
	ctx.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
		"error":      "too many authentication attempts",
		"code":       "auth_rate_limited",
		"retryAfter": seconds,
	})
}
