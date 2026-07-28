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

// Registry owns the process collectors and bounded OmniWA domain metrics.
type Registry struct {
	registry                   *prometheus.Registry
	eligibilityRequestDuration *prometheus.HistogramVec
	eligibilityRequestedGroups *prometheus.HistogramVec
	eligibilityResults         *prometheus.CounterVec
	mutationRejections         *prometheus.CounterVec
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
	}
	for _, collector := range []prometheus.Collector{
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		result.eligibilityRequestDuration, result.eligibilityRequestedGroups,
		result.eligibilityResults, result.mutationRejections,
	} {
		if err := result.registry.Register(collector); err != nil {
			return nil, err
		}
	}
	return result, nil
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

func invalidCounts(counts EligibilityCounts) bool {
	return counts.Eligible < 0 || counts.Unavailable < 0 || counts.Unknown < 0
}

type noopGroupListEligibilityObserver struct{}

func (noopGroupListEligibilityObserver) ObserveRequest(string, time.Duration, int, EligibilityCounts) {
}
func (noopGroupListEligibilityObserver) ObserveMutationRejection(string, string) {}

var _ GroupListEligibilityObserver = (*Registry)(nil)
var _ GroupListEligibilityObserver = noopGroupListEligibilityObserver{}
