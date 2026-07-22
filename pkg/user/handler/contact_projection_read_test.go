package user_handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	user_service "github.com/evolution-foundation/evolution-go/pkg/user/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type contactUserServiceStub struct {
	user_service.UserService
	contacts []user_service.ContactInfo
	meta     *projection_service.ProjectionReadMeta
	err      error
	contact  *user_service.ContactInfo
	term     string
	limit    int
	cursor   string
	checked  *user_service.CheckUserCollection
}

func (s *contactUserServiceStub) CheckUser(context.Context, *user_service.CheckUserStruct, *instance_model.Instance) (*user_service.CheckUserCollection, error) {
	return s.checked, s.err
}

func (s *contactUserServiceStub) GetContact(context.Context, *instance_model.Instance, string) (*user_service.ContactInfo, *projection_service.ProjectionReadMeta, error) {
	return s.contact, s.meta, s.err
}

func (s *contactUserServiceStub) GetContacts(context.Context, *instance_model.Instance) ([]user_service.ContactInfo, *projection_service.ProjectionReadMeta, error) {
	return s.contacts, s.meta, s.err
}

func (s *contactUserServiceStub) SearchContacts(_ context.Context, _ *instance_model.Instance, term string, limit int, cursor string) ([]user_service.ContactInfo, *projection_service.ProjectionReadMeta, error) {
	s.term, s.limit, s.cursor = term, limit, cursor
	return s.contacts, s.meta, s.err
}

func TestGetContactsReturnsAdditiveProjectionMetaAndEmptyArray(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reconciledAt := time.Unix(500, 0)
	service := &contactUserServiceStub{contacts: []user_service.ContactInfo{}, meta: &projection_service.ProjectionReadMeta{
		Source: "projection", SyncStatus: projection_model.SyncStatusReady, LastSyncedAt: &reconciledAt,
	}}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/user/contacts", nil)
	ctx.Set("instance", &instance_model.Instance{Id: "instance-a"})
	(&userHandler{userService: service}).GetContacts(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	data, ok := body["data"].([]any)
	meta, metaOK := body["meta"].(map[string]any)
	if !ok || len(data) != 0 || !metaOK || meta["source"] != "projection" || body["message"] != "success" {
		t.Fatalf("response = %#v", body)
	}
}

func TestGetContactsReturnsServiceUnavailableUntilProjectionReady(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/user/contacts", nil)
	ctx.Set("instance", &instance_model.Instance{Id: "instance-a"})
	(&userHandler{userService: &contactUserServiceStub{err: projection_service.ErrContactsProjectionNotReady}}).GetContacts(ctx)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestGetContactValidatesJIDAndMapsNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name, contactID string
		err             error
		status          int
	}{
		{name: "invalid JID", contactID: "invalid", status: http.StatusBadRequest},
		{name: "missing contact", contactID: "15550001@s.whatsapp.net", err: gorm.ErrRecordNotFound, status: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/user/contact/"+test.contactID, nil)
			ctx.Params = gin.Params{{Key: "contactId", Value: test.contactID}}
			ctx.Set("instance", &instance_model.Instance{Id: "instance-a"})
			(&userHandler{userService: &contactUserServiceStub{err: test.err}}).GetContact(ctx)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestSearchContactsReturnsProjectionPageAndForwardsFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reconciledAt := time.Unix(500, 0)
	service := &contactUserServiceStub{
		contacts: []user_service.ContactInfo{{Jid: "alice@s.whatsapp.net", FullName: "Alice"}},
		meta: &projection_service.ProjectionReadMeta{
			Source: "projection", SyncStatus: projection_model.SyncStatusReady, LastSyncedAt: &reconciledAt, NextCursor: "next-page",
		},
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/user/contacts/search?q=Ali&limit=25&cursor=current", nil)
	ctx.Set("instance", &instance_model.Instance{Id: "instance-a"})
	(&userHandler{userService: service}).SearchContacts(ctx)
	if recorder.Code != http.StatusOK || service.term != "Ali" || service.limit != 25 || service.cursor != "current" {
		t.Fatalf("status=%d body=%s service=%#v", recorder.Code, recorder.Body.String(), service)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	data, dataOK := body["data"].([]any)
	meta, metaOK := body["meta"].(map[string]any)
	if !dataOK || len(data) != 1 || !metaOK || meta["nextCursor"] != "next-page" || meta["source"] != "projection" {
		t.Fatalf("response = %#v", body)
	}
}

func TestSearchContactsUsesStableSafeErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name   string
		query  string
		err    error
		status int
		code   string
	}{
		{name: "invalid limit", query: "?limit=0", status: http.StatusBadRequest, code: "invalid_pagination"},
		{name: "invalid cursor", err: projection_service.ErrInvalidContactCursor, status: http.StatusBadRequest, code: "invalid_cursor"},
		{name: "not ready", err: projection_service.ErrContactsProjectionNotReady, status: http.StatusServiceUnavailable, code: "projection_not_ready"},
		{name: "internal", err: errors.New("database password leaked"), status: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/user/contacts/search"+test.query, nil)
			ctx.Set("instance", &instance_model.Instance{Id: "instance-a"})
			(&userHandler{userService: &contactUserServiceStub{err: test.err}}).SearchContacts(ctx)
			if recorder.Code != test.status || (test.code != "" && !strings.Contains(recorder.Body.String(), `"code":"`+test.code+`"`)) ||
				strings.Contains(recorder.Body.String(), "database password leaked") {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestCheckUserReturnsAdditiveStaleCacheMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &contactUserServiceStub{checked: &user_service.CheckUserCollection{Users: []user_service.User{{Query: "15550001", IsInWhatsapp: true}}, Stale: true}}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/user/check", strings.NewReader(`{"number":["15550001"]}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("instance", &instance_model.Instance{Id: "instance-a"})
	(&userHandler{userService: service}).CheckUser(ctx)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"meta":{"source":"cache","stale":true}`) || strings.Contains(recorder.Body.String(), `"Stale"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
