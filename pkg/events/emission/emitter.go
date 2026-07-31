package emission

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	event_outbox "github.com/evolution-foundation/evolution-go/pkg/events/outbox"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	event_types "github.com/evolution-foundation/evolution-go/pkg/internal/event_types"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/google/uuid"
)

type durableEventBuilder interface {
	Build(string, string, any) (*projection_model.DurableEvent, error)
}

type atomicRecorder interface {
	Record(context.Context, *projection_model.DurableEvent, []event_outbox.Delivery) error
}

type Observer interface {
	ObserveEmission(string, string, int)
	ObserveRoute(event_outbox.Transport, event_outbox.Destination)
}

type noopObserver struct{}

func (noopObserver) ObserveEmission(string, string, int)                           {}
func (noopObserver) ObserveRoute(event_outbox.Transport, event_outbox.Destination) {}

type Settings struct {
	GlobalWebhookEnabled bool
	GlobalRabbitEnabled  bool
	AMQPGlobalEvents     []string
	AMQPSpecificEvents   []string
}

type Event struct {
	Instance  *instance_model.Instance
	Type      string
	QueueName string
	Raw       any
	Payload   []byte
}

// Emitter is the single application boundary for durable event history and
// restart-safe Webhook and RabbitMQ routes.
type Emitter struct {
	builder              durableEventBuilder
	recorder             atomicRecorder
	globalWebhookEnabled bool
	globalRabbitEnabled  bool
	amqpGlobalEvents     []string
	amqpSpecificEvents   []string
	observer             Observer
}

func NewEmitter(builder durableEventBuilder, recorder atomicRecorder, settings Settings, observer Observer) (*Emitter, error) {
	if builder == nil || recorder == nil {
		return nil, errors.New("external event emitter builder and recorder are required")
	}
	if observer == nil {
		observer = noopObserver{}
	}
	return &Emitter{
		builder: builder, recorder: recorder,
		globalWebhookEnabled: settings.GlobalWebhookEnabled,
		globalRabbitEnabled:  settings.GlobalRabbitEnabled,
		amqpGlobalEvents:     append([]string(nil), settings.AMQPGlobalEvents...),
		amqpSpecificEvents:   append([]string(nil), settings.AMQPSpecificEvents...),
		observer:             observer,
	}, nil
}

func (e *Emitter) Record(ctx context.Context, input Event) error {
	if e == nil || e.builder == nil || e.recorder == nil || ctx == nil || input.Instance == nil ||
		uuid.Validate(input.Instance.Id) != nil || strings.TrimSpace(input.Type) == "" || strings.TrimSpace(input.QueueName) == "" ||
		len(input.Payload) == 0 || !json.Valid(input.Payload) {
		return errors.New("complete external event emission input is required")
	}
	durableEvent, err := e.builder.Build(input.Instance.Id, input.Type, input.Raw)
	if err != nil {
		e.observer.ObserveEmission("history_only", "failed", 0)
		return err
	}
	deliveries := e.Plan(input)
	mode := "history_only"
	if len(deliveries) > 0 {
		mode = "routed"
	}
	if err := e.recorder.Record(ctx, durableEvent, deliveries); err != nil {
		e.observer.ObserveEmission(mode, "failed", len(deliveries))
		return err
	}
	e.observer.ObserveEmission(mode, "success", len(deliveries))
	for _, delivery := range deliveries {
		e.observer.ObserveRoute(delivery.Transport, delivery.Destination)
	}
	return nil
}

func (e *Emitter) Plan(input Event) []event_outbox.Delivery {
	if e == nil || input.Instance == nil {
		return nil
	}
	subscribed := InstanceSubscribed(input.Instance.Events, input.Type, input.Payload)
	deliveries := make([]event_outbox.Delivery, 0, 4)
	appendDelivery := func(transport event_outbox.Transport, destination event_outbox.Destination, routingKey string) {
		deliveries = append(deliveries, event_outbox.Delivery{
			ID: uuid.NewString(), Transport: transport, Destination: destination,
			RoutingKey: routingKey, Payload: append(json.RawMessage(nil), input.Payload...),
		})
	}
	if subscribed && instanceWebhookEnabled(input.Instance.Webhook) {
		if e.globalWebhookEnabled {
			appendDelivery(event_outbox.TransportWebhook, event_outbox.DestinationGlobal, input.QueueName)
		}
		appendDelivery(event_outbox.TransportWebhook, event_outbox.DestinationInstance, input.QueueName)
	}
	if subscribed && instanceRabbitEnabled(input.Instance.RabbitmqEnable) {
		appendDelivery(event_outbox.TransportRabbitMQ, event_outbox.DestinationInstance, input.QueueName)
	}
	if queue, ok := e.GlobalRabbitQueue(input.Type); ok {
		appendDelivery(event_outbox.TransportRabbitMQ, event_outbox.DestinationGlobal, queue)
	}
	return deliveries
}

