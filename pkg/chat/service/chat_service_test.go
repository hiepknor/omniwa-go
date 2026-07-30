package chat_service

import (
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

func TestHistorySyncRequestValidate(t *testing.T) {
	valid := func() *HistorySyncRequestStruct {
		return &HistorySyncRequestStruct{
			MessageInfo: &types.MessageInfo{
				MessageSource: types.MessageSource{Chat: types.NewJID("5511999999999", types.DefaultUserServer)},
				ID:            "message-id",
				Timestamp:     time.Now().UTC(),
			},
			Count: 50,
		}
	}

	tests := []struct {
		name   string
		mutate func(*HistorySyncRequestStruct) *HistorySyncRequestStruct
	}{
		{name: "nil request", mutate: func(*HistorySyncRequestStruct) *HistorySyncRequestStruct { return nil }},
		{name: "missing message info", mutate: func(value *HistorySyncRequestStruct) *HistorySyncRequestStruct { value.MessageInfo = nil; return value }},
		{name: "missing chat", mutate: func(value *HistorySyncRequestStruct) *HistorySyncRequestStruct {
			value.MessageInfo.Chat = types.JID{}
			return value
		}},
		{name: "missing message id", mutate: func(value *HistorySyncRequestStruct) *HistorySyncRequestStruct {
			value.MessageInfo.ID = " "
			return value
		}},
		{name: "missing timestamp", mutate: func(value *HistorySyncRequestStruct) *HistorySyncRequestStruct {
			value.MessageInfo.Timestamp = time.Time{}
			return value
		}},
		{name: "invalid count", mutate: func(value *HistorySyncRequestStruct) *HistorySyncRequestStruct { value.Count = 0; return value }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.mutate(valid()).Validate(); !errors.Is(err, ErrInvalidHistorySyncRequest) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid request error=%v", err)
	}
}

func TestHistorySyncRequestRejectsInvalidInputBeforeDependencies(t *testing.T) {
	service := &chatService{}
	if _, err := service.HistorySyncRequest(nil, nil); !errors.Is(err, ErrInvalidHistorySyncRequest) {
		t.Fatalf("error=%v", err)
	}
}
