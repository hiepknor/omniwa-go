package observability

import (
	"io"
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

	request := httptest.NewRequest("GET", "/metrics", nil)
	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, request)
	body, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"instance-123", "provider_120363@g.us", "operation=\"batch\""} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("invalid metric material was exposed: %q", forbidden)
		}
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
		}()
	}
	wait.Wait()

	request := httptest.NewRequest("GET", "/metrics", nil)
	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, request)
	for _, expected := range []string{
		`omniwa_group_list_eligibility_request_duration_seconds_count{operation="aggregate"} 32`,
		`omniwa_group_list_mutation_rejections_total{code="group_list_group_unavailable",operation="campaign_create"} 32`,
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
}
