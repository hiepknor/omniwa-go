package projection_service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

const contactResource = "contacts"

type contactEventIdentity struct {
	Kind  projection_model.ContactIdentityKind `json:"kind"`
	Value string                               `json:"value"`
}

type contactEventPayload struct {
	PreferredJID             string                 `json:"preferredJid"`
	Identities               []contactEventIdentity `json:"identities"`
	PhoneJID                 *string                `json:"phoneJid,omitempty"`
	LID                      *string                `json:"lid,omitempty"`
	Username                 *string                `json:"username,omitempty"`
	Found                    *bool                  `json:"found,omitempty"`
	FirstName                *string                `json:"firstName,omitempty"`
	FullName                 *string                `json:"fullName,omitempty"`
	PushName                 *string                `json:"pushName,omitempty"`
	BusinessName             *string                `json:"businessName,omitempty"`
	SaveOnPrimaryAddressbook *bool                  `json:"saveOnPrimaryAddressbook,omitempty"`
	PictureID                *string                `json:"pictureId,omitempty"`
	PictureAuthorJID         *string                `json:"pictureAuthorJid,omitempty"`
	PictureRemoved           *bool                  `json:"pictureRemoved,omitempty"`
	PictureUpdatedAt         *time.Time             `json:"pictureUpdatedAt,omitempty"`
	About                    *string                `json:"about,omitempty"`
	AboutUpdatedAt           *time.Time             `json:"aboutUpdatedAt,omitempty"`
	CompletedAt              *time.Time             `json:"completedAt,omitempty"`
}

func NormalizeContactEvent(instanceID string, rawEvent any) (*projection_model.Event, bool, error) {
	if instanceID == "" {
		return nil, true, errors.New("contact projection event has no instance identity")
	}
	var eventType string
	var occurredAt time.Time
	var payload contactEventPayload
	switch event := rawEvent.(type) {
	case *events.Contact:
		if event == nil || event.JID.IsEmpty() || event.Action == nil || !isContactJID(event.JID) {
			return nil, event != nil && !event.JID.IsEmpty(), errors.New("contact event is incomplete")
		}
		eventType, occurredAt = "contact", event.Timestamp.UTC()
		payload = newContactPayload(event.JID)
		found := true
		payload.Found = &found
		payload.FirstName, payload.FullName = event.Action.FirstName, event.Action.FullName
		payload.SaveOnPrimaryAddressbook = event.Action.SaveOnPrimaryAddressbook
		if err := addContactActionAliases(&payload, event.Action.GetPnJID(), event.Action.GetLidJID(), event.Action.Username); err != nil {
			return nil, true, err
		}
	case *events.PushName:
		if event == nil || event.JID.IsEmpty() || !isContactJID(event.JID) {
			return nil, event != nil && !event.JID.IsEmpty(), errors.New("push-name event is incomplete")
		}
		eventType = "push_name"
		occurredAt = eventMessageTime(event.Message)
		payload = newContactPayload(event.JID)
		payload.PushName = &event.NewPushName
		addJIDIdentity(&payload, event.JIDAlt)
	case *events.BusinessName:
		if event == nil || event.JID.IsEmpty() || !isContactJID(event.JID) {
			return nil, event != nil && !event.JID.IsEmpty(), errors.New("business-name event is incomplete")
		}
		eventType = "business_name"
		occurredAt = eventMessageTime(event.Message)
		payload = newContactPayload(event.JID)
		payload.BusinessName = &event.NewBusinessName
	case *events.Picture:
		if event == nil || event.JID.IsEmpty() || !isContactJID(event.JID) {
			return nil, false, nil
		}
		eventType, occurredAt = "picture", event.Timestamp.UTC()
		payload = newContactPayload(event.JID)
		payload.PictureID, payload.PictureRemoved, payload.PictureUpdatedAt = &event.PictureID, &event.Remove, contactTimePointer(event.Timestamp.UTC())
		if !event.Author.IsEmpty() {
			author := event.Author.ToNonAD().String()
			payload.PictureAuthorJID = &author
		}
	case *events.UserAbout:
		if event == nil || event.JID.IsEmpty() || !isContactJID(event.JID) {
			return nil, false, nil
		}
		eventType, occurredAt = "user_about", event.Timestamp.UTC()
		payload = newContactPayload(event.JID)
		payload.About, payload.AboutUpdatedAt = &event.Status, contactTimePointer(event.Timestamp.UTC())
	default:
		return nil, false, nil
	}
	if occurredAt.IsZero() {
		occurredAt = time.Unix(0, 0).UTC()
	}
	payload.Identities = deduplicateContactEventIdentities(payload.Identities)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, true, err
	}
	sum := sha256.Sum256([]byte(eventType + "\x00" + payload.PreferredJID + "\x00" + occurredAt.Format(time.RFC3339Nano) + "\x00" + string(encoded)))
	return &projection_model.Event{
		InstanceID: instanceID, Resource: contactResource, EventKey: hex.EncodeToString(sum[:]),
		EntityKey: payload.PreferredJID, EventType: eventType, OccurredAt: occurredAt, Payload: encoded,
	}, true, nil
}

