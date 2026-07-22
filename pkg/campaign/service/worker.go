package campaign_service

import (
	"context"
	"errors"
	"math"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	campaign_repository "github.com/evolution-foundation/evolution-go/pkg/campaign/repository"
	"github.com/evolution-foundation/evolution-go/pkg/outbound"
	"github.com/evolution-foundation/evolution-go/pkg/waquery"
)

type WorkerSettings struct {
	BatchSize    int
	Lease        time.Duration
	PollInterval time.Duration
	MaxAttempts  int
	RetryBase    time.Duration
}

func (s WorkerSettings) Validate() error {
	if s.BatchSize < 1 || s.BatchSize > 100 || s.Lease <= 0 || s.PollInterval <= 0 || s.MaxAttempts < 1 || s.RetryBase <= 0 {
		return errors.New("campaign worker settings are invalid")
	}
	return nil
}

type workerRepository interface {
	ClaimReady(context.Context, int, time.Duration) ([]campaign_model.Recipient, error)
	GetCampaign(context.Context, string, string) (*campaign_model.Campaign, error)
	MarkSent(context.Context, *campaign_model.Recipient, string) error
	MarkRetry(context.Context, *campaign_model.Recipient, string, time.Time) error
	MarkDeferred(context.Context, *campaign_model.Recipient, string, time.Time) error
	MarkFailed(context.Context, *campaign_model.Recipient, string) error
	Transition(context.Context, string, string, campaign_model.CampaignStatus, *time.Time, campaign_repository.Actor) (*campaign_model.Campaign, error)
}

type Sender interface {
	Send(context.Context, *campaign_model.Campaign, *campaign_model.Recipient) (string, error)
}

type BatchResult struct {
	Claimed  int
	Sent     int
	Retried  int
	Deferred int
	Failed   int
}

type ResultHandler func(BatchResult, error)

type Worker struct {
	repository workerRepository
	sender     Sender
	settings   WorkerSettings
	now        func() time.Time
	onResult   ResultHandler
}

func NewWorker(repository workerRepository, sender Sender, settings WorkerSettings, onResult ResultHandler) *Worker {
	return &Worker{repository: repository, sender: sender, settings: settings, now: time.Now, onResult: onResult}
}

func (w *Worker) RunOnce(ctx context.Context) (BatchResult, error) {
	var result BatchResult
	if w == nil || w.repository == nil || w.sender == nil || w.now == nil || ctx == nil {
		return result, errors.New("campaign worker dependencies are required")
	}
	if err := w.settings.Validate(); err != nil {
		return result, err
	}
	recipients, err := w.repository.ClaimReady(ctx, w.settings.BatchSize, w.settings.Lease)
	if err != nil {
		return result, err
	}
	result.Claimed = len(recipients)
	campaigns := make(map[string]*campaign_model.Campaign)
	affected := make(map[string][2]string)
	errorsList := make([]error, 0)
	for index := range recipients {
		if ctx.Err() != nil {
			return result, errors.Join(append(errorsList, ctx.Err())...)
		}
		recipient := &recipients[index]
		affected[recipient.CampaignID] = [2]string{recipient.InstanceID, recipient.CampaignID}
		campaign := campaigns[recipient.CampaignID]
		if campaign == nil {
			campaign, err = w.repository.GetCampaign(ctx, recipient.InstanceID, recipient.CampaignID)
			if err != nil {
				errorsList = append(errorsList, w.deferFailure(ctx, recipient, "campaign_unavailable", w.settings.RetryBase, &result))
				continue
			}
			campaigns[recipient.CampaignID] = campaign
		}
		providerID, sendErr := w.sender.Send(ctx, campaign, recipient)
		if sendErr == nil {
			if err := w.repository.MarkSent(ctx, recipient, providerID); err != nil {
				errorsList = append(errorsList, err)
				continue
			}
			result.Sent++
			continue
		}
		if ctx.Err() != nil {
			return result, errors.Join(append(errorsList, ctx.Err())...)
		}
		if err := w.recordSendFailure(ctx, recipient, safeSendErrorCode(sendErr), sendErr, &result); err != nil {
			errorsList = append(errorsList, err)
		}
	}
	for _, identity := range affected {
		_, completeErr := w.repository.Transition(ctx, identity[0], identity[1], campaign_model.CampaignStatusCompleted, nil, campaign_repository.Actor{Type: "system"})
		if completeErr != nil && !errors.Is(completeErr, campaign_repository.ErrCampaignHasPendingWork) &&
			!errors.Is(completeErr, campaign_repository.ErrInvalidCampaignTransition) && !errors.Is(completeErr, campaign_repository.ErrCampaignConflict) {
			errorsList = append(errorsList, completeErr)
		}
	}
	return result, errors.Join(errorsList...)
}

func (w *Worker) recordSendFailure(ctx context.Context, recipient *campaign_model.Recipient, errorCode string, sendErr error, result *BatchResult) error {
	now := w.now().UTC()
	var dependency *dependencyUnavailableError
	if errors.As(sendErr, &dependency) {
		return w.deferFailure(ctx, recipient, "dependency_unavailable", w.settings.RetryBase, result)
	}
	if delay, limited := retryAfter(sendErr); limited {
		return w.deferFailure(ctx, recipient, errorCode, delay, result)
	}
	if recipient.AttemptCount+1 >= w.settings.MaxAttempts {
		if err := w.repository.MarkFailed(ctx, recipient, errorCode); err != nil {
			return err
		}
		result.Failed++
		return nil
	}
	delay := exponentialDelay(w.settings.RetryBase, recipient.AttemptCount)
	if err := w.repository.MarkRetry(ctx, recipient, errorCode, now.Add(delay)); err != nil {
		return err
	}
	result.Retried++
	return nil
}

func (w *Worker) deferFailure(ctx context.Context, recipient *campaign_model.Recipient, errorCode string, delay time.Duration, result *BatchResult) error {
	if err := w.repository.MarkDeferred(ctx, recipient, errorCode, w.now().UTC().Add(positiveDelay(delay))); err != nil {
		return err
	}
	result.Deferred++
	return nil
}

func (w *Worker) Run(ctx context.Context) error {
	if w == nil || ctx == nil || w.settings.PollInterval <= 0 {
		return errors.New("campaign worker configuration is invalid")
	}
	if err := w.settings.Validate(); err != nil {
		return err
	}
	ticker := time.NewTicker(w.settings.PollInterval)
	defer ticker.Stop()
	for {
		result, err := w.RunOnce(ctx)
		if w.onResult != nil {
			w.onResult(result, err)
		}
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func retryAfter(err error) (time.Duration, bool) {
	if delay, ok := outbound.RetryAfter(err); ok {
		return positiveDelay(delay), true
	}
	var rateLimit *waquery.RateLimitError
	if errors.As(err, &rateLimit) {
		return positiveDelay(rateLimit.RetryAfter), true
	}
	return 0, false
}

func safeSendErrorCode(err error) string {
	if _, ok := outbound.RetryAfter(err); ok {
		return "outbound_rate_limited"
	}
	var rateLimit *waquery.RateLimitError
	if errors.As(err, &rateLimit) {
		return "info_rate_limited"
	}
	return "send_failed"
}

func exponentialDelay(base time.Duration, attempts int) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	power := math.Min(float64(attempts), 10)
	delay := float64(base) * math.Pow(2, power)
	maximum := float64(time.Hour)
	if delay > maximum {
		delay = maximum
	}
	return time.Duration(delay)
}

func positiveDelay(delay time.Duration) time.Duration {
	if delay < time.Second {
		return time.Second
	}
	return delay
}
