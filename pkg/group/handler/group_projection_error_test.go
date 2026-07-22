package group_handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestWriteGroupProjectionReadErrorUsesStableSafeContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name      string
		err       error
		status    int
		code      string
		forbidden string
	}{
		{name: "not ready", err: projection_service.ErrGroupsProjectionNotReady, status: http.StatusServiceUnavailable, code: "projection_not_ready"},
		{name: "not found", err: gorm.ErrRecordNotFound, status: http.StatusNotFound, code: "not_found"},
		{name: "internal", err: errors.New("database password leaked"), status: http.StatusInternalServerError, forbidden: "database password leaked"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			writeGroupProjectionReadError(ctx, test.err)
			if response.Code != test.status || (test.code != "" && !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`)) ||
				(test.forbidden != "" && strings.Contains(response.Body.String(), test.forbidden)) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
