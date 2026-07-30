package routes

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

type conversationRouteAuth struct{}

func (conversationRouteAuth) Auth(ctx *gin.Context)                { ctx.Next() }
func (conversationRouteAuth) AuthAdmin(ctx *gin.Context)           { ctx.Next() }
func (conversationRouteAuth) AuthAdminOrInstance(ctx *gin.Context) { ctx.Next() }

type conversationRouteHandler struct {
	appState bool
	history  bool
}

func (conversationRouteHandler) ListConversations(*gin.Context)       {}
func (conversationRouteHandler) GetConversation(*gin.Context)         {}
func (conversationRouteHandler) ConversationMessages(*gin.Context)    {}
func (conversationRouteHandler) ConversationMessage(*gin.Context)     {}
func (conversationRouteHandler) ArchiveConversation(*gin.Context)     {}
func (conversationRouteHandler) UnarchiveConversation(*gin.Context)   {}
func (conversationRouteHandler) PinConversation(*gin.Context)         {}
func (conversationRouteHandler) UnpinConversation(*gin.Context)       {}
func (conversationRouteHandler) MuteConversation(*gin.Context)        {}
func (conversationRouteHandler) UnmuteConversation(*gin.Context)      {}
func (conversationRouteHandler) ConversationHistorySync(*gin.Context) {}
func (handler conversationRouteHandler) ConversationAppStateCommandsEnabled() bool {
	return handler.appState
}
func (handler conversationRouteHandler) ConversationHistorySyncEnabled() bool {
	return handler.history
}

func TestConversationRoutesContainCanonicalContractWithoutLegacyChatCommands(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	configured := &Routes{
		authMiddleware: conversationRouteAuth{},
		chatHandler:    conversationRouteHandler{appState: true, history: true},
	}
	configured.assignConversationRoutes(router)

	routes := map[string]bool{}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		routes[key] = true
		if strings.HasPrefix(route.Path, "/chat") {
			t.Fatalf("legacy Chat route registered: %s", key)
		}
	}
	for _, expected := range []string{
		http.MethodGet + " /conversations",
		http.MethodGet + " /conversations/:conversationRef",
		http.MethodGet + " /conversations/:conversationRef/messages",
		http.MethodGet + " /conversations/:conversationRef/messages/:messageId",
		http.MethodPost + " /conversations/:conversationRef/archive",
		http.MethodDelete + " /conversations/:conversationRef/archive",
		http.MethodPost + " /conversations/:conversationRef/pin",
		http.MethodDelete + " /conversations/:conversationRef/pin",
		http.MethodPut + " /conversations/:conversationRef/mute",
		http.MethodDelete + " /conversations/:conversationRef/mute",
		http.MethodPost + " /conversations/:conversationRef/history-sync",
	} {
		if !routes[expected] {
			t.Fatalf("canonical Conversation route missing: %s", expected)
		}
	}
}

func TestConversationCommandRoutesRemainCapabilityGated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	configured := &Routes{
		authMiddleware: conversationRouteAuth{},
		chatHandler:    conversationRouteHandler{},
	}
	configured.assignConversationRoutes(router)

	for _, route := range router.Routes() {
		if route.Method != http.MethodGet {
			t.Fatalf("command route registered while capabilities are disabled: %s %s", route.Method, route.Path)
		}
	}
}
