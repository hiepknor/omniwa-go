package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

type OriginPolicy struct {
	allowed map[string]struct{}
}

func NewOriginPolicy(origins []string) (*OriginPolicy, error) {
	policy := &OriginPolicy{allowed: make(map[string]struct{}, len(origins))}
	for _, value := range origins {
		normalized, err := normalizeOrigin(value)
		if err != nil {
			return nil, err
		}
		policy.allowed[normalized] = struct{}{}
	}
	return policy, nil
}

func (p *OriginPolicy) Allows(request *http.Request) bool {
	if request == nil {
		return false
	}
	raw := strings.TrimSpace(request.Header.Get("Origin"))
	if raw == "" {
		return true
	}
	origin, err := normalizeOrigin(raw)
	if err != nil {
		return false
	}
	parsed, _ := url.Parse(origin)
	if strings.EqualFold(parsed.Host, request.Host) {
		return true
	}
	if p == nil {
		return false
	}
	_, allowed := p.allowed[origin]
	return allowed
}

func (p *OriginPolicy) Middleware() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		origin := strings.TrimSpace(ctx.GetHeader("Origin"))
		if origin != "" && !p.Allows(ctx.Request) {
			WriteError(ctx, http.StatusForbidden, "origin_not_allowed", "request origin is not allowed")
			ctx.Abort()
			return
		}
		if origin != "" {
			ctx.Header("Access-Control-Allow-Origin", origin)
			ctx.Writer.Header().Add("Vary", "Origin")
			ctx.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			ctx.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, X-Request-ID, Idempotency-Key, Authorization, Accept, Cache-Control, X-Requested-With, apikey, ApiKey")
			ctx.Header("Access-Control-Expose-Headers", "Content-Length, Retry-After, X-Request-ID")
			ctx.Header("Access-Control-Max-Age", "600")
		}
		if ctx.Request.Method == http.MethodOptions {
			ctx.AbortWithStatus(http.StatusNoContent)
			return
		}
		ctx.Next()
	}
}

func normalizeOrigin(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("HTTP allowed origins must be exact http(s) origins without paths, queries, fragments, or credentials")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("HTTP allowed origins must use http or https")
	}
	return scheme + "://" + strings.ToLower(parsed.Host), nil
}
