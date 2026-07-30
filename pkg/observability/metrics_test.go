package observability

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRegistryExposesProcessAndBoundedEligibilityMetrics(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	observer := registry.GroupListEligibility()
	observer.ObserveRequest(EligibilityOperationBatch, 250*time.Millisecond, 4, EligibilityCounts{Eligible: 2, Unavailable: 1, Unknown: 1})
	observer.ObserveMutationRejection(EligibilityOperationUpdate, "projection_not_ready")
	registry.ConversationAPI().ObserveConversationRequest(ConversationContractCanonical, ConversationOperationList, 200, 25*time.Millisecond)
	registry.ConversationAPI().ObserveConversationRequest(ConversationContractCanonical, ConversationOperationMessages, 503, 50*time.Millisecond)

	request := httptest.NewRequest("GET", "/metrics", nil)
	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{
		"go_goroutines", "process_cpu_seconds_total",
		`omniwa_group_list_eligibility_request_duration_seconds_count{operation="batch"} 1`,
		`omniwa_group_list_eligibility_requested_groups_sum{operation="batch"} 4`,
		`omniwa_group_list_eligibility_results_total{eligibility="eligible",operation="batch"} 2`,
		`omniwa_group_list_eligibility_results_total{eligibility="unavailable",operation="batch"} 1`,
		`omniwa_group_list_eligibility_results_total{eligibility="unknown",operation="batch"} 1`,
		`omniwa_group_list_mutation_rejections_total{code="projection_not_ready",operation="update"} 1`,
		`omniwa_conversation_api_requests_total{contract="conversation",operation="list",status="2xx"} 1`,
		`omniwa_conversation_api_requests_total{contract="conversation",operation="messages",status="5xx"} 1`,
		`omniwa_conversation_api_request_duration_seconds_count{contract="conversation",operation="list"} 1`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics missing %q", expected)
		}
	}
}

func TestRegistryRejectsUnboundedLabelsAndInvalidCounts(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	observer := registry.GroupListEligibility()
	observer.ObserveRequest("instance-123", time.Second, 1, EligibilityCounts{Eligible: 1})
	observer.ObserveRequest(EligibilityOperationBatch, time.Second, 2, EligibilityCounts{Eligible: 1})
	observer.ObserveMutationRejection(EligibilityOperationCreate, "provider_120363@g.us")
	registry.ConversationAPI().ObserveConversationRequest("instance-123", ConversationOperationList, 200, time.Second)
	registry.ConversationAPI().ObserveConversationRequest("legacy_chat", ConversationOperationList, 200, time.Second)
	registry.ConversationAPI().ObserveConversationRequest(ConversationContractCanonical, "provider_120363@g.us", 200, time.Second)
	registry.ConversationAPI().ObserveConversationRequest(ConversationContractCanonical, ConversationOperationList, 999, time.Second)
	registry.ConversationAPI().ObserveConversationRequest(ConversationContractProvider, "provider_120363@g.us", 200, time.Second)

	request := httptest.NewRequest("GET", "/metrics", nil)
	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, request)
	body, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"instance-123", "legacy_chat", "provider_120363@g.us", "operation=\"batch\"", "omniwa_conversation_api_requests_total"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("invalid metric material was exposed: %q", forbidden)
		}
	}
}

func TestConversationObserverRecordsBoundedProviderCommand(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	registry.ConversationAPI().ObserveConversationRequest(ConversationContractProvider, ConversationOperationHistorySync, http.StatusBadRequest, time.Millisecond)

	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, request)
	body, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	want := `omniwa_conversation_api_requests_total{contract="provider_chat",operation="history_sync",status="4xx"} 1`
	if !strings.Contains(string(body), want) {
		t.Fatalf("missing metric %q in %s", want, body)
	}
}

func TestEligibilityObserverIsConcurrentSafe(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	const workers = 32
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			registry.ObserveRequest(EligibilityOperationAggregate, time.Millisecond, 1, EligibilityCounts{Eligible: 1})
			registry.ObserveMutationRejection(EligibilityOperationCampaignCreate, "group_list_group_unavailable")
			registry.ObserveConversationRequest(ConversationContractCanonical, ConversationOperationGet, 200, time.Millisecond)
		}()
	}
	wait.Wait()

	request := httptest.NewRequest("GET", "/metrics", nil)
	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, request)
	for _, expected := range []string{
		`omniwa_group_list_eligibility_request_duration_seconds_count{operation="aggregate"} 32`,
		`omniwa_group_list_mutation_rejections_total{code="group_list_group_unavailable",operation="campaign_create"} 32`,
		`omniwa_conversation_api_requests_total{contract="conversation",operation="get",status="2xx"} 32`,
	} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("concurrent metrics missing %q", expected)
		}
	}
}

func TestNilRegistryReturnsUnavailableHandlerAndNoopObserver(t *testing.T) {
	var registry *Registry
	request := httptest.NewRequest("GET", "/metrics", nil)
	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, request)
	if response.Code != 503 {
		t.Fatalf("status=%d", response.Code)
	}
	registry.GroupListEligibility().ObserveRequest(EligibilityOperationBatch, 0, 0, EligibilityCounts{})
	registry.ConversationAPI().ObserveConversationRequest(ConversationContractCanonical, ConversationOperationList, 200, 0)
}