func newContactPayload(jid types.JID) contactEventPayload {
	jid = jid.ToNonAD()
	payload := contactEventPayload{PreferredJID: jid.String()}
	addJIDIdentity(&payload, jid)
	return payload
}

func addJIDIdentity(payload *contactEventPayload, jid types.JID) {
	if payload == nil || jid.IsEmpty() || !isContactJID(jid) {
		return
	}
	jid = jid.ToNonAD()
	value := jid.String()
	payload.Identities = append(payload.Identities, contactEventIdentity{Kind: projection_model.ContactIdentityKindJID, Value: value})
	switch jid.Server {
	case types.DefaultUserServer, types.LegacyUserServer:
		payload.Identities = append(payload.Identities, contactEventIdentity{Kind: projection_model.ContactIdentityKindPhoneJID, Value: value})
		payload.PhoneJID = &value
	case types.HiddenUserServer, types.HostedLIDServer:
		payload.Identities = append(payload.Identities, contactEventIdentity{Kind: projection_model.ContactIdentityKindLID, Value: value})
		payload.LID = &value
	}
}

func addContactActionAliases(payload *contactEventPayload, phoneJID, lidJID string, username *string) error {
	for _, raw := range []struct {
		value string
		kind  projection_model.ContactIdentityKind
	}{{phoneJID, projection_model.ContactIdentityKindPhoneJID}, {lidJID, projection_model.ContactIdentityKindLID}} {
		if raw.value == "" {
			continue
		}
		jid, err := types.ParseJID(raw.value)
		if err != nil || jid.IsEmpty() || !contactAliasServerMatches(raw.kind, jid.Server) {
			return errors.New("contact event contains an invalid provider alias")
		}
		value := jid.ToNonAD().String()
		payload.Identities = append(payload.Identities, contactEventIdentity{Kind: projection_model.ContactIdentityKindJID, Value: value}, contactEventIdentity{Kind: raw.kind, Value: value})
		if raw.kind == projection_model.ContactIdentityKindPhoneJID {
			payload.PhoneJID = &value
			payload.PreferredJID = value
		} else {
			payload.LID = &value
		}
	}
	if username != nil && strings.TrimSpace(*username) != "" {
		value := strings.TrimSpace(*username)
		if len(value) > 255 {
			return errors.New("contact event username exceeds storage limits")
		}
		payload.Username = &value
		payload.Identities = append(payload.Identities, contactEventIdentity{Kind: projection_model.ContactIdentityKindUsername, Value: value})
	}
	return nil
}

func contactAliasServerMatches(kind projection_model.ContactIdentityKind, server string) bool {
	switch kind {
	case projection_model.ContactIdentityKindPhoneJID:
		return server == types.DefaultUserServer || server == types.LegacyUserServer
	case projection_model.ContactIdentityKindLID:
		return server == types.HiddenUserServer || server == types.HostedLIDServer
	default:
		return false
	}
}

func deduplicateContactEventIdentities(identities []contactEventIdentity) []contactEventIdentity {
	seen := make(map[string]struct{}, len(identities))
	result := make([]contactEventIdentity, 0, len(identities))
	for _, identity := range identities {
		key := string(identity.Kind) + "\x00" + identity.Value
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, identity)
	}
	return result
}

func isContactJID(jid types.JID) bool {
	switch jid.ToNonAD().Server {
	case types.DefaultUserServer, types.LegacyUserServer, types.HiddenUserServer, types.HostedLIDServer:
		return true
	default:
		return false
	}
}

func eventMessageTime(info *types.MessageInfo) time.Time {
	if info == nil {
		return time.Time{}
	}
	return info.Timestamp.UTC()
}

func contactTimePointer(value time.Time) *time.Time { return &value }
