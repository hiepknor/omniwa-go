package chat_service

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

func TestRequestHistorySyncRejectsInvalidProviderInputBeforeDependencies(t *testing.T) {
	valid := func() types.MessageInfo {
		return types.MessageInfo{
			MessageSource: types.MessageSource{Chat: types.NewJID("5511999999999", types.DefaultUserServer)},
			ID:            "message-id",
			Timestamp:     time.Now().UTC(),
		}
	}
	tests := []struct {
		name       string
		instanceID string
		message    types.MessageInfo
		count      int
	}{
		{name: "missing instance", message: valid(), count: 50},
		{name: "missing chat", instanceID: "instance-id", message: types.MessageInfo{ID: "message-id", Timestamp: time.Now().UTC()}, count: 50},
		{name: "missing message id", instanceID: "instance-id", message: func() types.MessageInfo { value := valid(); value.ID = " "; return value }(), count: 50},
		{name: "missing timestamp", instanceID: "instance-id", message: func() types.MessageInfo { value := valid(); value.Timestamp = time.Time{}; return value }(), count: 50},
		{name: "invalid count", instanceID: "instance-id", message: valid()},
	}
	service := &chatService{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.RequestHistorySync(context.Background(), test.instanceID, test.message, test.count); !errors.Is(err, ErrInvalidHistorySyncRequest) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
