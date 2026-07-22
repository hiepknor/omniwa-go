package auth_middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestBodyLimitRejectsDeclaredOversizedRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(BodyLimit())
	router.POST("/instance/create", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	request := httptest.NewRequest(http.MethodPost, "/instance/create", strings.NewReader("{}"))
	request.ContentLength = defaultJSONBodyLimit + 1
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge || !strings.Contains(response.Body.String(), `"code":"request_too_large"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBodyLimitBoundsChunkedBodyBeforeHandlerReadsIt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(BodyLimit())
	var readError error
	router.POST("/instance/create", func(ctx *gin.Context) {
		_, readError = io.ReadAll(ctx.Request.Body)
		ctx.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodPost, "/instance/create", strings.NewReader(strings.Repeat("x", int(defaultJSONBodyLimit+1))))
	request.ContentLength = -1
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)

	if readError == nil || !strings.Contains(readError.Error(), "request body too large") {
		t.Fatalf("read error=%v", readError)
	}
}

func TestRequestBodyLimitPreservesMediaCompatibility(t *testing.T) {
	for _, test := range []struct {
		path        string
		contentType string
		want        int64
	}{
		{path: "/instance/create", contentType: "application/json", want: defaultJSONBodyLimit},
		{path: "/send/media", contentType: "application/json", want: mediaJSONBodyLimit},
		{path: "/group/photo", contentType: "application/json", want: mediaJSONBodyLimit},
		{path: "/send/media", contentType: "multipart/form-data; boundary=test", want: multipartBodyLimit},
	} {
		request := httptest.NewRequest(http.MethodPost, test.path, nil)
		request.Header.Set("Content-Type", test.contentType)
		if got := requestBodyLimit(request); got != test.want {
			t.Errorf("requestBodyLimit(%s, %s)=%d want=%d", test.path, test.contentType, got, test.want)
		}
	}
}
