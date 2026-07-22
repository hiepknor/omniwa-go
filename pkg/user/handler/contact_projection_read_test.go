package user_handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
}

func (s *contactUserServiceStub) GetContact(context.Context, *instance_model.Instance, string) (*user_service.ContactInfo, *projection_service.ProjectionReadMeta, error) {
	return s.contact, s.meta, s.err
}

func (s *contactUserServiceStub) GetContacts(context.Context, *instance_model.Instance) ([]user_service.ContactInfo, *projection_service.ProjectionReadMeta, error) {
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
