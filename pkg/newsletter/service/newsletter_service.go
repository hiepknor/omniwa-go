package newsletter_service

import (
	"context"
	"errors"
	"fmt"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	"github.com/evolution-foundation/evolution-go/pkg/waquery"
	whatsmeow_service "github.com/evolution-foundation/evolution-go/pkg/whatsmeow/service"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

type NewsletterService interface {
	CreateNewsletter(data *CreateNewsletterStruct, instance *instance_model.Instance) (*types.NewsletterMetadata, error)
	ListNewsletter(ctx context.Context, instance *instance_model.Instance) ([]*types.NewsletterMetadata, error)
	GetNewsletter(ctx context.Context, data *GetNewsletterStruct, instance *instance_model.Instance) (*types.NewsletterMetadata, error)
	GetNewsletterInvite(ctx context.Context, data *GetNewsletterInviteStruct, instance *instance_model.Instance) (*types.NewsletterMetadata, error)
	SubscribeNewsletter(data *GetNewsletterStruct, instance *instance_model.Instance) error
	GetNewsletterMessages(ctx context.Context, data *GetNewsletterMessagesStruct, instance *instance_model.Instance) ([]*types.NewsletterMessage, error)
}

type newsletterService struct {
	clientPointer    map[string]*whatsmeow.Client
	whatsmeowService whatsmeow_service.WhatsmeowService
	loggerWrapper    *logger_wrapper.LoggerManager
	queryGuard       waquery.Guard
}

type CreateNewsletterStruct struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type GetNewsletterStruct struct {
	JID types.JID `json:"jid"`
}

type GetNewsletterInviteStruct struct {
	Key string `json:"key"`
}

type GetNewsletterMessagesStruct struct {
	JID      types.JID `json:"jid"`
	Count    int       `json:"count"`
	BeforeID int       `json:"before_id"`
}

func (n *newsletterService) ensureClientConnected(instanceId string) (*whatsmeow.Client, error) {
	client := n.clientPointer[instanceId]
	n.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] Checking client connection status - Client exists: %v", instanceId, client != nil)

	if client == nil {
		n.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] No client found, attempting to start new instance", instanceId)
		err := n.whatsmeowService.StartInstance(instanceId)
		if err != nil {
			n.loggerWrapper.GetLogger(instanceId).LogError("[%s] Failed to start instance: %v", instanceId, err)
			return nil, errors.New("no active session found")
		}

		n.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] Instance started, waiting 2 seconds...", instanceId)
		time.Sleep(2 * time.Second)

		client = n.clientPointer[instanceId]
		n.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] Checking new client - Exists: %v, Connected: %v",
			instanceId,
			client != nil,
			client != nil && client.IsConnected())

		if client == nil || !client.IsConnected() {
			n.loggerWrapper.GetLogger(instanceId).LogError("[%s] New client validation failed - Exists: %v, Connected: %v",
				instanceId,
				client != nil,
				client != nil && client.IsConnected())
			return nil, errors.New("no active session found")
		}
	} else if !client.IsConnected() {
		n.loggerWrapper.GetLogger(instanceId).LogError("[%s] Existing client is disconnected - Connected status: %v",
			instanceId,
			client.IsConnected())
		return nil, errors.New("client disconnected")
	}

	n.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] Client successfully validated - Connected: %v", instanceId, client.IsConnected())
	return client, nil
}

func (n *newsletterService) CreateNewsletter(data *CreateNewsletterStruct, instance *instance_model.Instance) (*types.NewsletterMetadata, error) {
	client, err := n.ensureClientConnected(instance.Id)
	if err != nil {
		return nil, err
	}

	newsletter, err := client.CreateNewsletter(context.Background(), whatsmeow.CreateNewsletterParams{
		Name:        data.Name,
		Description: data.Description,
	})
	if err != nil {
		n.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error create newsletter: %v", instance.Id, err)
		return nil, err
	}

	return newsletter, nil
}

