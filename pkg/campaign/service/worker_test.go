package campaign_service

import (
	"context"
	"errors"
	"testing"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	campaign_repository "github.com/evolution-foundation/evolution-go/pkg/campaign/repository"
	"github.com/evolution-foundation/evolution-go/pkg/outbound"
	"github.com/evolution-foundation/evolution-go/pkg/waquery"
	"github.com/google/uuid"
)

type workerRepositoryFake struct {
	recipients       []campaign_model.Recipient
	campaign         *campaign_model.Campaign
	getErr           error
	sent             int
	retried          int
	deferred         int
	failed           int
	retryAt          time.Time
	errorCode        string
	transitionTarget campaign_model.CampaignStatus
}

func (f *workerRepositoryFake) ClaimReady(context.Context, int, time.Duration) ([]campaign_model.Recipient, error) {
	return f.recipients, nil
}
func (f *workerRepositoryFake) GetCampaign(context.Context, string, string) (*campaign_model.Campaign, error) {
	return f.campaign, f.getErr
}
func (f *workerRepositoryFake) MarkSent(context.Context, *campaign_model.Recipient, string) error {
	f.sent++
	return nil
}
func (f *workerRepositoryFake) MarkRetry(_ context.Context, _ *campaign_model.Recipient, code string, at time.Time) error {
	f.retried++
	f.errorCode, f.retryAt = code, at
	return nil
}
func (f *workerRepositoryFake) MarkDeferred(_ context.Context, _ *campaign_model.Recipient, code string, at time.Time) error {
	f.deferred++
	f.errorCode, f.retryAt = code, at
	return nil
}
func (f *workerRepositoryFake) MarkFailed(_ context.Context, _ *campaign_model.Recipient, code string) error {
	f.failed++
	f.errorCode = code
	return nil
}
func (f *workerRepositoryFake) Transition(_ context.Context, _, _ string, target campaign_model.CampaignStatus, _ *time.Time, _ campaign_repository.Actor) (*campaign_model.Campaign, error) {
	f.transitionTarget = target
	return f.campaign, nil
}

type senderFake struct {
	providerID string
	err        error
}

func (f senderFake) Send(context.Context, *campaign_model.Campaign, *campaign_model.Recipient) (string, error) {
	return f.providerID, f.err
}

func TestWorkerMarksSuccessfulSendAndAttemptsCompletion(t *testing.T) {
	repository, campaign := workerFixture(0)
	worker := newTestWorker(repository, senderFake{providerID: "provider-1"})
	result, err := worker.RunOnce(context.Background())
	if err != nil || result != (BatchResult{Claimed: 1, Sent: 1}) {
		t.Fatalf("RunOnce() = %#v, %v", result, err)
	}
	if repository.sent != 1 || repository.transitionTarget != campaign_model.CampaignStatusCompleted || campaign.ID == "" {
		t.Fatalf("repository state = %#v", repository)
	}
}

func TestWorkerDefersRateLimitsWithoutConsumingAttempt(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{name: "outbound", err: &outbound.RateLimitError{RetryAfter: 90 * time.Second}, code: "outbound_rate_limited"},
		{name: "information query", err: &waquery.RateLimitError{RetryAfter: 2 * time.Minute, Source: waquery.LimitSourceCircuitOpen}, code: "info_rate_limited"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, _ := workerFixture(2)
			worker := newTestWorker(repository, senderFake{err: test.err})
			result, err := worker.RunOnce(context.Background())
			if err != nil || result.Deferred != 1 || repository.deferred != 1 || repository.retried != 0 || repository.failed != 0 {
				t.Fatalf("RunOnce() = %#v, %v; repository = %#v", result, err, repository)
			}
			if repository.errorCode != test.code || !repository.retryAt.Equal(worker.now().UTC().Add(retryDelay(test.err))) {
				t.Fatalf("defer = %s at %v", repository.errorCode, repository.retryAt)
			}
		})
	}
}

