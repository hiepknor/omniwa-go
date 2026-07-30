package chat_handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	chat_service "github.com/evolution-foundation/evolution-go/pkg/chat/service"
	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/gin-gonic/gin"
)

type conversationCommandServiceFake struct {
	instanceID      string
	conversationRef string
	operation       string
	duration        time.Duration
	historyInput    chat_service.ConversationHistorySyncInput
	err             error
}

func (service *conversationCommandServiceFake) result(operation string) (*chat_service.ConversationCommandResult, error) {
	if service.err != nil {
		return nil, service.err
	}
	return &chat_service.ConversationCommandResult{ConversationID: "11111111-1111-1111-1111-111111111111", Operation: operation, Status: "accepted"}, nil
}
func (service *conversationCommandServiceFake) SetArchived(_ context.Context, instanceID, ref string, value bool) (*chat_service.ConversationCommandResult, error) {
	service.instanceID, service.conversationRef, service.operation = instanceID, ref, "archive"
	if !value {
		service.operation = "unarchive"
	}
	return service.result(service.operation)
}
func (service *conversationCommandServiceFake) SetPinned(_ context.Context, instanceID, ref string, value bool) (*chat_service.ConversationCommandResult, error) {
	service.instanceID, service.conversationRef, service.operation = instanceID, ref, "pin"
	if !value {
		service.operation = "unpin"
	}
	return service.result(service.operation)
}
func (service *conversationCommandServiceFake) SetMuted(_ context.Context, instanceID, ref string, duration time.Duration) (*chat_service.ConversationCommandResult, error) {
	service.instanceID, service.conversationRef, service.operation, service.duration = instanceID, ref, "mute", duration
	if duration == 0 {
		service.operation = "unmute"
	}
	return service.result(service.operation)
}
func (service *conversationCommandServiceFake) RequestHistorySync(_ context.Context, instanceID, ref string, input chat_service.ConversationHistorySyncInput) (*chat_service.ConversationCommandResult, error) {
	service.instanceID, service.conversationRef, service.operation, service.historyInput = instanceID, ref, "history_sync", input
	return service.result(service.operation)
}

func TestConversationCommandHandlersExposeCanonicalContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct{ name, method, path, body, operation string }{
		{name: "archive", method: http.MethodPost, path: "/conversations/absorbed%40s.whatsapp.net/archive", operation: "archive"},
		{name: "unarchive", method: http.MethodDelete, path: "/conversations/absorbed%40s.whatsapp.net/archive", operation: "unarchive"},
		{name: "pin", method: http.MethodPost, path: "/conversations/absorbed%40s.whatsapp.net/pin", operation: "pin"},
		{name: "unpin", method: http.MethodDelete, path: "/conversations/absorbed%40s.whatsapp.net/pin", operation: "unpin"},
		{name: "mute", method: http.MethodPut, path: "/conversations/absorbed%40s.whatsapp.net/mute", body: `{"durationSeconds":3600}`, operation: "mute"},
		{name: "unmute", method: http.MethodDelete, path: "/conversations/absorbed%40s.whatsapp.net/mute", operation: "unmute"},
		{name: "history", method: http.MethodPost, path: "/conversations/absorbed%40s.whatsapp.net/history-sync", body: `{"anchorMessageId":"anchor","count":50}`, operation: "history_sync"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &conversationCommandServiceFake{}
			request := httptest.NewRequest(test.method, test.path, bytes.NewBufferString(test.body))
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			conversationCommandTestRouter(service).ServeHTTP(response, request)
			if response.Code != http.StatusAccepted || service.instanceID != "instance-a" || service.conversationRef != "absorbed@s.whatsapp.net" || service.operation != test.operation {
				t.Fatalf("status=%d service=%+v body=%s", response.Code, service, response.Body.String())
			}
			if test.operation == "mute" && service.duration != time.Hour {
				t.Fatalf("duration=%s", service.duration)
			}
			if test.operation == "history_sync" && (service.historyInput.AnchorMessageID != "anchor" || service.historyInput.Count != 50) {
				t.Fatalf("input=%+v", service.historyInput)
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			data, _ := body["data"].(map[string]any)
			if data["conversationId"] != "11111111-1111-1111-1111-111111111111" || data["operation"] != test.operation || data["status"] != "accepted" {
				t.Fatalf("body=%v", body)
			}
		})
	}
}

func TestConversationCommandHandlersReturnMachineReadableErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "not ready", err: chat_service.ErrConversationProjectionNotReady, status: http.StatusServiceUnavailable, code: "projection_not_ready"},
		{name: "unsupported", err: chat_service.ErrUnsupportedConversationCommand, status: http.StatusUnprocessableEntity, code: "unsupported_conversation_operation"},
		{name: "invalid", err: chat_service.ErrInvalidConversationCommand, status: http.StatusBadRequest, code: "invalid_conversation_command"},
		{name: "provider", err: &chat_service.ConversationProviderError{}, status: http.StatusBadGateway, code: "provider_command_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/conversations/ref/archive", nil)
			request.Header.Set(httpapi.RequestIDHeader, "conversation-command-test")
			response := httptest.NewRecorder()
			conversationCommandTestRouter(&conversationCommandServiceFake{err: test.err}).ServeHTTP(response, request)
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.status || body["code"] != test.code || body["requestId"] != "conversation-command-test" {
				t.Fatalf("status=%d body=%v", response.Code, body)
			}
		})
	}

	service := &conversationCommandServiceFake{}
	request := httptest.NewRequest(http.MethodPut, "/conversations/ref/mute", bytes.NewBufferString(`{"durationSeconds":0}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	conversationCommandTestRouter(service).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || service.operation != "" {
		t.Fatalf("status=%d service=%+v", response.Code, service)
	}
}

func TestConversationCommandFeatureFlagsRequireAService(t *testing.T) {
	handler := NewChatHandler(nil, WithConversationCommands(nil, true, true))
	if handler.ConversationAppStateCommandsEnabled() || handler.ConversationHistorySyncEnabled() {
		t.Fatal("nil command service enabled canonical routes")
	}
	service := &conversationCommandServiceFake{}
	handler = NewChatHandler(nil, WithConversationCommands(service, true, false))
	if !handler.ConversationAppStateCommandsEnabled() || handler.ConversationHistorySyncEnabled() {
		t.Fatal("feature flags were not preserved")
	}
}

func conversationCommandTestRouter(service conversationCommandService) *gin.Engine {
	handler := NewChatHandler(nil, WithConversationCommands(service, true, true))
	router := gin.New()
	router.Use(httpapi.RequestIdentity(), func(ctx *gin.Context) { ctx.Set("instance", &instance_model.Instance{Id: "instance-a"}) })
	router.POST("/conversations/:conversationRef/archive", handler.ArchiveConversation)
	router.DELETE("/conversations/:conversationRef/archive", handler.UnarchiveConversation)
	router.POST("/conversations/:conversationRef/pin", handler.PinConversation)
	router.DELETE("/conversations/:conversationRef/pin", handler.UnpinConversation)
	router.PUT("/conversations/:conversationRef/mute", handler.MuteConversation)
	router.DELETE("/conversations/:conversationRef/mute", handler.UnmuteConversation)
	router.POST("/conversations/:conversationRef/history-sync", handler.ConversationHistorySync)
	return router
}
