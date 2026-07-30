package chat_handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	chat_service "github.com/evolution-foundation/evolution-go/pkg/chat/service"
	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/gin-gonic/gin"
	"go.mau.fi/whatsmeow"
)

type historySyncChatService struct {
	calls int
}

func (*historySyncChatService) ChatPin(*chat_service.BodyStruct, *instance_model.Instance) (string, error) {
	return "", nil
}
func (*historySyncChatService) ChatUnpin(*chat_service.BodyStruct, *instance_model.Instance) (string, error) {
	return "", nil
}
func (*historySyncChatService) ChatArchive(*chat_service.BodyStruct, *instance_model.Instance) (string, error) {
	return "", nil
}
func (*historySyncChatService) ChatUnarchive(*chat_service.BodyStruct, *instance_model.Instance) (string, error) {
	return "", nil
}
func (*historySyncChatService) ChatMute(*chat_service.BodyStruct, *instance_model.Instance) (string, error) {
	return "", nil
}
func (*historySyncChatService) ChatUnmute(*chat_service.BodyStruct, *instance_model.Instance) (string, error) {
	return "", nil
}
func (service *historySyncChatService) HistorySyncRequest(*chat_service.HistorySyncRequestStruct, *instance_model.Instance) (*whatsmeow.SendResponse, error) {
	service.calls++
	return &whatsmeow.SendResponse{}, nil
}

func TestHistorySyncRequestRejectsInvalidInputWithoutCallingService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{`},
		{name: "empty", body: `{}`},
		{name: "missing message info", body: `{"count":50}`},
		{name: "empty message info", body: `{"messageInfo":{},"count":50}`},
		{name: "invalid count", body: `{"messageInfo":{"Chat":"5511999999999@s.whatsapp.net","ID":"message-id","Timestamp":"2026-07-30T00:00:00Z"},"count":0}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &historySyncChatService{}
			router := historySyncTestRouter(service)
			request := httptest.NewRequest(http.MethodPost, "/chat/history-sync", bytes.NewBufferString(test.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(httpapi.RequestIDHeader, "history-sync-test-request")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if service.calls != 0 {
				t.Fatalf("service calls=%d", service.calls)
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["code"] != "invalid_history_sync_request" || body["error"] != "invalid history sync request" || body["requestId"] != "history-sync-test-request" {
				t.Fatalf("body=%v", body)
			}
		})
	}
}

func TestHistorySyncRequestAcceptsValidInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &historySyncChatService{}
	router := historySyncTestRouter(service)
	body := `{"messageInfo":{"Chat":"5511999999999@s.whatsapp.net","ID":"message-id","Timestamp":"2026-07-30T00:00:00Z"},"count":50}`
	request := httptest.NewRequest(http.MethodPost, "/chat/history-sync", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || service.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, service.calls, response.Body.String())
	}
}

func historySyncTestRouter(service chat_service.ChatService) *gin.Engine {
	router := gin.New()
	router.Use(httpapi.RequestIdentity())
	router.POST("/chat/history-sync", func(ctx *gin.Context) {
		ctx.Set("instance", &instance_model.Instance{Id: "instance-id"})
	}, NewChatHandler(service, nil).HistorySyncRequest)
	return router
}
