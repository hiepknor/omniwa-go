package chat_service

import (
	"context"
	"errors"
	"strings"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"go.mau.fi/whatsmeow/types"
)

const (
	maxConversationHistorySyncCount = 1000
	minConversationMuteDuration     = time.Minute
	maxConversationMuteDuration     = 365 * 24 * time.Hour
)

var (
	ErrInvalidConversationCommand     = errors.New("invalid conversation command")
	ErrConversationProjectionNotReady = errors.New("conversation projection is not ready")
	ErrUnsupportedConversationCommand = errors.New("conversation command is not supported for this conversation type")
)

type ConversationCommandRepository interface {
	GetConversation(context.Context, string, string) (*projection_repository.ConversationRecord, error)
	GetMessage(context.Context, string, string) (*projection_model.ProjectedMessage, error)
}

type ConversationCommandResult struct {
	ConversationID string `json:"conversationId" binding:"required" format:"uuid"`
	Operation      string `json:"operation" binding:"required" enums:"archive,unarchive,pin,unpin,mute,unmute,history_sync"`
	Status         string `json:"status" binding:"required" enums:"accepted"`
}

type ConversationHistorySyncInput struct {
	AnchorMessageID string `json:"anchorMessageId" binding:"required"`
	Count           int    `json:"count" binding:"required" minimum:"1" maximum:"1000"`
}

type ConversationMuteInput struct {
	DurationSeconds int64 `json:"durationSeconds" binding:"required" minimum:"60" maximum:"31536000"`
}

type ConversationProviderError struct {
	err error
}

func (err *ConversationProviderError) Error() string { return "provider conversation command failed" }
func (err *ConversationProviderError) Unwrap() error { return err.err }

type ConversationCommandService struct {
	repository ConversationCommandRepository
	provider   ConversationCommandProvider
	ready      func(string) (bool, error)
}

func NewConversationCommandService(repository ConversationCommandRepository, provider ConversationCommandProvider, ready func(string) (bool, error)) *ConversationCommandService {
	return &ConversationCommandService{repository: repository, provider: provider, ready: ready}
}

func (service *ConversationCommandService) SetArchived(ctx context.Context, instanceID, conversationRef string, archived bool) (*ConversationCommandResult, error) {
	record, err := service.resolve(ctx, instanceID, conversationRef)
	if err != nil {
		return nil, err
	}
	if err := service.provider.SetArchived(ctx, instanceID, *record.Conversation.AddressingJID, archived); err != nil {
		return nil, &ConversationProviderError{err: err}
	}
	operation := "archive"
	if !archived {
		operation = "unarchive"
	}
	return acceptedConversationCommand(record.Conversation.ConversationID, operation), nil
}

func (service *ConversationCommandService) SetPinned(ctx context.Context, instanceID, conversationRef string, pinned bool) (*ConversationCommandResult, error) {
	record, err := service.resolve(ctx, instanceID, conversationRef)
	if err != nil {
		return nil, err
	}
	if err := service.provider.SetPinned(ctx, instanceID, *record.Conversation.AddressingJID, pinned); err != nil {
		return nil, &ConversationProviderError{err: err}
	}
	operation := "pin"
	if !pinned {
		operation = "unpin"
	}
	return acceptedConversationCommand(record.Conversation.ConversationID, operation), nil
}

func (service *ConversationCommandService) SetMuted(ctx context.Context, instanceID, conversationRef string, duration time.Duration) (*ConversationCommandResult, error) {
	if duration != 0 && (duration < minConversationMuteDuration || duration > maxConversationMuteDuration) {
		return nil, ErrInvalidConversationCommand
	}
	record, err := service.resolve(ctx, instanceID, conversationRef)
	if err != nil {
		return nil, err
	}
	if err := service.provider.SetMuted(ctx, instanceID, *record.Conversation.AddressingJID, duration); err != nil {
		return nil, &ConversationProviderError{err: err}
	}
	operation := "mute"
	if duration == 0 {
		operation = "unmute"
	}
	return acceptedConversationCommand(record.Conversation.ConversationID, operation), nil
}

func (service *ConversationCommandService) RequestHistorySync(ctx context.Context, instanceID, conversationRef string, input ConversationHistorySyncInput) (*ConversationCommandResult, error) {
	if strings.TrimSpace(input.AnchorMessageID) == "" || input.Count < 1 || input.Count > maxConversationHistorySyncCount {
		return nil, ErrInvalidConversationCommand
	}
	record, err := service.resolve(ctx, instanceID, conversationRef)
	if err != nil {
		return nil, err
	}
	message, err := service.repository.GetMessage(ctx, instanceID, input.AnchorMessageID)
	if err != nil {
		return nil, err
	}
	if message == nil || message.ConversationID == nil || *message.ConversationID != record.Conversation.ConversationID ||
		message.ChatID == "" || message.ProviderTimestamp.IsZero() ||
		(message.Direction != projection_model.MessageDirectionIncoming && message.Direction != projection_model.MessageDirectionOutgoing) {
		return nil, ErrInvalidConversationCommand
	}
	anchorJID, err := types.ParseJID(message.ChatID)
	if err != nil || anchorJID.IsEmpty() || !conversationJIDMatchesType(anchorJID, record.Conversation.Type) {
		return nil, ErrInvalidConversationCommand
	}
	messageInfo := types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     anchorJID.ToNonAD(),
			IsFromMe: message.Direction == projection_model.MessageDirectionOutgoing,
			IsGroup:  record.Conversation.Type == projection_model.ChatTypeGroup,
		},
		ID:        types.MessageID(message.MessageID),
		Timestamp: message.ProviderTimestamp.UTC(),
	}
	if _, err := service.provider.RequestHistorySync(ctx, instanceID, messageInfo, input.Count); err != nil {
		return nil, &ConversationProviderError{err: err}
	}
	return acceptedConversationCommand(record.Conversation.ConversationID, "history_sync"), nil
}

func (service *ConversationCommandService) resolve(ctx context.Context, instanceID, conversationRef string) (*projection_repository.ConversationRecord, error) {
	if service == nil || service.repository == nil || service.provider == nil || service.ready == nil || ctx == nil || instanceID == "" || strings.TrimSpace(conversationRef) == "" {
		return nil, ErrInvalidConversationCommand
	}
	ready, err := service.ready(instanceID)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, ErrConversationProjectionNotReady
	}
	record, err := service.repository.GetConversation(ctx, instanceID, conversationRef)
	if err != nil {
		return nil, err
	}
	if record == nil || record.Conversation.ConversationID == "" || record.Conversation.AddressingJID == nil {
		return nil, ErrInvalidConversationCommand
	}
	jid, err := types.ParseJID(*record.Conversation.AddressingJID)
	if err != nil || jid.IsEmpty() || !conversationJIDMatchesType(jid, record.Conversation.Type) {
		return nil, ErrUnsupportedConversationCommand
	}
	return record, nil
}

func conversationJIDMatchesType(jid types.JID, conversationType projection_model.ChatType) bool {
	switch conversationType {
	case projection_model.ChatTypeDirect:
		return jid.Server == types.DefaultUserServer || jid.Server == types.HiddenUserServer
	case projection_model.ChatTypeGroup:
		return jid.Server == types.GroupServer
	default:
		return false
	}
}

func acceptedConversationCommand(conversationID, operation string) *ConversationCommandResult {
	return &ConversationCommandResult{ConversationID: conversationID, Operation: operation, Status: "accepted"}
}
