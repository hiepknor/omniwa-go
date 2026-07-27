package campaign_service

import (
	"context"
	"errors"
	"strings"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	send_service "github.com/evolution-foundation/evolution-go/pkg/sendMessage/service"
	"go.mau.fi/whatsmeow"
)

type instanceReader interface {
	GetInstanceByID(string) (*instance_model.Instance, error)
}

type textSendService interface {
	SendText(*send_service.TextStruct, *instance_model.Instance) (*send_service.MessageSendStruct, error)
	SendTextOnce(context.Context, *send_service.TextStruct, *instance_model.Instance) (*send_service.MessageSendStruct, error)
}

type DeliveryFailureKind string

const (
	DeliveryFailureTransient DeliveryFailureKind = "transient"
	DeliveryFailureTerminal  DeliveryFailureKind = "terminal"
	DeliveryFailureRateLimit DeliveryFailureKind = "rate_limit"
	DeliveryFailureUnknown   DeliveryFailureKind = "unknown"
)

type DeliveryError struct {
	Kind       DeliveryFailureKind
	Code       string
	RetryAfter time.Duration
	Cause      error
}

func (e *DeliveryError) Error() string { return e.Code }
func (e *DeliveryError) Unwrap() error { return e.Cause }

type TextSender struct {
	instances instanceReader
	sends     textSendService
}

type dependencyUnavailableError struct{ cause error }

func (e *dependencyUnavailableError) Error() string { return "campaign send dependency unavailable" }
func (e *dependencyUnavailableError) Unwrap() error { return e.cause }

func NewTextSender(instances instanceReader, sends textSendService) *TextSender {
	return &TextSender{instances: instances, sends: sends}
}

func (s *TextSender) Send(ctx context.Context, campaign *campaign_model.Campaign, recipient *campaign_model.Recipient) (string, error) {
	if s == nil || s.instances == nil || s.sends == nil || ctx == nil || campaign == nil || recipient == nil ||
		campaign.ID == "" || campaign.InstanceID == "" || campaign.InstanceID != recipient.InstanceID || campaign.ID != recipient.CampaignID ||
		campaign.ContentType != "text" || campaign.TextBody == "" || recipient.RecipientJID == "" {
		return "", errors.New("campaign sender dependencies and normalized job are required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	instance, err := s.instances.GetInstanceByID(recipient.InstanceID)
	if err != nil {
		return "", &dependencyUnavailableError{cause: err}
	}
	data := &send_service.TextStruct{
		Number: recipient.RecipientJID,
		Text:   campaign.TextBody,
		Id:     deterministicMessageID(recipient.ID),
	}
	var result *send_service.MessageSendStruct
	if recipient.TargetType == campaign_model.RecipientTargetGroup {
		result, err = s.sends.SendTextOnce(ctx, data, instance)
	} else {
		result, err = s.sends.SendText(data, instance)
	}
	if err != nil {
		if recipient.TargetType == campaign_model.RecipientTargetGroup {
			return "", classifyGroupDeliveryError(err)
		}
		return "", err
	}
	if result == nil || result.Info.ID == "" {
		return "", errors.New("campaign send returned no provider message identity")
	}
	return string(result.Info.ID), nil
}

func classifyGroupDeliveryError(err error) error {
	if delay, limited := retryAfter(err); limited || errors.Is(err, whatsmeow.ErrIQRateOverLimit) {
		return &DeliveryError{Kind: DeliveryFailureRateLimit, Code: "provider_rate_limited", RetryAfter: positiveDelay(delay), Cause: err}
	}
	if errors.Is(err, whatsmeow.ErrIQForbidden) {
		return &DeliveryError{Kind: DeliveryFailureTerminal, Code: "send_permission_denied", Cause: err}
	}
	if errors.Is(err, whatsmeow.ErrIQNotFound) {
		return &DeliveryError{Kind: DeliveryFailureTerminal, Code: "group_access_lost", Cause: err}
	}
	if errors.Is(err, whatsmeow.ErrIQGone) {
		return &DeliveryError{Kind: DeliveryFailureTerminal, Code: "group_dissolved", Cause: err}
	}
	if errors.Is(err, whatsmeow.ErrIQNotAuthorized) {
		return &DeliveryError{Kind: DeliveryFailureTerminal, Code: "instance_not_authorized", Cause: err}
	}
	var providerError *send_service.ProviderSendError
	if errors.As(err, &providerError) {
		return &DeliveryError{Kind: DeliveryFailureUnknown, Code: "unknown_send_outcome", Cause: err}
	}
	return &DeliveryError{Kind: DeliveryFailureTransient, Code: "send_failed", Cause: err}
}

func deterministicMessageID(recipientID string) string {
	return strings.ToUpper(strings.ReplaceAll(recipientID, "-", ""))
}
