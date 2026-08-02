package server_handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/config"
	"github.com/evolution-foundation/evolution-go/pkg/httpapi"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	instance_service "github.com/evolution-foundation/evolution-go/pkg/instance/service"
	auth_middleware "github.com/evolution-foundation/evolution-go/pkg/middleware"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type projectionStateHandlerStub struct {
	healthInstanceID       string
	capabilitiesInstanceID string
	capabilities           []string
	capabilitiesErr        error
}

type runtimeHealthStub struct {
	live  bool
	ready bool
}

func (s runtimeHealthStub) Live() bool  { return s.live }
func (s runtimeHealthStub) Ready() bool { return s.ready }

func TestRuntimeHealthEndpointsFailClosedAndDoNotCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name       string
		health     RuntimeHealth
		invoke     func(ServerHandler, *gin.Context)
		wantStatus int
		wantBody   string
	}{
		{name: "live", health: runtimeHealthStub{live: true}, invoke: func(handler ServerHandler, ctx *gin.Context) { handler.RuntimeLive(ctx) }, wantStatus: http.StatusOK, wantBody: `{"status":"ok"}`},
		{name: "terminated", health: runtimeHealthStub{}, invoke: func(handler ServerHandler, ctx *gin.Context) { handler.RuntimeLive(ctx) }, wantStatus: http.StatusServiceUnavailable, wantBody: `{"status":"not_live"}`},
		{name: "active", health: runtimeHealthStub{live: true, ready: true}, invoke: func(handler ServerHandler, ctx *gin.Context) { handler.RuntimeReady(ctx) }, wantStatus: http.StatusOK, wantBody: `{"status":"ready"}`},
		{name: "standby", health: runtimeHealthStub{live: true}, invoke: func(handler ServerHandler, ctx *gin.Context) { handler.RuntimeReady(ctx) }, wantStatus: http.StatusServiceUnavailable, wantBody: `{"status":"not_ready"}`},
		{name: "missing live state", invoke: func(handler ServerHandler, ctx *gin.Context) { handler.RuntimeLive(ctx) }, wantStatus: http.StatusServiceUnavailable, wantBody: `{"status":"not_live"}`},
		{name: "missing state", invoke: func(handler ServerHandler, ctx *gin.Context) { handler.RuntimeReady(ctx) }, wantStatus: http.StatusServiceUnavailable, wantBody: `{"status":"not_ready"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := NewServerHandler("test", "abc123", nil, nil, nil, WithRuntimeHealth(test.health))
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/server/health", nil)
			test.invoke(handler, ctx)
			if response.Code != test.wantStatus || strings.TrimSpace(response.Body.String()) != test.wantBody {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control=%q", response.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestServerOkContractIsUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewServerHandler("test", "abc123", nil, nil, nil)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/server/ok", nil)
	handler.ServerOk(ctx)
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func (s *projectionStateHandlerStub) Get(string, string) (*projection_model.State, error) {
	return nil, nil
}
func (s *projectionStateHandlerStub) GetServingState(instanceID, resource string) (*projection_model.State, error) {
	return s.Get(instanceID, resource)
}

type eventHistoryRepositoryStub struct {
	instanceID string
	page       *projection_repository.DurableEventPage
}

type overviewRepositoryHandlerStub struct{ instanceID string }

func (s *overviewRepositoryHandlerStub) Snapshot(_ context.Context, instanceID string, _, _ time.Time) (*projection_repository.OverviewCounts, error) {
	s.instanceID = instanceID
	return &projection_repository.OverviewCounts{InstancesTotal: 1}, nil
}

func (s *eventHistoryRepositoryStub) List(_ context.Context, instanceID, _ string, _ int, _ *projection_repository.DurableEventCursor) (*projection_repository.DurableEventPage, error) {
	s.instanceID = instanceID
	return s.page, nil
}
func (s *projectionStateHandlerStub) Ensure(string, string, int64) (*projection_model.State, error) {
	return nil, nil
}

func TestEventHistoryIsInstanceScopedAndRejectsInvalidPagination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repository := &eventHistoryRepositoryStub{page: &projection_repository.DurableEventPage{Items: []projection_model.DurableEvent{}}}
	reader := projection_service.NewDurableEventReader(repository, 30*24*time.Hour)
	handler := NewServerHandler("test", "abc123", &projectionStateHandlerStub{}, reader, nil)

	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/events?limit=10&type=Message", nil)
	ctx.Set("instance", &instance_model.Instance{Id: "instance-a"})
	handler.EventHistory(ctx)
	if response.Code != http.StatusOK || repository.instanceID != "instance-a" || !strings.Contains(response.Body.String(), `"data":[]`) || !strings.Contains(response.Body.String(), `"backfill":false`) {
		t.Fatalf("EventHistory() status=%d instance=%q body=%s", response.Code, repository.instanceID, response.Body.String())
	}

	response = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/events?cursor=forged", nil)
	ctx.Set("instance", &instance_model.Instance{Id: "instance-a"})
	handler.EventHistory(ctx)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_cursor"`) {
		t.Fatalf("invalid cursor status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestOverviewUsesAuthenticationScopeAndValidatesWindow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name       string
		instance   *instance_model.Instance
		wantID     string
		wantScope  string
		requestURL string
		wantStatus int
	}{
		{name: "admin scope", wantScope: `"type":"server"`, requestURL: "/server/overview?window=1h", wantStatus: http.StatusOK},
		{name: "instance scope", instance: &instance_model.Instance{Id: "instance-a"}, wantID: "instance-a", wantScope: `"type":"instance"`, requestURL: "/server/overview", wantStatus: http.StatusOK},
		{name: "invalid window", requestURL: "/server/overview?window=721h", wantStatus: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &overviewRepositoryHandlerStub{}
			handler := NewServerHandler("test", "abc123", &projectionStateHandlerStub{}, nil, projection_service.NewOverviewService(repository))
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest(http.MethodGet, test.requestURL, nil)
			if test.instance != nil {
				ctx.Set("instance", test.instance)
			}
			handler.Overview(ctx)
			if response.Code != test.wantStatus || (test.wantStatus == http.StatusOK && (repository.instanceID != test.wantID ||
				!strings.Contains(response.Body.String(), test.wantScope) || !strings.Contains(response.Body.String(), `"conversations":0,"chats":0`))) {
				t.Fatalf("Overview() status=%d instance=%q body=%s", response.Code, repository.instanceID, response.Body.String())
			}
		})
	}
}
func (s *projectionStateHandlerStub) RecordEvent(string, string, int64, time.Time) error { return nil }
func (s *projectionStateHandlerStub) MarkSyncing(string, string, int64) error            { return nil }
func (s *projectionStateHandlerStub) MarkReady(string, string, int64, time.Time) error   { return nil }
func (s *projectionStateHandlerStub) MarkStale(string, string, int64) error              { return nil }
func (s *projectionStateHandlerStub) MarkFailed(string, string, int64) error             { return nil }
func (s *projectionStateHandlerStub) Capabilities(instanceID string) ([]string, error) {
	s.capabilitiesInstanceID = instanceID
	return s.capabilities, s.capabilitiesErr
}
func (s *projectionStateHandlerStub) Health(instanceID string) (*projection_service.ProjectionHealth, error) {
	s.healthInstanceID = instanceID
	return &projection_service.ProjectionHealth{Status: "healthy", GeneratedAt: time.Unix(100, 0), ByStatus: map[string]int{}, Resources: []projection_service.ProjectionResourceHealth{}}, nil
}

