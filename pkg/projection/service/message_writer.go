package projection_service

import (
	"context"
	"encoding/json"
	"errors"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type MessageWriteThrough interface {
	WriteSent(context.Context, string, types.MessageInfo, *waE2E.Message) error
}

type messageWriteThrough struct {
	projector *ChatMessageProjector
}

func NewMessageWriteThrough(projector *ChatMessageProjector) MessageWriteThrough {
	return &messageWriteThrough{projector: projector}
}

func (w *messageWriteThrough) WriteSent(ctx context.Context, instanceID string, info types.MessageInfo, message *waE2E.Message) error {
	if w == nil || w.projector == nil {
		return errors.New("message write-through projector is required")
	}
	if !info.IsFromMe {
		return errors.New("message write-through only accepts confirmed outbound messages")
	}
	event, relevant, err := NormalizeChatMessageEvent(instanceID, &events.Message{Info: info, Message: message})
	if err != nil {
		return err
	}
	if !relevant || event == nil {
		return errors.New("confirmed outbound message was not projection-relevant")
	}
	var payload messageEventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return errors.New("invalid normalized outbound message payload")
	}
	payload.Provenance = "write_through"
	event, _, err = newMessageProjectionEvent(instanceID, "message", payload.MessageID, event.OccurredAt, payload)
	if err != nil {
		return err
	}
	return w.projector.Handle(ctx, event)
}
