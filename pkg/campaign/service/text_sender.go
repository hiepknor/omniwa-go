package campaign_service

import (
	"context"
	"errors"
	"strings"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	send_service "github.com/evolution-foundation/evolution-go/pkg/sendMessage/service"
)

type instanceReader interface {
	GetInstanceByID(string) (*instance_model.Instance, error)
}

type textSendService interface {
	SendText(*send_service.TextStruct, *instance_model.Instance) (*send_service.MessageSendStruct, error)
}

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
	result, err := s.sends.SendText(&send_service.TextStruct{
		Number: recipient.RecipientJID,
		Text:   campaign.TextBody,
		Id:     deterministicMessageID(recipient.ID),
	}, instance)
	if err != nil {
		return "", err
	}
	if result == nil || result.Info.ID == "" {
		return "", errors.New("campaign send returned no provider message identity")
	}
	return string(result.Info.ID), nil
}

func deterministicMessageID(recipientID string) string {
	return strings.ToUpper(strings.ReplaceAll(recipientID, "-", ""))
}