func TestWorkerRetriesThenFailsAtBoundedAttemptLimit(t *testing.T) {
	repository, _ := workerFixture(1)
	worker := newTestWorker(repository, senderFake{err: errors.New("provider unavailable")})
	result, err := worker.RunOnce(context.Background())
	if err != nil || result.Retried != 1 || repository.retried != 1 || repository.errorCode != "send_failed" {
		t.Fatalf("retry result = %#v, %v; repository = %#v", result, err, repository)
	}
	if want := worker.now().UTC().Add(2 * time.Second); !repository.retryAt.Equal(want) {
		t.Fatalf("retryAt = %v, want %v", repository.retryAt, want)
	}

	repository, _ = workerFixture(2)
	worker = newTestWorker(repository, senderFake{err: errors.New("provider unavailable")})
	result, err = worker.RunOnce(context.Background())
	if err != nil || result.Failed != 1 || repository.failed != 1 || repository.retried != 0 {
		t.Fatalf("terminal result = %#v, %v; repository = %#v", result, err, repository)
	}
}

func TestWorkerDefersUnavailableDependenciesWithoutConsumingAttempt(t *testing.T) {
	repository, _ := workerFixture(2)
	repository.getErr = errors.New("database unavailable")
	worker := newTestWorker(repository, senderFake{})
	result, err := worker.RunOnce(context.Background())
	if err != nil || result.Deferred != 1 || repository.failed != 0 || repository.errorCode != "campaign_unavailable" {
		t.Fatalf("RunOnce() = %#v, %v; repository = %#v", result, err, repository)
	}

	repository, _ = workerFixture(2)
	worker = newTestWorker(repository, senderFake{err: &dependencyUnavailableError{cause: errors.New("database unavailable")}})
	result, err = worker.RunOnce(context.Background())
	if err != nil || result.Deferred != 1 || repository.failed != 0 || repository.errorCode != "dependency_unavailable" {
		t.Fatalf("dependency result = %#v, %v; repository = %#v", result, err, repository)
	}
}

func TestWorkerSettingsAndDelayBounds(t *testing.T) {
	if (WorkerSettings{}).Validate() == nil || (WorkerSettings{BatchSize: 101, Lease: time.Second, PollInterval: time.Second, MaxAttempts: 1, RetryBase: time.Second}).Validate() == nil {
		t.Fatal("invalid settings accepted")
	}
	if got := exponentialDelay(time.Hour, 100); got != time.Hour {
		t.Fatalf("capped delay = %v", got)
	}
	if got := positiveDelay(0); got != time.Second {
		t.Fatalf("positive delay = %v", got)
	}
}

func workerFixture(attempts int) (*workerRepositoryFake, *campaign_model.Campaign) {
	instanceID, campaignID := uuid.NewString(), uuid.NewString()
	campaign := &campaign_model.Campaign{ID: campaignID, InstanceID: instanceID, ContentType: "text", TextBody: "hello"}
	claim := uuid.NewString()
	return &workerRepositoryFake{
		campaign: campaign,
		recipients: []campaign_model.Recipient{{
			ID: uuid.NewString(), CampaignID: campaignID, InstanceID: instanceID, RecipientJID: "15550001@s.whatsapp.net",
			Status: campaign_model.RecipientStatusProcessing, ClaimToken: &claim, AttemptCount: attempts,
		}},
	}, campaign
}

func newTestWorker(repository workerRepository, sender Sender) *Worker {
	worker := NewWorker(repository, sender, WorkerSettings{BatchSize: 10, Lease: time.Minute, PollInterval: time.Second, MaxAttempts: 3, RetryBase: time.Second}, nil)
	worker.now = func() time.Time { return time.Unix(1_000, 0) }
	return worker
}

func retryDelay(err error) time.Duration {
	delay, _ := retryAfter(err)
	return delay
}