func (n *newsletterService) ListNewsletter(ctx context.Context, instance *instance_model.Instance) ([]*types.NewsletterMetadata, error) {
	client, err := n.ensureClientConnected(instance.Id)
	if err != nil {
		return nil, err
	}

	newsletters, err := waquery.Do(ctx, n.queryGuard, instance.Id, waquery.OperationNewslettersList, "", client.GetSubscribedNewsletters)
	if err != nil {
		n.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error list newsletters: %v", instance.Id, err)
		return nil, err
	}

	// For each newsletter, fetch full info to get subscribers_count
	fullNewsletters := make([]*types.NewsletterMetadata, 0, len(newsletters))
	for index, newsletter := range newsletters {
		fullInfo, err := waquery.Do(ctx, n.queryGuard, instance.Id, waquery.OperationNewsletterInfo, newsletter.ID.String(), func(queryCtx context.Context) (*types.NewsletterMetadata, error) {
			return client.GetNewsletterInfo(queryCtx, newsletter.ID)
		})
		if err != nil {
			var rateLimitErr *waquery.RateLimitError
			if errors.As(err, &rateLimitErr) {
				// The subscribed-newsletter query already returned valid basic
				// metadata. Stop optional enrichment without turning that result
				// into an error or attempting more guarded queries.
				fullNewsletters = append(fullNewsletters, newsletters[index:]...)
				break
			}
			// If a non-rate-limit lookup fails, preserve the existing basic result.
			n.loggerWrapper.GetLogger(instance.Id).LogWarn("[%s] error getting full info for newsletter %s: %v", instance.Id, newsletter.ID.String(), err)
			fullNewsletters = append(fullNewsletters, newsletter)
			continue
		}
		fullNewsletters = append(fullNewsletters, fullInfo)
	}

	return fullNewsletters, nil
}

func (n *newsletterService) GetNewsletter(ctx context.Context, data *GetNewsletterStruct, instance *instance_model.Instance) (*types.NewsletterMetadata, error) {
	client, err := n.ensureClientConnected(instance.Id)
	if err != nil {
		return nil, err
	}

	newsletter, err := waquery.Do(ctx, n.queryGuard, instance.Id, waquery.OperationNewsletterInfo, data.JID.String(), func(queryCtx context.Context) (*types.NewsletterMetadata, error) {
		return client.GetNewsletterInfo(queryCtx, data.JID)
	})
	if err != nil {
		n.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error list newsletter: %v", instance.Id, err)
		return nil, err
	}

	return newsletter, nil
}

func (n *newsletterService) GetNewsletterInvite(ctx context.Context, data *GetNewsletterInviteStruct, instance *instance_model.Instance) (*types.NewsletterMetadata, error) {
	client, err := n.ensureClientConnected(instance.Id)
	if err != nil {
		return nil, err
	}

	newsletter, err := waquery.Do(ctx, n.queryGuard, instance.Id, waquery.OperationNewsletterInviteInfo, data.Key, func(queryCtx context.Context) (*types.NewsletterMetadata, error) {
		return client.GetNewsletterInfoWithInvite(queryCtx, data.Key)
	})
	if err != nil {
		n.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error list newsletter: %v", instance.Id, err)
		return nil, err
	}

	return newsletter, nil
}

func (n *newsletterService) SubscribeNewsletter(data *GetNewsletterStruct, instance *instance_model.Instance) error {
	client, err := n.ensureClientConnected(instance.Id)
	if err != nil {
		return err
	}

	_, err = client.NewsletterSubscribeLiveUpdates(context.TODO(), data.JID)
	if err != nil {
		n.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error list newsletter: %v", instance.Id, err)
		return err
	}

	return nil
}

func (n *newsletterService) GetNewsletterMessages(ctx context.Context, data *GetNewsletterMessagesStruct, instance *instance_model.Instance) ([]*types.NewsletterMessage, error) {
	client, err := n.ensureClientConnected(instance.Id)
	if err != nil {
		return nil, err
	}

	resource := fmt.Sprintf("%s:count=%d:before=%d", data.JID.String(), data.Count, data.BeforeID)
	messages, err := waquery.Do(ctx, n.queryGuard, instance.Id, waquery.OperationNewsletterMessages, resource, func(queryCtx context.Context) ([]*types.NewsletterMessage, error) {
		return client.GetNewsletterMessages(queryCtx, data.JID, &whatsmeow.GetNewsletterMessagesParams{
			Count: data.Count, Before: data.BeforeID,
		})
	})
	if err != nil {
		n.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error list newsletter: %v", instance.Id, err)
		return nil, err
	}

	return messages, nil
}

func NewNewsletterService(
	clientPointer map[string]*whatsmeow.Client,
	whatsmeowService whatsmeow_service.WhatsmeowService,
	queryGuard waquery.Guard,
	loggerWrapper *logger_wrapper.LoggerManager,
) NewsletterService {
	return &newsletterService{
		clientPointer:    clientPointer,
		whatsmeowService: whatsmeowService,
		queryGuard:       queryGuard,
		loggerWrapper:    loggerWrapper,
	}
}
