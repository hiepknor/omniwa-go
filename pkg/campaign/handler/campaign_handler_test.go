package campaign_handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	campaign_repository "github.com/evolution-foundation/evolution-go/pkg/campaign/repository"
	campaign_service "github.com/evolution-foundation/evolution-go/pkg/campaign/service"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type managementServiceFake struct {
	instanceID string
	campaignID string
	create     campaign_service.CreateCampaignInput
	target     campaign_model.CampaignStatus
	startsAt   *time.Time
	actor      campaign_repository.Actor
	err        error
	detail     *campaign_service.CampaignDetail
	list       *campaign_service.CampaignList
	recipients *campaign_service.RecipientList
	audit      *campaign_service.AuditList
	calls      int
}

func (f *managementServiceFake) Create(_ context.Context, instanceID string, input campaign_service.CreateCampaignInput) (*campaign_service.CampaignDetail, error) {
	f.calls++
	f.instanceID, f.create, f.actor = instanceID, input, input.Actor
	return f.detail, f.err
}
func (f *managementServiceFake) Get(_ context.Context, instanceID, campaignID string) (*campaign_service.CampaignDetail, error) {
	f.calls++
	f.instanceID, f.campaignID = instanceID, campaignID
	return f.detail, f.err
}
func (f *managementServiceFake) List(_ context.Context, instanceID string, _ campaign_model.CampaignStatus, _ int, _ string) (*campaign_service.CampaignList, error) {
	f.calls++
	f.instanceID = instanceID
	return f.list, f.err
}
func (f *managementServiceFake) Recipients(_ context.Context, instanceID, campaignID string, _ int, _ string) (*campaign_service.RecipientList, error) {
	f.calls++
	f.instanceID, f.campaignID = instanceID, campaignID
	return f.recipients, f.err
}
func (f *managementServiceFake) Audit(_ context.Context, instanceID, campaignID string, _ int, _ string) (*campaign_service.AuditList, error) {
	f.calls++
	f.instanceID, f.campaignID = instanceID, campaignID
	return f.audit, f.err
}
func (f *managementServiceFake) Transition(_ context.Context, instanceID, campaignID string, target campaign_model.CampaignStatus, startsAt *time.Time, actor campaign_repository.Actor) (*campaign_service.CampaignDetail, error) {
	f.calls++
	f.instanceID, f.campaignID, f.target, f.startsAt, f.actor = instanceID, campaignID, target, startsAt, actor
	return f.detail, f.err
}

func TestCreateIsInstanceScopedAndDoesNotEchoConsentEvidence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	instanceID, campaignID := uuid.NewString(), uuid.NewString()
	service := &managementServiceFake{detail: detailFixture(instanceID, campaignID)}
	handler := NewCampaignHandler(service)
	body := `{"name":"Launch","text":"Hello","recipients":[{"jid":"15550001@s.whatsapp.net","optInSource":"checkout","optInEvidenceReference":"private-consent-record","optedInAt":"2026-01-01T00:00:00Z"}]}`
	ctx, response := campaignContext(http.MethodPost, "/campaigns", body, instanceID, "instance-secret", "")
	handler.Create(ctx)
	if response.Code != http.StatusCreated || service.instanceID != instanceID || service.actor.Type != "instance" || service.actor.Reference != "instance-secret" || len(service.create.Recipients) != 1 {
		t.Fatalf("Create() status=%d service=%#v body=%s", response.Code, service, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "private-consent-record") || !strings.Contains(response.Body.String(), `"recipientCount":1`) {
		t.Fatalf("unsafe create response: %s", response.Body.String())
	}
}

func TestCampaignReadsAreScopedPaginatedAndRejectForgedIdentity(t *testing.T) {
	instanceID, campaignID := uuid.NewString(), uuid.NewString()
	service := &managementServiceFake{recipients: &campaign_service.RecipientList{Items: []campaign_model.Recipient{}, NextCursor: "next"}}
	handler := NewCampaignHandler(service)
	ctx, response := campaignContext(http.MethodGet, "/campaigns/"+campaignID+"/recipients?limit=10", "", instanceID, "token", campaignID)
	handler.Recipients(ctx)
	if response.Code != http.StatusOK || service.instanceID != instanceID || service.campaignID != campaignID || !strings.Contains(response.Body.String(), `"nextCursor":"next"`) {
		t.Fatalf("Recipients() status=%d service=%#v body=%s", response.Code, service, response.Body.String())
	}

	ctx, response = campaignContext(http.MethodGet, "/campaigns/forged", "", instanceID, "token", "forged")
	handler.Get(ctx)
	if response.Code != http.StatusBadRequest || service.calls != 1 || !strings.Contains(response.Body.String(), `"code":"invalid_campaign_input"`) {
		t.Fatalf("forged ID status=%d calls=%d body=%s", response.Code, service.calls, response.Body.String())
	}
}

func TestLifecycleUsesSafeConflictContractAndInstanceAttribution(t *testing.T) {
	instanceID, campaignID := uuid.NewString(), uuid.NewString()
	service := &managementServiceFake{err: campaign_repository.ErrInvalidCampaignTransition}
	handler := NewCampaignHandler(service)
	ctx, response := campaignContext(http.MethodPost, "/campaigns/"+campaignID+"/pause", "", instanceID, "instance-secret", campaignID)
	handler.Pause(ctx)
	if response.Code != http.StatusConflict || service.target != campaign_model.CampaignStatusPaused || service.actor.Reference != "instance-secret" ||
		!strings.Contains(response.Body.String(), `"code":"campaign_state_conflict"`) || strings.Contains(response.Body.String(), "invalid campaign status transition") {
		t.Fatalf("Pause() status=%d service=%#v body=%s", response.Code, service, response.Body.String())
	}
}

func TestCampaignHandlerRequiresInstanceContext(t *testing.T) {
	service := &managementServiceFake{list: &campaign_service.CampaignList{}}
	handler := NewCampaignHandler(service)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/campaigns", nil)
	handler.List(ctx)
	if response.Code != http.StatusUnauthorized || service.calls != 0 {
		t.Fatalf("List() status=%d calls=%d", response.Code, service.calls)
	}
}

func TestCreateRejectsOversizedRequestBeforeService(t *testing.T) {
	instanceID := uuid.NewString()
	service := &managementServiceFake{}
	handler := NewCampaignHandler(service)
	body := `{"name":"oversized","text":"` + strings.Repeat("x", maxCampaignRequestSize) + `","recipients":[]}`
	ctx, response := campaignContext(http.MethodPost, "/campaigns", body, instanceID, "token", "")
	handler.Create(ctx)
	if response.Code != http.StatusBadRequest || service.calls != 0 {
		t.Fatalf("Create() status=%d calls=%d", response.Code, service.calls)
	}
}

func campaignContext(method, target, body, instanceID, token, campaignID string) (*gin.Context, *httptest.ResponseRecorder) {
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.Header.Set("apikey", token)
	ctx.Set("instance", &instance_model.Instance{Id: instanceID})
	if campaignID != "" {
		ctx.Params = gin.Params{{Key: "campaignId", Value: campaignID}}
	}
	return ctx, response
}

func detailFixture(instanceID, campaignID string) *campaign_service.CampaignDetail {
	return &campaign_service.CampaignDetail{
		Campaign:       &campaign_model.Campaign{ID: campaignID, InstanceID: instanceID, Status: campaign_model.CampaignStatusDraft},
		RecipientCount: 1, ByStatus: map[campaign_model.RecipientStatus]int64{campaign_model.RecipientStatusPending: 1},
	}
}
