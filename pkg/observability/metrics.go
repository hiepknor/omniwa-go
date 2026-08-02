// Package observability owns operator-facing metrics and their bounded domain
// observer contracts. It intentionally does not use Prometheus' global
// registry so application and test lifecycles remain isolated.
package observability

import (
	"net/http"
	"sync"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/events/emission"
	"github.com/evolution-foundation/evolution-go/pkg/events/outbox"
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
	registry                     *prometheus.Registry
	eligibilityRequestDuration   *prometheus.HistogramVec
	eligibilityRequestedGroups   *prometheus.HistogramVec
	eligibilityResults           *prometheus.CounterVec
	mutationRejections           *prometheus.CounterVec
	conversationRequests         *prometheus.CounterVec
	conversationDuration         *prometheus.HistogramVec
	outboxAttempts               *prometheus.CounterVec
	outboxAttemptDuration        *prometheus.HistogramVec
	outboxClaimed                prometheus.Histogram
	outboxQueue                  *prometheus.GaugeVec
	outboxOldestPending          prometheus.Gauge
	outboxInfrastructureFailures *prometheus.CounterVec
	emitterRecords               *prometheus.CounterVec
	emitterRouteCount            *prometheus.HistogramVec
	emitterRoutes                *prometheus.CounterVec
	phoneIdentityEvidence        *prometheus.CounterVec
	phoneNumberResolution        *prometheus.CounterVec
	phonePayloadPolicy           *prometheus.CounterVec
	processRole                  *prometheus.GaugeVec
	processReady                 prometheus.Gauge
	processTransitions           *prometheus.CounterVec
	processStateMu               sync.Mutex
	processStateRevision         uint64
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
		outboxAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "omniwa", Subsystem: "external_event_outbox", Name: "attempts_total",
			Help: "External event delivery attempts by bounded transport, outcome, and error code.",
		}, []string{"transport", "outcome", "code"}),
		outboxAttemptDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "omniwa", Subsystem: "external_event_outbox", Name: "attempt_duration_seconds",
			Help: "External event delivery attempt duration by bounded transport and outcome.",
		}, []string{"transport", "outcome"}),
		outboxClaimed: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "omniwa", Subsystem: "external_event_outbox", Name: "claimed_batch_size",
			Help:    "Number of external event deliveries claimed per worker poll.",
			Buckets: []float64{0, 1, 5, 10, 25, 50, 100},
		}),
		outboxQueue: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "omniwa", Subsystem: "external_event_outbox", Name: "rows",
			Help: "External event outbox rows by bounded state.",
		}, []string{"status"}),
		outboxOldestPending: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "omniwa", Subsystem: "external_event_outbox", Name: "oldest_pending_age_seconds",
			Help: "Age of the oldest pending external event delivery.",
		}),
		outboxInfrastructureFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "omniwa", Subsystem: "external_event_outbox", Name: "infrastructure_failures_total",
			Help: "Outbox worker infrastructure failures by bounded code.",
		}, []string{"code"}),
		emitterRecords: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "omniwa", Subsystem: "external_event_emitter", Name: "records_total",
			Help: "Atomic external event record attempts by bounded mode and outcome.",
		}, []string{"mode", "outcome"}),
		emitterRouteCount: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "omniwa", Subsystem: "external_event_emitter", Name: "route_count",
			Help: "Number of selected durable routes per atomic event record.", Buckets: []float64{0, 1, 2, 3, 4},
		}, []string{"mode", "outcome"}),
		emitterRoutes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "omniwa", Subsystem: "external_event_emitter", Name: "routes_total",
			Help: "Successfully recorded durable routes by bounded transport and destination.",
		}, []string{"transport", "destination"}),
		phoneIdentityEvidence: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "omniwa", Subsystem: "phone_identity", Name: "evidence_total",
			Help: "Instance-scoped phone identity evidence observations by bounded outcome.",
		}, []string{"outcome"}),
		phoneNumberResolution: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "omniwa", Subsystem: "phone_identity", Name: "resolution_total",
			Help: "Instance-scoped phone number resolution attempts by bounded outcome.",
		}, []string{"outcome"}),
		phonePayloadPolicy: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "omniwa", Subsystem: "phone_identity", Name: "payload_policy_total",
			Help: "Phone payload policy applications by bounded outcome.",
		}, []string{"outcome"}),
		processRole: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "omniwa", Subsystem: "runtime", Name: "role",
			Help: "Current process role as a one-hot gauge over bounded roles.",
		}, []string{"role"}),
		processReady: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "omniwa", Subsystem: "runtime", Name: "ready",
			Help: "Whether the process is ready to receive business traffic.",
		}),
		processTransitions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "omniwa", Subsystem: "runtime", Name: "role_transitions_total",
			Help: "Successful process role transitions between bounded roles.",
		}, []string{"from", "to"}),
	}
	for _, collector := range []prometheus.Collector{
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		result.eligibilityRequestDuration, result.eligibilityRequestedGroups,
		result.eligibilityResults, result.mutationRejections,
		result.conversationRequests, result.conversationDuration,
		result.outboxAttempts, result.outboxAttemptDuration, result.outboxClaimed,
		result.outboxQueue, result.outboxOldestPending, result.outboxInfrastructureFailures,
		result.emitterRecords, result.emitterRouteCount, result.emitterRoutes,
		result.phoneIdentityEvidence, result.phoneNumberResolution, result.phonePayloadPolicy,
		result.processRole, result.processReady, result.processTransitions,
	} {
		if err := result.registry.Register(collector); err != nil {
			return nil, err
		}
	}
	return result, nil
}

var processRoles = [...]string{"starting", "standby", "promotion_pending", "active", "draining", "terminated"}

