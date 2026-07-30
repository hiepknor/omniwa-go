package chat_service

import (
	"context"
	"errors"
	"strings"
	"time"

	instance_runtime "github.com/evolution-foundation/evolution-go/pkg/instance/runtime"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	"github.com/evolution-foundation/evolution-go/pkg/utils"
	whatsmeow_service "github.com/evolution-foundation/evolution-go/pkg/whatsmeow/service"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/types"
)

type ConversationCommandProvider interface {
	SetArchived(context.Context, string, string, bool) error
	SetPinned(context.Context, string, string, bool) error
	SetMuted(context.Context, string, string, time.Duration) error
	RequestHistorySync(context.Context, string, types.MessageInfo, int) (*whatsmeow.SendResponse, error)
}

type Service interface {
	ConversationCommandProvider
}

type chatService struct {
	clients          instance_runtime.ClientProvider
	whatsmeowService whatsmeow_service.WhatsmeowService
	loggerWrapper    *logger_wrapper.LoggerManager
}

var _ ConversationCommandProvider = (*chatService)(nil)

var ErrInvalidHistorySyncRequest = errors.New("invalid history sync request")

func (c *chatService) ensureClientConnected(instanceId string) (*whatsmeow.Client, error) {
	client := c.clients.Get(instanceId)
	c.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] Checking client connection status - Client exists: %v", instanceId, client != nil)

	if client == nil {
		c.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] No client found, attempting to start new instance", instanceId)
		err := c.whatsmeowService.StartInstance(instanceId)
		if err != nil {
			c.loggerWrapper.GetLogger(instanceId).LogError("[%s] Failed to start instance: %v", instanceId, err)
			return nil, errors.New("no active session found")
		}

		c.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] Instance started, waiting 2 seconds...", instanceId)
		time.Sleep(2 * time.Second)

		client = c.clients.Get(instanceId)
		c.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] Checking new client - Exists: %v, Connected: %v",
			instanceId,
			client != nil,
			client != nil && client.IsConnected())

		if client == nil || !client.IsConnected() {
			c.loggerWrapper.GetLogger(instanceId).LogError("[%s] New client validation failed - Exists: %v, Connected: %v",
				instanceId,
				client != nil,
				client != nil && client.IsConnected())
			return nil, errors.New("no active session found")
		}
	} else if !client.IsConnected() {
		c.loggerWrapper.GetLogger(instanceId).LogError("[%s] Existing client is disconnected - Connected status: %v",
			instanceId,
			client.IsConnected())
		return nil, errors.New("client disconnected")
	}

	c.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] Client successfully validated - Connected: %v", instanceId, client.IsConnected())
	return client, nil
}

func (c *chatService) SetArchived(ctx context.Context, instanceID, target string, archived bool) error {
	client, recipient, err := c.commandTarget(instanceID, target)
	if err != nil {
		return err
	}
	if err := client.SendAppState(ctx, appstate.BuildArchive(recipient, archived, time.Time{}, nil)); err != nil {
		c.loggerWrapper.GetLogger(instanceID).LogError("[%s] error update archive state: %v", instanceID, err)
		return err
	}
	return nil
}

func (c *chatService) SetPinned(ctx context.Context, instanceID, target string, pinned bool) error {
	client, recipient, err := c.commandTarget(instanceID, target)
	if err != nil {
		return err
	}
	if err := client.SendAppState(ctx, appstate.BuildPin(recipient, pinned)); err != nil {
		c.loggerWrapper.GetLogger(instanceID).LogError("[%s] error update pin state: %v", instanceID, err)
		return err
	}
	return nil
}

func (c *chatService) SetMuted(ctx context.Context, instanceID, target string, duration time.Duration) error {
	if duration < 0 {
		return errors.New("mute duration must not be negative")
	}
	client, recipient, err := c.commandTarget(instanceID, target)
	if err != nil {
		return err
	}
	if err := client.SendAppState(ctx, appstate.BuildMute(recipient, duration > 0, duration)); err != nil {
		c.loggerWrapper.GetLogger(instanceID).LogError("[%s] error update mute state: %v", instanceID, err)
		return err
	}
	return nil
}

func (c *chatService) commandTarget(instanceID, target string) (*whatsmeow.Client, types.JID, error) {
	if instanceID == "" {
		return nil, types.JID{}, errors.New("provider command instance identity is required")
	}
	recipient, ok := utils.ParseJID(target)
	if !ok {
		c.loggerWrapper.GetLogger(instanceID).LogError("[%s] Error validating message fields", instanceID)
		return nil, types.JID{}, errors.New("invalid phone number")
	}
	client, err := c.ensureClientConnected(instanceID)
	return client, recipient, err
}

func (c *chatService) RequestHistorySync(ctx context.Context, instanceID string, messageInfo types.MessageInfo, count int) (*whatsmeow.SendResponse, error) {
	if instanceID == "" || messageInfo.Chat.IsEmpty() || strings.TrimSpace(messageInfo.ID) == "" || messageInfo.Timestamp.IsZero() || count < 1 {
		return nil, ErrInvalidHistorySyncRequest
	}
	client, err := c.ensureClientConnected(instanceID)
	if err != nil {
		return nil, err
	}
	histRequest := client.BuildHistorySyncRequest(&messageInfo, count)
	if err := c.whatsmeowService.WaitOutbound(ctx, instanceID, 1); err != nil {
		return nil, err
	}
	res, err := client.SendMessage(ctx, messageInfo.Chat, histRequest, whatsmeow.SendRequestExtra{Peer: true})
	if err != nil {
		c.loggerWrapper.GetLogger(instanceID).LogError("[%s] error history sync request: %v", instanceID, err)
		return nil, err
	}
	return &res, nil
}

func NewChatService(
	clients instance_runtime.ClientProvider,
	whatsmeowService whatsmeow_service.WhatsmeowService,
	loggerWrapper *logger_wrapper.LoggerManager,
) Service {
	return &chatService{
		clients:          clients,
		whatsmeowService: whatsmeowService,
		loggerWrapper:    loggerWrapper,
	}
}
