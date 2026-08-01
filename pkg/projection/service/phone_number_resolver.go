package projection_service

import (
	"context"
	"strings"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"go.mau.fi/whatsmeow/types"
)

// PhoneNumberResolutionObserver records bounded outcomes without phone-number labels.
type PhoneNumberResolutionObserver interface {
	ObservePhoneNumberResolution(string)
}

// ObserveCurrentProviderResult records explicit PN/LID metadata returned for the
// current instance before it can be exposed by Resolve.
func (r *PhoneNumberResolver) ObserveCurrentProviderResult(ctx context.Context, instanceID string, observedAt time.Time, jids ...types.JID) {
	if !r.Enabled() || r.repository == nil {
		return
	}
	recorder := NewPhoneIdentityEvidenceRecorder(r.repository, nil)
	if err := recorder.ObserveJIDs(ctx, instanceID, observedAt, jids...); err != nil {
		r.observer.ObservePhoneNumberResolution("failed")
	}
}

type noopPhoneNumberResolutionObserver struct{}

func (noopPhoneNumberResolutionObserver) ObservePhoneNumberResolution(string) {}

// PhoneNumberResolver is the only public phone-number resolution boundary. It
// uses instance-scoped persisted evidence and deliberately ignores provider-global maps.
type PhoneNumberResolver struct {
	repository projection_repository.PhoneIdentityEvidenceRepository
	enabled    bool
	observer   PhoneNumberResolutionObserver
}

func NewPhoneNumberResolver(repository projection_repository.PhoneIdentityEvidenceRepository, enabled bool, observer PhoneNumberResolutionObserver) *PhoneNumberResolver {
	if observer == nil {
		observer = noopPhoneNumberResolutionObserver{}
	}
	return &PhoneNumberResolver{repository: repository, enabled: enabled, observer: observer}
}

func (r *PhoneNumberResolver) Enabled() bool { return r != nil && r.enabled }

func (r *PhoneNumberResolver) Resolve(ctx context.Context, instanceID string, identities []string) map[string]string {
	if !r.Enabled() || r.repository == nil || len(identities) == 0 {
		return map[string]string{}
	}
	resolved, err := r.repository.Resolve(ctx, instanceID, identities)
	if err != nil {
		r.observer.ObservePhoneNumberResolution("failed")
		return map[string]string{}
	}
	result := make(map[string]string, len(resolved))
	for identity, phoneJID := range resolved {
		if phone := phoneDigits(phoneJID); phone != "" {
			result[identity] = phone
			if identity == phoneJID {
				r.observer.ObservePhoneNumberResolution("direct")
			} else {
				r.observer.ObservePhoneNumberResolution("paired")
			}
		}
	}
	if len(result) < len(identities) {
		r.observer.ObservePhoneNumberResolution("unresolved")
	}
	return result
}

func (r *PhoneNumberResolver) List(ctx context.Context, instanceID string) map[string]string {
	if !r.Enabled() || r.repository == nil {
		return map[string]string{}
	}
	rows, err := r.repository.List(ctx, instanceID)
	if err != nil {
		r.observer.ObservePhoneNumberResolution("failed")
		return map[string]string{}
	}
	for _, row := range rows {
		if row.LIDJID == nil {
			r.observer.ObservePhoneNumberResolution("direct")
		} else {
			r.observer.ObservePhoneNumberResolution("paired")
		}
	}
	return evidencePhoneNumbers(rows)
}

func evidencePhoneNumbers(rows []projection_model.PhoneIdentityEvidence) map[string]string {
	result := make(map[string]string, len(rows)*2)
	for _, row := range rows {
		phone := phoneDigits(row.PhoneJID)
		if phone == "" {
			continue
		}
		result[row.PhoneJID] = phone
		if row.LIDJID != nil {
			result[*row.LIDJID] = phone
		}
	}
	return result
}

func phoneDigits(value string) string {
	jid, err := types.ParseJID(strings.TrimSpace(value))
	if err != nil || jid.IsEmpty() {
		return ""
	}
	jid = jid.ToNonAD()
	if jid.Server != types.DefaultUserServer && jid.Server != types.LegacyUserServer {
		return ""
	}
	for _, char := range jid.User {
		if char < '0' || char > '9' {
			return ""
		}
	}
	return jid.User
}