func (e *Emitter) GlobalRabbitQueue(eventType string) (string, bool) {
	if e == nil || !e.globalRabbitEnabled {
		return "", false
	}
	if len(e.amqpSpecificEvents) > 0 {
		if exactContains(e.amqpSpecificEvents, eventType) {
			return strings.ToLower(eventType), true
		}
		return "", false
	}
	group := EventGroup(eventType)
	if group != "" && exactContains(e.amqpGlobalEvents, group) {
		return strings.ToLower(eventType), true
	}
	return "", false
}

func InstanceSubscribed(configured, eventType string, payload []byte) bool {
	subscriptions := validSubscriptions(configured)
	if foldedContains(subscriptions, event_types.ALL) {
		return true
	}
	switch eventType {
	case "Message":
		return foldedContains(subscriptions, event_types.MESSAGE) || chatSubscription(subscriptions, payload, true)
	case "SendMessage":
		return foldedContains(subscriptions, event_types.SEND_MESSAGE) || chatSubscription(subscriptions, payload, true)
	case "Receipt":
		return foldedContains(subscriptions, event_types.READ_RECEIPT) || chatSubscription(subscriptions, payload, false)
	case "Presence":
		return foldedContains(subscriptions, event_types.PRESENCE)
	case "HistorySync":
		return foldedContains(subscriptions, event_types.HISTORY_SYNC)
	case "ChatPresence", "Archive":
		return foldedContains(subscriptions, event_types.CHAT_PRESENCE)
	case "CallOffer", "CallAccept", "CallTerminate", "CallOfferNotice", "CallRelayLatency":
		return foldedContains(subscriptions, event_types.CALL)
	case "Connected", "PairSuccess", "TemporaryBan", "LoggedOut", "ConnectFailure", "Disconnected":
		return foldedContains(subscriptions, event_types.CONNECTION)
	case "LabelEdit", "LabelAssociationChat", "LabelAssociationMessage":
		return foldedContains(subscriptions, event_types.LABEL)
	case "Contact", "PushName":
		return foldedContains(subscriptions, event_types.CONTACT)
	case "Picture":
		return foldedContains(subscriptions, event_types.PICTURE)
	case "UserAbout":
		return foldedContains(subscriptions, event_types.USER_ABOUT)
	case "GroupInfo", "JoinedGroup":
		return foldedContains(subscriptions, event_types.GROUP)
	case "NewsletterJoin", "NewsletterLeave":
		return foldedContains(subscriptions, event_types.NEWSLETTER)
	case "QRCode", "QRTimeout", "QRSuccess":
		return foldedContains(subscriptions, event_types.QRCODE)
	case "ButtonClick":
		return foldedContains(subscriptions, event_types.BUTTON_CLICK) || foldedContains(subscriptions, event_types.MESSAGE)
	default:
		return false
	}
}

func EventGroup(eventType string) string {
	switch eventType {
	case "Message":
		return event_types.MESSAGE
	case "SendMessage":
		return event_types.SEND_MESSAGE
	case "Receipt":
		return event_types.READ_RECEIPT
	case "Presence":
		return event_types.PRESENCE
	case "HistorySync":
		return event_types.HISTORY_SYNC
	case "ChatPresence", "Archive":
		return event_types.CHAT_PRESENCE
	case "CallOffer", "CallAccept", "CallTerminate", "CallOfferNotice", "CallRelayLatency":
		return event_types.CALL
	case "Connected", "PairSuccess", "TemporaryBan", "LoggedOut", "ConnectFailure", "Disconnected":
		return event_types.CONNECTION
	case "LabelEdit", "LabelAssociationChat", "LabelAssociationMessage":
		return event_types.LABEL
	case "Contact", "PushName":
		return event_types.CONTACT
	case "Picture":
		return event_types.PICTURE
	case "UserAbout":
		return event_types.USER_ABOUT
	case "GroupInfo", "JoinedGroup":
		return event_types.GROUP
	case "NewsletterJoin", "NewsletterLeave":
		return event_types.NEWSLETTER
	case "QRCode", "QRTimeout", "QRSuccess":
		return event_types.QRCODE
	default:
		return ""
	}
}

func validSubscriptions(configured string) []string {
	parts := strings.Split(configured, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if event_types.IsEventType(part) && !foldedContains(result, part) {
			result = append(result, part)
		}
	}
	return result
}

func chatSubscription(subscriptions []string, payload []byte, nestedInfo bool) bool {
	var envelope map[string]any
	if json.Unmarshal(payload, &envelope) != nil {
		return false
	}
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		return false
	}
	if nestedInfo {
		data, ok = data["Info"].(map[string]any)
		if !ok {
			return false
		}
	}
	chat, _ := data["Chat"].(string)
	return (strings.HasSuffix(chat, "@g.us") && foldedContains(subscriptions, event_types.GROUP)) ||
		(strings.HasSuffix(chat, "@newsletter") && foldedContains(subscriptions, event_types.NEWSLETTER))
}

func instanceWebhookEnabled(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value != "disabled"
}

func instanceRabbitEnabled(value string) bool {
	return value == "enabled" || value == "true"
}

func exactContains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func foldedContains(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(value, expected) {
			return true
		}
	}
	return false
}
