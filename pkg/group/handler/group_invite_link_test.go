package group_handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	group_service "github.com/evolution-foundation/evolution-go/pkg/group/service"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type groupInviteLinkServiceStub struct {
	group_service.GroupService
	link     string
	meta     *projection_service.ProjectionReadMeta
	err      error
	resetAck *group_service.CommandAcknowledgement
}

func (s *groupInviteLinkServiceStub) GetGroupInviteLink(context.Context, *group_service.GetGroupInviteLinkStruct, *instance_model.Instance) (string, *projection_service.ProjectionReadMeta, error) {
	return s.link, s.meta, s.err
}

func (s *groupInviteLinkServiceStub) ExecuteResetInviteLink(context.Context, *group_service.GetGroupInviteLinkStruct, *instance_model.Instance, group_service.ManagementCommandMetadata) (*group_service.CommandAcknowledgement, error) {
	return s.resetAck, s.err
}

func inviteLinkContext(body string) (*gin.Context, *httptest.ResponseRecorder) {
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/group/invitelink", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("instance", &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"})
	return ctx, response
}

func TestGroupInviteLinkReturnsCachedLinkWithProjectionMeta(t *testing.T) {
	reconciledAt := time.Unix(100, 0).UTC()
	meta := &projection_service.ProjectionReadMeta{Source: "projection", SyncStatus: projection_model.SyncStatusReady, LastSyncedAt: &reconciledAt}
	ctx, response := inviteLinkContext(`{"groupJid":"123@g.us","reset":false}`)
	(&groupHandler{groupService: &groupInviteLinkServiceStub{link: "https://chat.whatsapp.com/cached", meta: meta}, managementContract: true}).GetGroupInviteLink(ctx)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"data":"https://chat.whatsapp.com/cached"`) || !strings.Contains(response.Body.String(), `"source":"projection"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestGroupInviteLinkResetReturnsTypedAcknowledgement(t *testing.T) {
	ctx, response := inviteLinkContext(`{"groupJid":"123@g.us","reset":true}`)
	ctx.Request.Header.Set("Idempotency-Key", "reset-once")
	ack := &group_service.CommandAcknowledgement{CommandID: "command-id", Command: "invite_link_reset", GroupJID: "123@g.us", Status: "completed", ProjectionRefreshExpected: true}
	(&groupHandler{groupService: &groupInviteLinkServiceStub{resetAck: ack}, managementContract: true}).GetGroupInviteLink(ctx)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"message":"accepted"`) || !strings.Contains(response.Body.String(), `"command":"invite_link_reset"`) || !strings.Contains(response.Body.String(), `"status":"completed"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestGroupInviteLinkMissingCacheHasTypedPublicError(t *testing.T) {
	reconciledAt := time.Unix(100, 0).UTC()
	meta := &projection_service.ProjectionReadMeta{Source: "projection", SyncStatus: projection_model.SyncStatusStale, LastSyncedAt: &reconciledAt}
	ctx, response := inviteLinkContext(`{"groupJid":"123@g.us","reset":false}`)
	(&groupHandler{groupService: &groupInviteLinkServiceStub{meta: meta, err: group_service.ErrGroupInviteLinkNotFound}, managementContract: true}).GetGroupInviteLink(ctx)
	body := response.Body.String()
	if response.Code != http.StatusNotFound || !strings.Contains(body, `"code":"group_invite_link_not_found"`) || !strings.Contains(body, `"available":false`) || !strings.Contains(body, `"syncStatus":"stale"`) || strings.Contains(body, "chat.whatsapp.com") {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
}

func TestGroupInviteLinkMissingGroupIsDistinct(t *testing.T) {
	meta := &projection_service.ProjectionReadMeta{Source: "projection", SyncStatus: projection_model.SyncStatusReady}
	ctx, response := inviteLinkContext(`{"groupJid":"123@g.us","reset":false}`)
	(&groupHandler{groupService: &groupInviteLinkServiceStub{meta: meta, err: gorm.ErrRecordNotFound}, managementContract: true}).GetGroupInviteLink(ctx)
	body := response.Body.String()
	if response.Code != http.StatusNotFound || !strings.Contains(body, `"code":"group_not_found"`) || strings.Contains(body, `"available":false`) {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
}

func TestGroupInviteLinkProjectionNotReadyRemainsServiceUnavailable(t *testing.T) {
	ctx, response := inviteLinkContext(`{"groupJid":"123@g.us","reset":false}`)
	(&groupHandler{groupService: &groupInviteLinkServiceStub{err: projection_service.ErrGroupsProjectionNotReady}, managementContract: true}).GetGroupInviteLink(ctx)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"code":"projection_not_ready"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestGroupInviteLinkPermissionRevalidationUsesTypedErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "denied", err: group_service.ErrManagementPermissionDenied, status: http.StatusForbidden, code: "group_permission_denied"},
		{name: "unknown", err: group_service.ErrManagementPermissionUnknown, status: http.StatusConflict, code: "group_state_changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, response := inviteLinkContext(`{"groupJid":"123@g.us","reset":false}`)
			meta := &projection_service.ProjectionReadMeta{Source: "projection", SyncStatus: projection_model.SyncStatusReady}
			(&groupHandler{groupService: &groupInviteLinkServiceStub{meta: meta, err: test.err}, managementContract: true}).GetGroupInviteLink(ctx)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) || !strings.Contains(response.Body.String(), `"source":"projection"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestGroupInviteLinkDoesNotConvertUnknownErrorToNotFound(t *testing.T) {
	ctx, response := inviteLinkContext(`{"groupJid":"123@g.us","reset":false}`)
	(&groupHandler{groupService: &groupInviteLinkServiceStub{err: errors.New("database unavailable")}, managementContract: true}).GetGroupInviteLink(ctx)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"code":"internal_error"`) || strings.Contains(response.Body.String(), "database unavailable") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
