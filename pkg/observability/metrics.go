// Package observability owns operator-facing metrics and their bounded domain
// observer contracts. It intentionally does not use Prometheus' global
// registry so application and test lifecycles remain isolated.
package observability

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	EligibilityOperationBatch          = "batch"
	EligibilityOperationAggregate      = "aggregate"
	EligibilityOperationCreate         = "create"
	EligibilityOperationUpdate         = "update"
	EligibilityOperationCampaignCreate = "campaign_create"

	EligibilityStateEligible    = "eligible"
	EligibilityStateUnavailable = "unavailable"
	EligibilityStateUnknown     = "unknown"

	ConversationContractCanonical    = "conversation"
	ConversationContractProvider     = "provider_chat"
	ConversationOperationList        = "list"
	ConversationOperationGet         = "get"
	ConversationOperationMessages    = "messages"
	ConversationOperationMessage     = "message"
	ConversationOperationArchive     = "archive"
	ConversationOperationUnarchive   = "unarchive"
	ConversationOperationMute        = "mute"
	ConversationOperationUnmute      = "unmute"
	ConversationOperationPin         = "pin"
	ConversationOperationUnpin       = "unpin"
	ConversationOperationHistorySync = "history_sync"
)

var eligibilityBatchSizeBuckets = []float64{1, 10, 25, 50, 100, 500, 1_000, 2_500, 5_000, 10_000}

// EligibilityCounts contains one bounded assessment result count. It carries
// no instance or provider identity and is therefore safe to aggregate process-wide.
type EligibilityCounts struct {
	Eligible    int
	Unavailable int
	Unknown     int
}

// GroupListEligibilityObserver is the instrumentation boundary used by Group
// List and Campaign services. Implementations must reject labels outside the
// documented allowlists.
type GroupListEligibilityObserver interface {
	ObserveRequest(operation string, duration time.Duration, requested int, counts EligibilityCounts)
	ObserveMutationRejection(operation, code string)
}

// ConversationAPIObserver records bounded migration telemetry. Implementations
// must never label metrics with instance, Conversation, Chat, or provider IDs.
type ConversationAPIObserver interface {
	ObserveConversationRequest(contract, operation string, status int, duration time.Duration)
}

// Registry owns the process collectors and bounded OmniWA domain metrics.
type Registry struct {
	registry                   *prometheus.Registry
	eligibilityRequestDuration *prometheus.HistogramVec
	eligibilityRequestedGroups *prometheus.HistogramVec
	eligibilityResults         *prometheus.CounterVec
	mutationRejections         *prometheus.CounterVec
	conversationRequests       *prometheus.CounterVec
	conversationDuration       *prometheus.HistogramVec
}

// NewRegistry constructs an isolated registry. Registration failures are
// returned instead of panicking during application startup.
func NewRegistry() (*Registry, error) {
	result := &Registry{
		registry: prometheus.NewRegistry(),
		eligibilityRequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "omniwa", Subsystem: "group_list", Name: "eligibility_request_duration_seconds",
			Help: "Duration of backend-authoritative Group List eligibility assessments.",
		}, []string{"operation"}),
		eligibilityRequestedGroups: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "omniwa", Subsystem: "group_list", Name: "eligibility_requested_groups",
			Help:    "Number of groups requested in a backend-authoritative eligibility assessment.",
			Buckets: eligibilityBatchSizeBuckets,
		}, []string{"operation"}),
		eligibilityResults: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "omniwa", Subsystem: "group_list", Name: "eligibility_results_total",
			Help: "Backend-authoritative Group List eligibility results by stable state.",
		}, []string{"operation", "eligibility"}),
		mutationRejections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "omniwa", Subsystem: "group_list", Name: "mutation_rejections_total",
			Help: "Group List and group-target Campaign mutation rejections by stable public code.",
		}, []string{"operation", "code"}),
		conversationRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "omniwa", Subsystem: "conversation_api", Name: "requests_total",
			Help: "Conversation API requests by bounded contract, operation, and HTTP status class.",
		}, []string{"contract", "operation", "status"}),
		conversationDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "omniwa", Subsystem: "conversation_api", Name: "request_duration_seconds",
			Help: "Conversation API latency by bounded contract and operation.",
		}, []string{"contract", "operation"}),
	}
	for _, collector := range []prometheus.Collector{
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		result.eligibilityRequestDuration, result.eligibilityRequestedGroups,
		result.eligibilityResults, result.mutationRejections,
		result.conversationRequests, result.conversationDuration,
	} {
		if err := result.registry.Register(collector); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// ConversationAPI returns the bounded canonical-contract migration observer.
func (r *Registry) ConversationAPI() ConversationAPIObserver {
	if r == nil {
		return noopConversationAPIObserver{}
	}
	return r
}

// Handler returns the Prometheus exposition handler for this registry.
func (r *Registry) Handler() http.Handler {
	if r == nil || r.registry == nil {
		return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "metrics unavailable", http.StatusServiceUnavailable)
		})
	}
	return promhttp.HandlerFor(r.registry, promhttp.HandlerOpts{})
}