func TestCapabilitiesExposeBuildRevision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewServerHandler("1.2.3", "0123456789abcdef", &projectionStateHandlerStub{}, nil, nil)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/server/capabilities", nil)
	httpapi.SetAuthPrincipal(ctx, httpapi.AuthPrincipal{Scope: httpapi.CredentialScopeAdmin})

	handler.Capabilities(ctx)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"version":"1.2.3"`) || !strings.Contains(response.Body.String(), `"revision":"0123456789abcdef"`) {
		t.Fatalf("Capabilities() status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestCredentialCapabilityIsAdminScoped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewServerHandler("test", "abc123", &projectionStateHandlerStub{}, nil, nil, WithAdminCapabilities("instance_token_rotation"))
	for _, test := range []struct {
		name      string
		principal httpapi.AuthPrincipal
		want      bool
	}{
		{name: "admin", principal: httpapi.AuthPrincipal{Scope: httpapi.CredentialScopeAdmin}, want: true},
		{name: "instance", principal: httpapi.AuthPrincipal{Scope: httpapi.CredentialScopeInstance, InstanceID: "instance-a"}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/server/capabilities", nil)
			httpapi.SetAuthPrincipal(ctx, test.principal)
			handler.Capabilities(ctx)
			got := strings.Contains(response.Body.String(), "instance_token_rotation")
			if response.Code != http.StatusOK || got != test.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

type capabilitiesCredentialResolver struct {
	instance *instance_model.Instance
}

func (r capabilitiesCredentialResolver) GetInstanceByID(instanceID string) (*instance_model.Instance, error) {
	if r.instance != nil && r.instance.Id == instanceID {
		return r.instance, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (r capabilitiesCredentialResolver) GetInstanceByToken(token string) (*instance_model.Instance, error) {
	switch token {
	case "instance-token":
		return r.instance, nil
	case "invalid-token":
		return nil, instance_service.ErrInvalidInstanceCredential
	default:
		return nil, errors.New("credential lookup unavailable")
	}
}

func TestAdminCapabilitiesCanTargetAnInstance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	instance := &instance_model.Instance{Id: "0bca2c34-ef2a-463c-98fd-e2afb6978457"}
	state := &projectionStateHandlerStub{capabilities: []string{"messages_projection", "messages_projection"}}
	handler := NewServerHandler(
		"1.2.3", "abc123", state, nil, nil,
		WithAdminCapabilities("instance_token_rotation", "messages_projection"),
		WithCapabilityInstanceReader(capabilitiesCredentialResolver{instance: instance}),
	)

	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/server/capabilities?instanceId="+instance.Id, nil)
	httpapi.SetAuthPrincipal(ctx, httpapi.AuthPrincipal{Scope: httpapi.CredentialScopeAdmin})
	handler.Capabilities(ctx)

	body := response.Body.String()
	if response.Code != http.StatusOK || state.capabilitiesInstanceID != instance.Id ||
		!strings.Contains(body, `"credentialScope":"admin"`) || !strings.Contains(body, `"instanceId":"`+instance.Id+`"`) ||
		strings.Count(body, `"messages_projection"`) != 1 {
		t.Fatalf("Capabilities() status=%d target=%q body=%s", response.Code, state.capabilitiesInstanceID, body)
	}
}

func TestCapabilityTargetValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	instance := &instance_model.Instance{Id: "0bca2c34-ef2a-463c-98fd-e2afb6978457"}
	for _, test := range []struct {
		name      string
		url       string
		principal httpapi.AuthPrincipal
		want      int
		code      string
	}{
		{name: "invalid admin target", url: "/server/capabilities?instanceId=not-a-uuid", principal: httpapi.AuthPrincipal{Scope: httpapi.CredentialScopeAdmin}, want: http.StatusBadRequest, code: "invalid_instance_id"},
		{name: "unknown admin target", url: "/server/capabilities?instanceId=71b08adc-3400-4932-9aa1-cdcbe5004207", principal: httpapi.AuthPrincipal{Scope: httpapi.CredentialScopeAdmin}, want: http.StatusNotFound, code: "instance_not_found"},
		{name: "instance cross scope", url: "/server/capabilities?instanceId=71b08adc-3400-4932-9aa1-cdcbe5004207", principal: httpapi.AuthPrincipal{Scope: httpapi.CredentialScopeInstance, InstanceID: instance.Id}, want: http.StatusForbidden, code: "instance_scope_mismatch"},
		{name: "instance own scope", url: "/server/capabilities?instanceId=" + instance.Id, principal: httpapi.AuthPrincipal{Scope: httpapi.CredentialScopeInstance, InstanceID: instance.Id}, want: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := NewServerHandler("test", "abc123", &projectionStateHandlerStub{}, nil, nil, WithCapabilityInstanceReader(capabilitiesCredentialResolver{instance: instance}))
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest(http.MethodGet, test.url, nil)
			httpapi.SetAuthPrincipal(ctx, test.principal)
			handler.Capabilities(ctx)
			if response.Code != test.want || (test.code != "" && !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`)) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestCapabilitiesAuthenticationContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	digest := "credential-digest-must-not-leak"
	instance := &instance_model.Instance{
		Id:          "0bca2c34-ef2a-463c-98fd-e2afb6978457",
		Name:        "private-instance-name",
		Token:       "instance-token",
		TokenDigest: &digest,
		Jid:         "15550001111@s.whatsapp.net",
		Proxy:       `{"provider":"private-provider-payload"}`,
	}
	state := &projectionStateHandlerStub{capabilities: []string{}}
	handler := NewServerHandler("1.2.3", "abc123", state, nil, nil, WithAdminCapabilities("instance_token_rotation"))
	auth := auth_middleware.NewMiddleware(&config.Config{GlobalApiKey: "admin-token"}, capabilitiesCredentialResolver{instance: instance})
	router := gin.New()
	router.GET("/server/capabilities", auth.AuthAdminOrInstance, handler.Capabilities)

	for _, test := range []struct {
		name             string
		token            string
		wantStatus       int
		wantScope        string
		wantInstanceID   string
		wantInstanceIDOK bool
		wantCapability   string
	}{
		{name: "admin credential", token: "admin-token", wantStatus: http.StatusOK, wantScope: "admin", wantCapability: "instance_token_rotation"},
		{name: "instance credential with projection not ready", token: "instance-token", wantStatus: http.StatusOK, wantScope: "instance", wantInstanceID: instance.Id, wantInstanceIDOK: true},
		{name: "missing credential", wantStatus: http.StatusUnauthorized},
		{name: "invalid credential", token: "invalid-token", wantStatus: http.StatusUnauthorized},
		{name: "credential lookup failure", token: "lookup-failure", wantStatus: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/server/capabilities", nil)
			if test.token != "" {
				request.Header.Set("apikey", test.token)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if test.wantStatus != http.StatusOK {
				return
			}

			var envelope struct {
				Message string `json:"message"`
				Data    struct {
					Version         string   `json:"version"`
					Revision        string   `json:"revision"`
					Capabilities    []string `json:"capabilities"`
					CredentialScope string   `json:"credentialScope"`
					InstanceID      *string  `json:"instanceId"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Message != "success" || envelope.Data.Version != "1.2.3" || envelope.Data.Revision != "abc123" || envelope.Data.CredentialScope != test.wantScope {
				t.Fatalf("unexpected contract: %#v", envelope)
			}
			if (envelope.Data.InstanceID != nil) != test.wantInstanceIDOK || (envelope.Data.InstanceID != nil && *envelope.Data.InstanceID != test.wantInstanceID) {
				t.Fatalf("instanceId=%v body=%s", envelope.Data.InstanceID, response.Body.String())
			}
			if envelope.Data.Capabilities == nil {
				t.Fatalf("capabilities must serialize as an array: %s", response.Body.String())
			}
			if test.wantCapability != "" && !strings.Contains(response.Body.String(), test.wantCapability) {
				t.Fatalf("missing capability %q: %s", test.wantCapability, response.Body.String())
			}
			for _, forbidden := range []string{instance.Token, digest, instance.Jid, instance.Name, "private-provider-payload", `"token"`, `"tokenDigest"`, `"jid"`, `"phone"`, `"provider"`} {
				if strings.Contains(response.Body.String(), forbidden) {
					t.Fatalf("response leaked %q: %s", forbidden, response.Body.String())
				}
			}
		})
	}

	if state.capabilitiesInstanceID != instance.Id {
		t.Fatalf("instance capabilities scope=%q", state.capabilitiesInstanceID)
	}
}

func TestCapabilitiesRejectsMissingPrincipalAndPreservesProjectionErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name      string
		principal *httpapi.AuthPrincipal
		state     *projectionStateHandlerStub
	}{
		{name: "missing principal", state: &projectionStateHandlerStub{}},
		{name: "projection error", principal: &httpapi.AuthPrincipal{Scope: httpapi.CredentialScopeInstance, InstanceID: "instance-a"}, state: &projectionStateHandlerStub{capabilitiesErr: errors.New("projection repository unavailable")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := NewServerHandler("test", "abc123", test.state, nil, nil)
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/server/capabilities", nil)
			if test.principal != nil {
				httpapi.SetAuthPrincipal(ctx, *test.principal)
			}
			handler.Capabilities(ctx)
			if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"code":"internal_error"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestProjectionHealthUsesAuthenticationScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name     string
		instance *instance_model.Instance
		wantID   string
	}{
		{name: "admin scope", wantID: ""},
		{name: "instance scope", instance: &instance_model.Instance{Id: "instance-a"}, wantID: "instance-a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := &projectionStateHandlerStub{}
			handler := NewServerHandler("test", "abc123", state, nil, nil)
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/server/projection-health", nil)
			if test.instance != nil {
				ctx.Set("instance", test.instance)
			}

			handler.ProjectionHealth(ctx)

			if response.Code != http.StatusOK || state.healthInstanceID != test.wantID {
				t.Fatalf("ProjectionHealth() status=%d scope=%q body=%s", response.Code, state.healthInstanceID, response.Body.String())
			}
		})
	}
}
