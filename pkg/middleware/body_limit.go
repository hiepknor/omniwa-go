package auth_middleware

import (
	"net/http"
	"strings"

	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	"github.com/gin-gonic/gin"
)

const (
	defaultJSONBodyLimit = int64(2 * 1024 * 1024)
	mediaJSONBodyLimit   = int64(48 * 1024 * 1024)
	multipartBodyLimit   = int64(64 * 1024 * 1024)
)

func BodyLimit() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if ctx.Request.Body == nil {
			ctx.Next()
			return
		}
		limit := requestBodyLimit(ctx.Request)
		if ctx.Request.ContentLength > limit {
			httpapi.WriteError(ctx, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large")
			ctx.Abort()
			return
		}
		ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, limit)
		ctx.Next()
	}
}

func requestBodyLimit(request *http.Request) int64 {
	if strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "multipart/form-data") {
		return multipartBodyLimit
	}
	path := request.URL.Path
	if strings.HasPrefix(path, "/send/") || path == "/group/photo" || path == "/user/profilePicture" {
		return mediaJSONBodyLimit
	}
	return defaultJSONBodyLimit
}