// GroupListEligibility returns the bounded eligibility observer.
func (r *Registry) GroupListEligibility() GroupListEligibilityObserver {
	if r == nil {
		return noopGroupListEligibilityObserver{}
	}
	return r
}

func (r *Registry) ObserveRequest(operation string, duration time.Duration, requested int, counts EligibilityCounts) {
	if r == nil || !requestOperation(operation) || duration < 0 || requested < 0 || invalidCounts(counts) ||
		counts.Eligible+counts.Unavailable+counts.Unknown != requested {
		return
	}
	r.eligibilityRequestDuration.WithLabelValues(operation).Observe(duration.Seconds())
	r.eligibilityRequestedGroups.WithLabelValues(operation).Observe(float64(requested))
	r.eligibilityResults.WithLabelValues(operation, EligibilityStateEligible).Add(float64(counts.Eligible))
	r.eligibilityResults.WithLabelValues(operation, EligibilityStateUnavailable).Add(float64(counts.Unavailable))
	r.eligibilityResults.WithLabelValues(operation, EligibilityStateUnknown).Add(float64(counts.Unknown))
}

func (r *Registry) ObserveMutationRejection(operation, code string) {
	if r == nil || !mutationOperation(operation) || !mutationRejectionCode(code) {
		return
	}
	r.mutationRejections.WithLabelValues(operation, code).Inc()
}

func (r *Registry) ObserveConversationRequest(contract, operation string, status int, duration time.Duration) {
	statusLabel := conversationStatusClass(status)
	if r == nil || !conversationContract(contract) || !conversationOperation(operation) || statusLabel == "" || duration < 0 {
		return
	}
	r.conversationRequests.WithLabelValues(contract, operation, statusLabel).Inc()
	r.conversationDuration.WithLabelValues(contract, operation).Observe(duration.Seconds())
}

func requestOperation(value string) bool {
	return value == EligibilityOperationBatch || value == EligibilityOperationAggregate
}

func mutationOperation(value string) bool {
	return value == EligibilityOperationCreate || value == EligibilityOperationUpdate || value == EligibilityOperationCampaignCreate
}

func mutationRejectionCode(value string) bool {
	switch value {
	case "group_list_not_found", "group_list_name_conflict", "group_list_version_conflict", "group_list_empty",
		"group_list_invalid_group", "group_list_group_unavailable", "invalid_group_list_input", "projection_not_ready":
		return true
	default:
		return false
	}
}

func conversationContract(value string) bool {
	return value == ConversationContractCanonical || value == ConversationContractProvider
}

func conversationOperation(value string) bool {
	switch value {
	case ConversationOperationList, ConversationOperationGet, ConversationOperationMessages, ConversationOperationMessage,
		ConversationOperationArchive, ConversationOperationUnarchive, ConversationOperationMute, ConversationOperationUnmute,
		ConversationOperationPin, ConversationOperationUnpin, ConversationOperationHistorySync:
		return true
	default:
		return false
	}
}

func conversationStatusClass(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500 && status < 600:
		return "5xx"
	default:
		return ""
	}
}

func invalidCounts(counts EligibilityCounts) bool {
	return counts.Eligible < 0 || counts.Unavailable < 0 || counts.Unknown < 0
}

type noopGroupListEligibilityObserver struct{}

func (noopGroupListEligibilityObserver) ObserveRequest(string, time.Duration, int, EligibilityCounts) {
}
func (noopGroupListEligibilityObserver) ObserveMutationRejection(string, string) {}

type noopConversationAPIObserver struct{}

func (noopConversationAPIObserver) ObserveConversationRequest(string, string, int, time.Duration) {}

var _ GroupListEligibilityObserver = (*Registry)(nil)
var _ GroupListEligibilityObserver = noopGroupListEligibilityObserver{}
var _ ConversationAPIObserver = (*Registry)(nil)
var _ ConversationAPIObserver = noopConversationAPIObserver{}