// ObserveProcessState records only the newest process-local state revision. The
// revision prevents delayed concurrent callbacks from restoring stale gauges.
func (r *Registry) ObserveProcessState(role string, ready bool, revision uint64) {
	if r == nil || !validProcessRole(role) || revision == 0 || ready != (role == "active") {
		return
	}
	r.processStateMu.Lock()
	defer r.processStateMu.Unlock()
	if revision <= r.processStateRevision {
		return
	}
	for _, candidate := range processRoles {
		value := 0.0
		if candidate == role {
			value = 1
		}
		r.processRole.WithLabelValues(candidate).Set(value)
	}
	if ready {
		r.processReady.Set(1)
	} else {
		r.processReady.Set(0)
	}
	r.processStateRevision = revision
}

func (r *Registry) ObserveProcessTransition(from, to string) {
	if r == nil || !validProcessRole(from) || !validProcessRole(to) || from == to {
		return
	}
	r.processTransitions.WithLabelValues(from, to).Inc()
}

func validProcessRole(value string) bool {
	for _, role := range processRoles {
		if value == role {
			return true
		}
	}
	return false
}

func (r *Registry) ObservePhoneNumberResolution(outcome string) {
	if r == nil || (outcome != "direct" && outcome != "paired" && outcome != "unresolved" && outcome != "failed") {
		return
	}
	r.phoneNumberResolution.WithLabelValues(outcome).Inc()
}

func (r *Registry) ObservePhonePayloadPolicy(outcome string) {
	if r == nil || (outcome != "preserved" && outcome != "redacted" && outcome != "failed") {
		return
	}
	r.phonePayloadPolicy.WithLabelValues(outcome).Inc()
}

// ObservePhoneIdentityEvidence records aggregate outcomes without provider or
// instance identifiers.
func (r *Registry) ObservePhoneIdentityEvidence(outcome string) {
	if r == nil || (outcome != "new" && outcome != "existing" && outcome != "conflict" && outcome != "failed") {
		return
	}
	r.phoneIdentityEvidence.WithLabelValues(outcome).Inc()
}

// ExternalEventEmitter returns aggregate-only acceptance instrumentation.
func (r *Registry) ExternalEventEmitter() emission.Observer {
	if r == nil {
		return nil
	}
	return r
}

func (r *Registry) ObserveEmission(mode, outcome string, routes int) {
	if r == nil || (mode != "history_only" && mode != "routed") || (outcome != "success" && outcome != "failed") || routes < 0 || routes > 4 {
		return
	}
	r.emitterRecords.WithLabelValues(mode, outcome).Inc()
	r.emitterRouteCount.WithLabelValues(mode, outcome).Observe(float64(routes))
}

func (r *Registry) ObserveRoute(transport outbox.Transport, destination outbox.Destination) {
	transportLabel := boundedOutboxTransport(transport)
	if r == nil || transportLabel == "" || transport == outbox.TransportNATS ||
		(destination != outbox.DestinationInstance && destination != outbox.DestinationGlobal) {
		return
	}
	r.emitterRoutes.WithLabelValues(transportLabel, string(destination)).Inc()
}

// ExternalEventOutbox returns aggregate-only worker instrumentation. Labels
// never include an instance, destination, routing key, or delivery identifier.
func (r *Registry) ExternalEventOutbox() outbox.Observer {
	if r == nil {
		return nil
	}
	return r
}

func (r *Registry) ObserveAttempt(transport outbox.Transport, outcome, code string, duration time.Duration) {
	transportLabel := boundedOutboxTransport(transport)
	if r == nil || transportLabel == "" || !boundedOutboxOutcome(outcome) || duration < 0 {
		return
	}
	code = boundedOutboxCode(code)
	r.outboxAttempts.WithLabelValues(transportLabel, outcome, code).Inc()
	r.outboxAttemptDuration.WithLabelValues(transportLabel, outcome).Observe(duration.Seconds())
}

func (r *Registry) ObserveClaimed(count int) {
	if r != nil && count >= 0 && count <= 1000 {
		r.outboxClaimed.Observe(float64(count))
	}
}

func (r *Registry) ObserveHealth(health outbox.Health) {
	if r == nil || health.Pending < 0 || health.Processing < 0 || health.DeadLetter < 0 || health.OldestPendingAge < 0 {
		return
	}
	r.outboxQueue.WithLabelValues("pending").Set(float64(health.Pending))
	r.outboxQueue.WithLabelValues("processing").Set(float64(health.Processing))
	r.outboxQueue.WithLabelValues("dead_letter").Set(float64(health.DeadLetter))
	r.outboxOldestPending.Set(health.OldestPendingAge.Seconds())
}

func (r *Registry) ObserveInfrastructureFailure(code string) {
	if r == nil || code != "repository_error" {
		return
	}
	r.outboxInfrastructureFailures.WithLabelValues(code).Inc()
}

func boundedOutboxTransport(value outbox.Transport) string {
	switch value {
	case outbox.TransportWebhook, outbox.TransportRabbitMQ, outbox.TransportNATS:
		return string(value)
	default:
		return ""
	}
}

func boundedOutboxOutcome(value string) bool {
	return value == "delivered" || value == "retry" || value == "dead_letter"
}

func boundedOutboxCode(value string) string {
	if value == "" {
		return "none"
	}
	switch value {
	case "attempt_timeout", "delivery_failed", "invalid_delivery", "invalid_payload", "invalid_destination",
		"destination_disabled", "instance_not_found", "target_lookup_failed", "transport_not_supported",
		"unsafe_target", "response_too_large", "cancelled", "connection_unavailable", "channel_unavailable",
		"confirm_unavailable", "queue_declare_failed", "publish_not_confirmed", "attempt_budget_exhausted":
		return value
	default:
		return "other"
	}
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
	return value == ConversationContractCanonical
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
