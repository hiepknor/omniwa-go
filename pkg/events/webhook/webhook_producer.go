package webhook_producer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	producer_interfaces "github.com/evolution-foundation/evolution-go/pkg/events/interfaces"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	"github.com/evolution-foundation/evolution-go/pkg/netguard"
)

type webhookProducer struct {
	url           string
	loggerWrapper *logger_wrapper.LoggerManager
	requester     netguard.Requester
}

func NewWebhookProducer(
	url string,
	requester netguard.Requester,
	loggerWrapper *logger_wrapper.LoggerManager,
) producer_interfaces.Producer {
	return &webhookProducer{
		url:           url,
		requester:     requester,
		loggerWrapper: loggerWrapper,
	}
}

func (p *webhookProducer) Produce(
	queueName string,
	payload []byte,
	webhookUrl string,
	userID string,
) error {
	splitQueue := strings.Split(queueName, ".")

	if len(splitQueue) < 2 {
		return nil
	}

	if p.url != "" {
		go p.sendWebhookWithRetry(p.url, payload, 5, 30*time.Second, userID)
	}
	if webhookUrl != "" {
		go p.sendWebhookWithRetry(webhookUrl, payload, 5, 30*time.Second, userID)
	}

	return nil
}

func (p *webhookProducer) sendWebhookWithRetry(url string, body []byte, maxRetries int, retryInterval time.Duration, userID string) {
	for i := 0; i < maxRetries; i++ {
		err, statusCode := p.sendWebhook(url, body)
		if err == nil {
			p.loggerWrapper.GetLogger(userID).LogInfo("[%s] webhook sent successfully - status: %d", userID, statusCode)
			return
		}
		p.loggerWrapper.GetLogger(userID).LogWarn("[%s] webhook failed - attempt: %d, error: %v", userID, i+1, err)
		if errors.Is(err, netguard.ErrUnsafeTarget) {
			return
		}

		time.Sleep(retryInterval)
	}
	p.loggerWrapper.GetLogger(userID).LogError("[%s] webhook failed after maximum retries", userID)
}

func (p *webhookProducer) sendWebhook(url string, body []byte) (error, int) {
	if p.requester == nil {
		return fmt.Errorf("%w: webhook host is not configured", netguard.ErrUnsafeTarget), 0
	}
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	resp, err := p.requester.Do(context.Background(), http.MethodPost, url, header, body)
	if err != nil {
		return err, 0
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("received non-2xx webhook response"), resp.StatusCode
	}

	return nil, resp.StatusCode
}

// CreateGlobalQueues não faz nada para webhook producer
func (p *webhookProducer) CreateGlobalQueues() error {
	return nil
}
