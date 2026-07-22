package httpapi

import (
	"regexp"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	RequestIDHeader = "X-Request-ID"
	requestIDKey    = "httpapi.request_id"
)

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{16,64}$`)

// RequestIdentity attaches a bounded correlation identity to every request and
// response. A caller-provided identity is accepted only when it is safe to put
// in headers and logs; otherwise a cryptographically random identity is used.
func RequestIdentity() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		requestID := ctx.GetHeader(RequestIDHeader)
		if !validRequestID.MatchString(requestID) {
			requestID = newRequestID()
		}
		ctx.Set(requestIDKey, requestID)
		ctx.Header(RequestIDHeader, requestID)
		ctx.Next()
	}
}

func RequestID(ctx *gin.Context) string {
	value, _ := ctx.Get(requestIDKey)
	requestID, _ := value.(string)
	return requestID
}

func newRequestID() string {
	return uuid.NewString()
}
