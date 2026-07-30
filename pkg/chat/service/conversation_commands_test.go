package chat_service

import (
	"context"
	"errors"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

type conversationCommandRepositoryFake struct {
	record           *projection_repository.ConversationRecord
	message          *projection_model.ProjectedMessage
	conversationRef  string
	conversationInst string
	messageID        string
	messageInst      string
	conversationErr  error
	messageErr       error
}

func (repository *conversationCommandRepositoryFake) GetConversation(_ context.Context, instanceID, conversationRef string) (*projection_repository.ConversationRecord, error) {
	repository.conversationInst, repository.conversationRef = instanceID, conversationRef
	return repository.record, repository.conversationErr
}

func (repository *conversationCommandRepositoryFake) GetMessage(_ context.Context, instanceID, messageID string) (*projection_model.ProjectedMessage, error) {
	repository.messageInst, repository.messageID = instanceID, messageID
	return repository.message, repository.messageErr
}

type conversationCommandProviderFake struct {
	operation  string
	instanceID string
	target     string
	value      bool
	duration   time.Duration
	info       types.MessageInfo
	count      int
	err        error
}

func (provider *conversationCommandProviderFake) SetArchived(_ context.Context, instanceID, target string, value bool) error {
	provider.operation, provider.instanceID, provider.target, provider.value = "archive", instanceID, target, value
	return provider.err
}
func (provider *conversationCommandProviderFake) SetPinned(_ context.Context, instanceID, target string, value bool) error {
	provider.operation, provider.instanceID, provider.target, provider.value = "pin", instanceID, target, value
	return provider.err
}
func (provider *conversationCommandProviderFake) SetMuted(_ context.Context, instanceID, target string, duration time.Duration) error {
	provider.operation, provider.instanceID, provider.target, provider.duration = "mute", instanceID, target, duration
	return provider.err
}
func (provider *conversationCommandProviderFake) RequestHistorySync(_ context.Context, instanceID string, info types.MessageInfo, count int) (*whatsmeow.SendResponse, error) {
	provider.operation, provider.instanceID, provider.info, provider.count = "history_sync", instanceID, info, count
	return &whatsmeow.SendResponse{}, provider.err
}

func TestConversationAppStateCommandsResolveCanonicalAddressing(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation string
		invoke    func(*ConversationCommandService) (*ConversationCommandResult, error)
		value     bool
		duration  time.Duration
	}{
		{name: "archive", operation: "archive", value: true, invoke: func(service *ConversationCommandService) (*ConversationCommandResult, error) {
			return service.SetArchived(context.Background(), "instance-a", "absorbed@s.whatsapp.net", true)
		}},
		{name: "unarchive", operation: "archive", value: false, invoke: func(service *ConversationCommandService) (*ConversationCommandResult, error) {
			return service.SetArchived(context.Background(), "instance-a", "absorbed@s.whatsapp.net", false)
		}},
		{name: "pin", operation: "pin", value: true, invoke: func(service *ConversationCommandService) (*ConversationCommandResult, error) {
			return service.SetPinned(context.Background(), "instance-a", "absorbed@s.whatsapp.net", true)
		}},
		{name: "unpin", operation: "pin", value: false, invoke: func(service *ConversationCommandService) (*ConversationCommandResult, error) {
			return service.SetPinned(context.Background(), "instance-a", "absorbed@s.whatsapp.net", false)
		}},
		{name: "mute", operation: "mute", duration: time.Hour, invoke: func(service *ConversationCommandService) (*ConversationCommandResult, error) {
			return service.SetMuted(context.Background(), "instance-a", "absorbed@s.whatsapp.net", time.Hour)
		}},
		{name: "unmute", operation: "mute", invoke: func(service *ConversationCommandService) (*ConversationCommandResult, error) {
			return service.SetMuted(context.Background(), "instance-a", "absorbed@s.whatsapp.net", 0)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := directConversationCommandRepository()
			provider := &conversationCommandProviderFake{}
			service := NewConversationCommandService(repository, provider, func(instanceID string) (bool, error) { return instanceID == "instance-a", nil })
			result, err := test.invoke(service)
			if err != nil {
				t.Fatal(err)
			}
			if repository.conversationInst != "instance-a" || repository.conversationRef != "absorbed@s.whatsapp.net" {
				t.Fatalf("repository scope=%q ref=%q", repository.conversationInst, repository.conversationRef)
			}
			if provider.operation != test.operation || provider.instanceID != "instance-a" || provider.target != "5511999999999@s.whatsapp.net" || provider.value != test.value || provider.duration != test.duration {
				t.Fatalf("provider=%+v", provider)
			}
			if result.ConversationID != "11111111-1111-1111-1111-111111111111" || result.Status != "accepted" {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestConversationHistorySyncDerivesProviderAnchorMetadata(t *testing.T) {
	repository := directConversationCommandRepository()
	conversationID := repository.record.Conversation.ConversationID
	repository.message = &projection_model.ProjectedMessage{
		InstanceID: "instance-a", MessageID: "anchor-message", ChatID: "123456789@lid", ConversationID: &conversationID,
		Direction: projection_model.MessageDirectionOutgoing, ProviderTimestamp: time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC),
	}
	provider := &conversationCommandProviderFake{}
	service := NewConversationCommandService(repository, provider, func(string) (bool, error) { return true, nil })
	result, err := service.RequestHistorySync(context.Background(), "instance-a", conversationID, ConversationHistorySyncInput{AnchorMessageID: "anchor-message", Count: 50})
	if err != nil {
		t.Fatal(err)
	}
	if repository.messageInst != "instance-a" || repository.messageID != "anchor-message" {
		t.Fatalf("message scope=%q id=%q", repository.messageInst, repository.messageID)
	}
	if provider.info.Chat.String() != "123456789@lid" || !provider.info.IsFromMe || provider.info.IsGroup || provider.info.ID != "anchor-message" || !provider.info.Timestamp.Equal(repository.message.ProviderTimestamp) || provider.count != 50 {
		t.Fatalf("provider info=%+v count=%d", provider.info, provider.count)
	}
	if provider.info.Chat.String() == *repository.record.Conversation.AddressingJID {
		t.Fatal("history sync used conversation addressing JID instead of the anchor provider alias")
	}
	if result.Operation != "history_sync" || result.Status != "accepted" {
		t.Fatalf("result=%+v", result)
	}
}

func TestConversationCommandsFailClosed(t *testing.T) {
	t.Run("projection not ready", func(t *testing.T) {
		repository := directConversationCommandRepository()
		provider := &conversationCommandProviderFake{}
		service := NewConversationCommandService(repository, provider, func(string) (bool, error) { return false, nil })
		if _, err := service.SetArchived(context.Background(), "instance-a", "ref", true); !errors.Is(err, ErrConversationProjectionNotReady) {
			t.Fatalf("error=%v", err)
		}
		if repository.conversationRef != "" || provider.operation != "" {
			t.Fatal("not-ready command reached repository or provider")
		}
	})

	t.Run("unsupported newsletter", func(t *testing.T) {
		repository := directConversationCommandRepository()
		target := "120363000000000000@newsletter"
		repository.record.Conversation.Type = projection_model.ChatTypeNewsletter
		repository.record.Conversation.AddressingJID = &target
		provider := &conversationCommandProviderFake{}
		service := NewConversationCommandService(repository, provider, func(string) (bool, error) { return true, nil })
		if _, err := service.SetPinned(context.Background(), "instance-a", "ref", true); !errors.Is(err, ErrUnsupportedConversationCommand) {
			t.Fatalf("error=%v", err)
		}
		if provider.operation != "" {
			t.Fatal("unsupported conversation reached provider")
		}
	})

	t.Run("anchor belongs to another conversation", func(t *testing.T) {
		repository := directConversationCommandRepository()
		other := "22222222-2222-2222-2222-222222222222"
		repository.message = &projection_model.ProjectedMessage{MessageID: "anchor", ChatID: "5511999999999@s.whatsapp.net", ConversationID: &other, Direction: projection_model.MessageDirectionIncoming, ProviderTimestamp: time.Now().UTC()}
		provider := &conversationCommandProviderFake{}
		service := NewConversationCommandService(repository, provider, func(string) (bool, error) { return true, nil })
		if _, err := service.RequestHistorySync(context.Background(), "instance-a", "ref", ConversationHistorySyncInput{AnchorMessageID: "anchor", Count: 50}); !errors.Is(err, ErrInvalidConversationCommand) {
			t.Fatalf("error=%v", err)
		}
		if provider.operation != "" {
			t.Fatal("cross-conversation anchor reached provider")
		}
	})

	t.Run("provider error is typed", func(t *testing.T) {
		repository := directConversationCommandRepository()
		provider := &conversationCommandProviderFake{err: errors.New("provider unavailable")}
		service := NewConversationCommandService(repository, provider, func(string) (bool, error) { return true, nil })
		_, err := service.SetArchived(context.Background(), "instance-a", "ref", true)
		var providerErr *ConversationProviderError
		if !errors.As(err, &providerErr) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestConversationCommandTypePolicy(t *testing.T) {
	t.Run("group", func(t *testing.T) {
		repository := directConversationCommandRepository()
		target := "120363000000000000@g.us"
		repository.record.Conversation.Type = projection_model.ChatTypeGroup
		repository.record.Conversation.AddressingJID = &target
		provider := &conversationCommandProviderFake{}
		service := NewConversationCommandService(repository, provider, func(string) (bool, error) { return true, nil })
		if _, err := service.SetArchived(context.Background(), "instance-a", "ref", true); err != nil {
			t.Fatal(err)
		}
		if provider.target != target {
			t.Fatalf("target=%q", provider.target)
		}
	})
	for _, chatType := range []projection_model.ChatType{projection_model.ChatTypeNewsletter, projection_model.ChatTypeBroadcast, projection_model.ChatTypeUnknown} {
		t.Run(string(chatType), func(t *testing.T) {
			repository := directConversationCommandRepository()
			target := "status@broadcast"
			repository.record.Conversation.Type, repository.record.Conversation.AddressingJID = chatType, &target
			provider := &conversationCommandProviderFake{}
			service := NewConversationCommandService(repository, provider, func(string) (bool, error) { return true, nil })
			if _, err := service.SetArchived(context.Background(), "instance-a", "ref", true); !errors.Is(err, ErrUnsupportedConversationCommand) {
				t.Fatalf("error=%v", err)
			}
			if provider.operation != "" {
				t.Fatal("unsupported type reached provider")
			}
		})
	}
}

func directConversationCommandRepository() *conversationCommandRepositoryFake {
	addressingJID := "5511999999999@s.whatsapp.net"
	return &conversationCommandRepositoryFake{record: &projection_repository.ConversationRecord{Conversation: projection_model.Conversation{
		InstanceID: "instance-a", ConversationID: "11111111-1111-1111-1111-111111111111",
		Type: projection_model.ChatTypeDirect, AddressingJID: &addressingJID,
	}}}
}
