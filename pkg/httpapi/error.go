package httpapi

import (
	"net/http"

	project_logger "github.com/evolution-foundation/evolution-go/pkg/logger"
	"github.com/gin-gonic/gin"
	base_logger "github.com/gomessguii/logger"
)

// WriteError writes an additive, public-safe error envelope. Human-readable
// error text remains a string for existing clients; code and requestId are
// stable machine-facing fields.
func WriteError(ctx *gin.Context, status int, code, message string) {
	if status == http.StatusInternalServerError {
		code = "internal_error"
		message = "internal server error"
	}
	body := gin.H{"error": message, "code": code}
	if requestID := RequestID(ctx); requestID != "" {
		body["requestId"] = requestID
	}
	ctx.JSON(status, body)
}

// WriteInternal records the detailed error server-side and returns no internal
// error material to the caller. Logger redaction is defense in depth; callers
// must still avoid putting credentials into errors.
func WriteInternal(ctx *gin.Context, err error) {
	detail := "unspecified internal error"
	if err != nil {
		detail = project_logger.RedactSensitive(err.Error())
	}
	base_logger.LogError("request_id=%s internal error: %s", RequestID(ctx), detail)
	WriteError(ctx, http.StatusInternalServerError, "internal_error", "internal server error")
}
