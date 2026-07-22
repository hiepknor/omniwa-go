package projection_service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	waSyncAction "go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types/events"
)

const labelResource = "labels"

type labelEventPayload struct {
	LabelID      string  `json:"labelId"`
	ChatID       string  `json:"chatId,omitempty"`
	MessageID    string  `json:"messageId,omitempty"`
	Name         *string `json:"name,omitempty"`
	Color        *int32  `json:"color,omitempty"`
	PredefinedID *int32  `json:"predefinedId,omitempty"`
	Deleted      *bool   `json:"deleted,omitempty"`
	OrderIndex   *int32  `json:"orderIndex,omitempty"`
	Active       *bool   `json:"active,omitempty"`
	Kind         *string `json:"kind,omitempty"`
	Immutable    *bool   `json:"immutable,omitempty"`
	Labeled      *bool   `json:"labeled,omitempty"`
}

func NormalizeLabelEvent(instanceID string, rawEvent any) (*projection_model.Event, bool, error) {
	if instanceID == "" {
		return nil, true, errors.New("label projection event has no instance identity")
	}
	var eventType string
	var occurredAt time.Time
	var payload labelEventPayload
	switch event := rawEvent.(type) {
	case *events.LabelEdit:
		if event == nil || event.LabelID == "" || event.Action == nil {
			return nil, true, errors.New("label edit event is incomplete")
		}
		eventType, occurredAt = "label_edit", event.Timestamp.UTC()
		payload = labelEventPayload{
			LabelID: event.LabelID, Name: event.Action.Name, Color: event.Action.Color,
			PredefinedID: event.Action.PredefinedID, Deleted: event.Action.Deleted,
			OrderIndex: event.Action.OrderIndex, Active: event.Action.IsActive,
			Immutable: event.Action.IsImmutable,
		}
		if event.Action.Type != nil {
			kind := normalizeLabelKind(*event.Action.Type)
			payload.Kind = &kind
		}
	case *events.LabelAssociationChat:
		if event == nil || event.LabelID == "" || event.JID.IsEmpty() || event.Action == nil || event.Action.Labeled == nil {
			return nil, true, errors.New("label chat association event is incomplete")
		}
		eventType, occurredAt = "label_chat_association", event.Timestamp.UTC()
		payload = labelEventPayload{LabelID: event.LabelID, ChatID: event.JID.String(), Labeled: event.Action.Labeled}
	case *events.LabelAssociationMessage:
		if event == nil || event.LabelID == "" || event.JID.IsEmpty() || event.MessageID == "" || event.Action == nil || event.Action.Labeled == nil {
			return nil, true, errors.New("label message association event is incomplete")
		}
		eventType, occurredAt = "label_message_association", event.Timestamp.UTC()
		payload = labelEventPayload{LabelID: event.LabelID, ChatID: event.JID.String(), MessageID: event.MessageID, Labeled: event.Action.Labeled}
	default:
		return nil, false, nil
	}
	if occurredAt.IsZero() {
		occurredAt = time.Unix(0, 0).UTC()
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, true, err
	}
	keyMaterial := eventType + "\x00" + payload.LabelID + "\x00" + occurredAt.Format(time.RFC3339Nano) + "\x00" + string(encoded)
	sum := sha256.Sum256([]byte(keyMaterial))
	return &projection_model.Event{
		InstanceID: instanceID, Resource: labelResource, EventKey: hex.EncodeToString(sum[:]),
		EntityKey: payload.LabelID, EventType: eventType, OccurredAt: occurredAt, Payload: encoded,
	}, true, nil
}

func normalizeLabelKind(value waSyncAction.LabelEditAction_ListType) string {
	return strings.ToLower(value.String())
}
