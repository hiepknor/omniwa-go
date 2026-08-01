package projection_service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"go.mau.fi/whatsmeow/types"
)

type PhoneIdentityEvidenceObserver interface {
	ObservePhoneIdentityEvidence(outcome string)
}

type noopPhoneIdentityEvidenceObserver struct{}

func (noopPhoneIdentityEvidenceObserver) ObservePhoneIdentityEvidence(string) {}

type PhoneIdentityEvidenceRecorder struct {
	repository projection_repository.PhoneIdentityEvidenceRepository
	observer   PhoneIdentityEvidenceObserver
}

func NewPhoneIdentityEvidenceRecorder(repository projection_repository.PhoneIdentityEvidenceRepository, observer PhoneIdentityEvidenceObserver) *PhoneIdentityEvidenceRecorder {
	if observer == nil {
		observer = noopPhoneIdentityEvidenceObserver{}
	}
	return &PhoneIdentityEvidenceRecorder{repository: repository, observer: observer}
}

func (r *PhoneIdentityEvidenceRecorder) ObserveJIDs(ctx context.Context, instanceID string, observedAt time.Time, jids ...types.JID) error {
	if r == nil || r.repository == nil || ctx == nil || instanceID == "" || observedAt.IsZero() {
		return errors.New("phone identity evidence recorder dependencies are required")
	}
	var phone, lid *types.JID
	for _, raw := range jids {
		if raw.IsEmpty() {
			continue
		}
		jid := raw.ToNonAD()
		switch jid.Server {
		case types.DefaultUserServer, types.LegacyUserServer:
			if phone != nil && phone.String() != jid.String() {
				r.observer.ObservePhoneIdentityEvidence("conflict")
				return nil
			}
			value := jid
			phone = &value
		case types.HiddenUserServer, types.HostedLIDServer:
			if lid != nil && lid.String() != jid.String() {
				r.observer.ObservePhoneIdentityEvidence("conflict")
				return nil
			}
			value := jid
			lid = &value
		}
	}
	if phone == nil {
		return nil
	}
	evidence := projection_model.PhoneIdentityEvidence{
		InstanceID: instanceID, PhoneJID: phone.String(), EvidenceKind: projection_model.PhoneIdentityEvidenceDirectPhone,
		FirstObservedAt: observedAt.UTC(), LastObservedAt: observedAt.UTC(),
	}
	if lid != nil {
		value := lid.String()
		evidence.LIDJID = &value
		evidence.EvidenceKind = projection_model.PhoneIdentityEvidencePairedAlt
	}
	created, err := r.repository.Observe(ctx, evidence)
	if err != nil {
		if errors.Is(err, projection_repository.ErrPhoneIdentityEvidenceConflict) {
			r.observer.ObservePhoneIdentityEvidence("conflict")
			return nil
		}
		r.observer.ObservePhoneIdentityEvidence("failed")
		return err
	}
	if created {
		r.observer.ObservePhoneIdentityEvidence("new")
	} else {
		r.observer.ObservePhoneIdentityEvidence("existing")
	}
	return nil
}

func (r *PhoneIdentityEvidenceRecorder) HandleContact(next EventHandler) EventHandler {
	return func(ctx context.Context, event *projection_model.Event) error {
		if event == nil {
			return next(ctx, event)
		}
		var payload contactEventPayload
		if err := json.Unmarshal(event.Payload, &payload); err == nil {
			if err := r.observeStrings(ctx, event.InstanceID, event.OccurredAt, payload.PhoneJID, payload.LID); err != nil {
				return err
			}
		}
		return next(ctx, event)
	}
}

func (r *PhoneIdentityEvidenceRecorder) HandleMessage(next EventHandler) EventHandler {
	return func(ctx context.Context, event *projection_model.Event) error {
		if event == nil {
			return next(ctx, event)
		}
		var payload messageEventPayload
		if err := json.Unmarshal(event.Payload, &payload); err == nil {
			pairs := [][2]*string{{payload.SenderJID, payload.SenderAltJID}, {stringPointer(payload.ChatID), nil}}
			if payload.Direction == projection_model.MessageDirectionOutgoing {
				pairs = append(pairs, [2]*string{payload.RecipientJID, payload.RecipientAltJID})
			} else {
				pairs = append(pairs, [2]*string{payload.RecipientAltJID, nil})
			}
			for _, pair := range pairs {
				if err := r.observeStrings(ctx, event.InstanceID, event.OccurredAt, pair[0], pair[1]); err != nil {
					return err
				}
			}
		}
		return next(ctx, event)
	}
}

func (r *PhoneIdentityEvidenceRecorder) observeStrings(ctx context.Context, instanceID string, observedAt time.Time, values ...*string) error {
	jids := make([]types.JID, 0, len(values))
	for _, value := range values {
		if value == nil || *value == "" {
			continue
		}
		jid, err := types.ParseJID(*value)
		if err != nil {
			continue
		}
		jids = append(jids, jid)
	}
	return r.ObserveJIDs(ctx, instanceID, observedAt, jids...)
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
