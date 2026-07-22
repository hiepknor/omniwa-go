package send_service

import (
	"context"
	"testing"
	"time"

	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

type captureMessageWriteThrough struct {
	instanceID string
	info       types.MessageInfo
	message    *waE2E.Message
}

func (c *captureMessageWriteThrough) WriteSent(_ context.Context, instanceID string, info types.MessageInfo, message *waE2E.Message) error {
	c.instanceID, c.info, c.message = instanceID, info, message
	return nil
}

func TestWriteMessageProjectionPassesConfirmedMessageToWriter(t *testing.T) {
	capture := &captureMessageWriteThrough{}
	service := &sendService{messageWriter: capture}
	message := &MessageSendStruct{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: types.NewJID("15550001", types.DefaultUserServer), IsFromMe: true},
			ID:            "message-1", Timestamp: time.Unix(1_000, 0),
		},
		Message: &waE2E.Message{Conversation: proto.String("confirmed")},
	}
	service.writeMessageProjection("instance-a", message)
	if capture.instanceID != "instance-a" || capture.info.ID != "message-1" || capture.message != message.Message {
		t.Fatalf("captured write-through = %#v", capture)
	}
}
