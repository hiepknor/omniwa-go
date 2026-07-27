package campaign_service

import (
	"context"
	"errors"
	"testing"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/outbound"
	send_service "github.com/evolution-foundation/evolution-go/pkg/sendMessage/service"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

type instanceReaderFake struct {
	instance *instance_model.Instance
	err      error
}

func (f instanceReaderFake) GetInstanceByID(string) (*instance_model.Instance, error) {
	return f.instance, f.err
}

type textSendServiceFake struct {
	input *send_service.TextStruct
	info  *send_service.MessageSendStruct
	err   error
}

func (f *textSendServiceFake) SendText(input *send_service.TextStruct, _ *instance_model.Instance) (*send_service.MessageSendStruct, error) {
	f.input = input
	return f.info, f.err
}

func (f *textSendServiceFake) SendTextOnce(_ context.Context, input *send_service.TextStruct, _ *instance_model.Instance) (*send_service.MessageSendStruct, error) {
	f.input = input
	return f.info, f.err
}

func TestTextSenderUsesNormalizedJobAndDeterministicIdentity(t *testing.T) {
	instanceID, campaignID, recipientID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	sends := &textSendServiceFake{info: &send_service.MessageSendStruct{Info: types.MessageInfo{ID: "provider-id"}}}
	sender := NewTextSender(instanceReaderFake{instance: &instance_model.Instance{Id: instanceID}}, sends)
	providerID, err := sender.Send(context.Background(),
		&campaign_model.Campaign{ID: campaignID, InstanceID: instanceID, ContentType: "text", TextBody: "hello"},
		&campaign_model.Recipient{ID: recipientID, CampaignID: campaignID, InstanceID: instanceID, RecipientJID: "15550001@s.whatsapp.net"})
	if err != nil || providerID != "provider-id" {
		t.Fatalf("Send() = %q, %v", providerID, err)
	}
	if sends.input == nil || sends.input.Number != "15550001@s.whatsapp.net" || sends.input.Text != "hello" || sends.input.Id != deterministicMessageID(recipientID) {
		t.Fatalf("send input = %#v", sends.input)
	}
}

func TestTextSenderRejectsInvalidOrCancelledJobs(t *testing.T) {
	sender := NewTextSender(instanceReaderFake{}, &textSendServiceFake{})
	if _, err := sender.Send(context.Background(), nil, nil); err == nil {
		t.Fatal("invalid job accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	instanceID, campaignID := uuid.NewString(), uuid.NewString()
	_, err := sender.Send(ctx, &campaign_model.Campaign{ID: campaignID, InstanceID: instanceID, ContentType: "text", TextBody: "hello"},
		&campaign_model.Recipient{ID: uuid.NewString(), CampaignID: campaignID, InstanceID: instanceID, RecipientJID: "15550001@s.whatsapp.net"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestTextSenderClassifiesInstanceLookupAsDependencyDeferral(t *testing.T) {
	sender := NewTextSender(instanceReaderFake{err: errors.New("database unavailable")}, &textSendServiceFake{})
	instanceID, campaignID := uuid.NewString(), uuid.NewString()
	_, err := sender.Send(context.Background(), &campaign_model.Campaign{ID: campaignID, InstanceID: instanceID, ContentType: "text", TextBody: "hello"},
		&campaign_model.Recipient{ID: uuid.NewString(), CampaignID: campaignID, InstanceID: instanceID, RecipientJID: "15550001@s.whatsapp.net"})
	var dependency *dependencyUnavailableError
	if !errors.As(err, &dependency) {
		t.Fatalf("dependency error = %v", err)
	}
}

func TestGroupDeliveryErrorClassificationIsConservative(t *testing.T) {
	tests := []struct {
		name string
		err  error
		kind DeliveryFailureKind
		code string
	}{
		{name: "terminal permission", err: &send_service.ProviderSendError{Cause: whatsmeow.ErrIQForbidden}, kind: DeliveryFailureTerminal, code: "send_permission_denied"},
		{name: "rate limit", err: &send_service.ProviderSendError{Cause: &outbound.RateLimitError{RetryAfter: time.Minute}}, kind: DeliveryFailureRateLimit, code: "provider_rate_limited"},
		{name: "unknown provider outcome", err: &send_service.ProviderSendError{Cause: errors.New("connection lost")}, kind: DeliveryFailureUnknown, code: "unknown_send_outcome"},
		{name: "known pre-provider transient", err: errors.New("dependency unavailable"), kind: DeliveryFailureTransient, code: "send_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var delivery *DeliveryError
			if err := classifyGroupDeliveryError(test.err); !errors.As(err, &delivery) || delivery.Kind != test.kind || delivery.Code != test.code {
				t.Fatalf("classification = %#v, %v", delivery, err)
			}
		})
	}
}
