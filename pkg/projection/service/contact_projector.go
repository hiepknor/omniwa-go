package projection_service

import (
	"context"
	"encoding/json"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
)

const ContactsProjectionSchemaVersion int64 = 1

type contactProjectionWriter interface {
	Apply(context.Context, projection_repository.ContactPatch) (*projection_model.Contact, bool, error)
}

type contactProjectionState interface {
	RecordEvent(instanceID, resource string, schemaVersion int64, occurredAt time.Time) error
	MarkReady(instanceID, resource string, schemaVersion int64, reconciledAt time.Time) error
}

var contactMutationEventTypes = []string{"contact", "push_name", "business_name", "picture", "user_about"}

type ContactProjector struct {
	contacts  contactProjectionWriter
	state     contactProjectionState
	readiness projectionReadinessBarrier
}

func NewContactProjector(contacts contactProjectionWriter, state contactProjectionState, readiness projectionReadinessBarrier) *ContactProjector {
	return &ContactProjector{contacts: contacts, state: state, readiness: readiness}
}

func (p *ContactProjector) Handle(ctx context.Context, event *projection_model.Event) error {
	if p == nil || p.contacts == nil || p.state == nil || p.readiness == nil {
		return permanentProjectionFailure(errorCodeMisconfigured)
	}
	if event == nil || event.Resource != contactResource || event.InstanceID == "" || event.EventKey == "" {
		return permanentProjectionFailure(errorCodeUnsupportedEvent)
	}
	var payload contactEventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return permanentProjectionFailure(errorCodeInvalidPayload)
	}
	if event.EventType == "contact_sync_complete" {
		if payload.PreferredJID != event.EntityKey || payload.CompletedAt == nil || payload.CompletedAt.IsZero() {
			return permanentProjectionFailure(errorCodeIncompletePayload)
		}
		unprocessed, err := p.readiness.HasUnprocessedEvents(ctx, event.InstanceID, contactResource, contactMutationEventTypes, event.EventKey)
		if err != nil {
			return err
		}
		if unprocessed {
			return retryableProjectionFailure(errorCodeDependencyPending)
		}
		return p.state.MarkReady(event.InstanceID, contactResource, ContactsProjectionSchemaVersion, payload.CompletedAt.UTC())
	}
	if payload.PreferredJID == "" || payload.PreferredJID != event.EntityKey || len(payload.Identities) == 0 {
		return permanentProjectionFailure(errorCodeIdentityMismatch)
	}
	patch := projection_repository.ContactPatch{
		InstanceID: event.InstanceID, Aspect: contactEventAspect(event.EventType), OccurredAt: event.OccurredAt, EventKey: event.EventKey,
		PreferredJID: payload.PreferredJID, PhoneJID: payload.PhoneJID, LID: payload.LID, Username: payload.Username,
		Found: payload.Found, FirstName: payload.FirstName, FullName: payload.FullName, PushName: payload.PushName,
		BusinessName: payload.BusinessName, SaveOnPrimaryAddressbook: payload.SaveOnPrimaryAddressbook,
		PictureID: payload.PictureID, PictureAuthorJID: payload.PictureAuthorJID, PictureRemoved: payload.PictureRemoved,
		PictureUpdatedAt: payload.PictureUpdatedAt, About: payload.About, AboutUpdatedAt: payload.AboutUpdatedAt,
	}
	if patch.Aspect == "" {
		return permanentProjectionFailure(errorCodeUnsupportedEvent)
	}
	for _, identity := range payload.Identities {
		patch.Identities = append(patch.Identities, projection_repository.ContactIdentityRef{Kind: identity.Kind, Value: identity.Value})
	}
	if _, _, err := p.contacts.Apply(ctx, patch); err != nil {
		return err
	}
	return p.state.RecordEvent(event.InstanceID, contactResource, ContactsProjectionSchemaVersion, event.OccurredAt)
}

func contactEventAspect(eventType string) projection_repository.ContactAspect {
	switch eventType {
	case "contact":
		return projection_repository.ContactAspectDetails
	case "push_name":
		return projection_repository.ContactAspectPushName
	case "business_name":
		return projection_repository.ContactAspectBusinessName
	case "picture":
		return projection_repository.ContactAspectPicture
	case "user_about":
		return projection_repository.ContactAspectAbout
	default:
		return ""
	}
}
