package rabbitmq_producer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	eventpayload "github.com/evolution-foundation/evolution-go/pkg/events/payload"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	"github.com/gomessguii/logger"
	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

type rabbitMQProducer struct {
	connGate           chan struct{}
	conn               *amqp.Connection
	amqpGlobalEnabled  bool
	amqpGlobalEvents   []string
	amqpSpecificEvents []string
	connStr            string
	loggerWrapper      *logger_wrapper.LoggerManager
}

const publisherConfirmTimeout = 5 * time.Second

type ConfirmedDeliveryError struct {
	Code      string
	Retryable bool
	Cause     error
}

func (e *ConfirmedDeliveryError) Error() string           { return "RabbitMQ delivery was not confirmed" }
func (e *ConfirmedDeliveryError) Unwrap() error           { return e.Cause }
func (e *ConfirmedDeliveryError) DeliveryCode() string    { return e.Code }
func (e *ConfirmedDeliveryError) DeliveryRetryable() bool { return e.Retryable }

type messagePublisher interface {
	Publish(string, string, bool, bool, amqp.Publishing) error
}

func NewRabbitMQProducer(
	conn *amqp.Connection,
	amqpGlobalEnabled bool,
	amqpGlobalEvents []string,
	amqpSpecificEvents []string,
	connStr string,
	loggerWrapper *logger_wrapper.LoggerManager,
) *rabbitMQProducer {
	producer := &rabbitMQProducer{
		connGate:           make(chan struct{}, 1),
		conn:               conn,
		amqpGlobalEnabled:  amqpGlobalEnabled,
		amqpGlobalEvents:   amqpGlobalEvents,
		amqpSpecificEvents: amqpSpecificEvents,
		connStr:            connStr,
		loggerWrapper:      loggerWrapper,
	}

	return producer
}

// maskConnectionString masks sensitive information in the connection string for logging
func (p *rabbitMQProducer) maskConnectionString(connStr string) string {
	if connStr == "" {
		return "empty"
	}

	parsedURL, err := url.Parse(connStr)
	if err != nil {
		return "invalid-url"
	}

	// Mask password if present
	if parsedURL.User != nil {
		if _, hasPassword := parsedURL.User.Password(); hasPassword {
			parsedURL.User = url.UserPassword(parsedURL.User.Username(), "***")
		}
	}

	return parsedURL.String()
}

// handleConnectionClose monitors connection close events and logs them
func (p *rabbitMQProducer) handleConnectionClose(connection *amqp.Connection) {
	if connection == nil {
		return
	}

	closeChan := make(chan *amqp.Error)
	connection.NotifyClose(closeChan)

	closeErr := <-closeChan
	if closeErr != nil {
		logger.LogWarn("RabbitMQ connection closed unexpectedly: %v", closeErr)
		logger.LogInfo("Connection will be re-established on next message send")
	} else {
		logger.LogInfo("RabbitMQ connection closed gracefully")
	}
}

// reconnectLocked replaces p.conn while the connection gate is held.
func (p *rabbitMQProducer) reconnectLocked(ctx context.Context) error {
	if p.connStr == "" {
		return fmt.Errorf("connection string is empty - RabbitMQ URL not configured")
	}

	logger.LogInfo("Starting RabbitMQ reconnection process with URL: %s", p.maskConnectionString(p.connStr))

	var err error
	for i := 0; i < 3; i++ {
		logger.LogInfo("Tentando reconectar ao RabbitMQ (tentativa %d/3)", i+1)

		// Create connection with heartbeat to prevent timeouts
		config := amqp.Config{
			Heartbeat: 30 * time.Second, // Send heartbeat every 30 seconds
			Locale:    "en_US",
			Dial: func(network, address string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, address)
			},
		}

		connection, dialErr := amqp.DialConfig(p.connStr, config)
		err = dialErr
		if err == nil {
			p.conn = connection
			logger.LogInfo("Reconectado com sucesso ao RabbitMQ com heartbeat de 30s")

			// Set up connection close notification
			go p.handleConnectionClose(connection)
			return nil
		}

		logger.LogError("Falha na tentativa %d/3 de reconexão: %v", i+1, err)
		if i < 2 { // Don't sleep on the last attempt
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return fmt.Errorf("falha ao reconectar após 3 tentativas: %v", err)
}

func (p *rabbitMQProducer) channel(ctx context.Context) (*amqp.Channel, error) {
	if p == nil || ctx == nil || p.connGate == nil {
		return nil, errors.New("RabbitMQ producer is not configured")
	}
	select {
	case p.connGate <- struct{}{}:
		defer func() { <-p.connGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if p.conn == nil || p.conn.IsClosed() {
		if err := p.reconnectLocked(ctx); err != nil {
			return nil, err
		}
	}
	return p.conn.Channel()
}

func (p *rabbitMQProducer) Health(ctx context.Context) error {
	channel, err := p.channel(ctx)
	if err != nil {
		return &ConfirmedDeliveryError{Code: "connection_unavailable", Retryable: true, Cause: err}
	}
	return channel.Close()
}

func publishAndAwaitConfirmation(
	ctx context.Context,
	publisher messagePublisher,
	confirmations <-chan amqp.Confirmation,
	queueName string,
	payload []byte,
	deliveryID string,
	timeout time.Duration,
) error {
	if ctx == nil || publisher == nil || confirmations == nil || strings.TrimSpace(queueName) == "" || timeout <= 0 {
		return errors.New("RabbitMQ publisher, confirmation stream, queue, and timeout are required")
	}
	if err := publisher.Publish("", queueName, false, false, amqp.Publishing{
		ContentType: "application/json", Body: payload, DeliveryMode: amqp.Persistent, MessageId: deliveryID,
	}); err != nil {
		return err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case confirmation, ok := <-confirmations:
		if !ok {
			return errors.New("RabbitMQ publisher confirmation stream closed")
		}
		if !confirmation.Ack {
			return errors.New("RabbitMQ broker rejected publish")
		}
		return nil
	case <-timer.C:
		return errors.New("RabbitMQ publisher confirmation timed out")
	}
}

// DeliverConfirmed performs exactly one durable publish attempt. PostgreSQL
// owns retry scheduling; a stable delivery ID is attached for consumer-side
// deduplication.
func (p *rabbitMQProducer) DeliverConfirmed(ctx context.Context, queueName string, payload []byte, mode, deliveryID string) error {
	if p == nil || ctx == nil || strings.TrimSpace(queueName) == "" || uuid.Validate(deliveryID) != nil ||
		(mode != "enabled" && mode != "global") {
		return &ConfirmedDeliveryError{Code: "invalid_delivery", Retryable: false}
	}
	safePayload, err := eventpayload.SanitizeJSON(payload)
	if err != nil {
		return &ConfirmedDeliveryError{Code: "invalid_payload", Retryable: false, Cause: err}
	}
	channel, err := p.channel(ctx)
	if err != nil {
		return &ConfirmedDeliveryError{Code: "connection_unavailable", Retryable: true, Cause: err}
	}
	defer channel.Close()
	if err := channel.Confirm(false); err != nil {
		return &ConfirmedDeliveryError{Code: "confirm_unavailable", Retryable: true, Cause: err}
	}
	if _, err := channel.QueueDeclare(queueName, true, false, false, false, amqp.Table{
		"x-queue-type": "quorum", "x-ha-policy": "all",
	}); err != nil {
		return &ConfirmedDeliveryError{Code: "queue_declare_failed", Retryable: true, Cause: err}
	}
	confirmations := channel.NotifyPublish(make(chan amqp.Confirmation, 1))
	if err := publishAndAwaitConfirmation(ctx, channel, confirmations, queueName, safePayload, deliveryID, publisherConfirmTimeout); err != nil {
		return &ConfirmedDeliveryError{Code: "publish_not_confirmed", Retryable: true, Cause: err}
	}
	return nil
}

// CreateGlobalQueues cria todas as filas globais no startup da aplicação
func (p *rabbitMQProducer) CreateGlobalQueues() error {
	if !p.amqpGlobalEnabled {
		return nil
	}

	p.loggerWrapper.GetLogger("system").LogInfo("Creating global queues for enabled events")

	channel, err := p.channel(context.Background())
	if err != nil {
		return fmt.Errorf("failed to open channel: %v", err)
	}
	defer channel.Close()

	args := amqp.Table{
		"x-queue-type": "quorum",
		"x-ha-policy":  "all", // Alta disponibilidade
	}

	createdQueues := 0

	// AMQP_SPECIFIC_EVENTS tem prioridade sobre AMQP_GLOBAL_EVENTS
	if len(p.amqpSpecificEvents) > 0 {
		p.loggerWrapper.GetLogger("system").LogInfo("Using AMQP_SPECIFIC_EVENTS (priority over AMQP_GLOBAL_EVENTS)")

		// Cria filas diretas para eventos específicos
		for _, eventName := range p.amqpSpecificEvents {
			queueName := strings.ToLower(eventName)

			_, err = channel.QueueDeclare(
				queueName, // name
				true,      // durable
				false,     // delete when unused
				false,     // exclusive
				false,     // no-wait
				args,      // arguments
			)
			if err != nil {
				p.loggerWrapper.GetLogger("system").LogError("Failed to create specific queue %s: %v", queueName, err)
				return fmt.Errorf("failed to create specific queue %s: %v", queueName, err)
			}
			p.loggerWrapper.GetLogger("system").LogInfo("Specific queue created: %s", queueName)
			createdQueues++
		}
	} else {
		p.loggerWrapper.GetLogger("system").LogInfo("Using AMQP_GLOBAL_EVENTS (fallback mode)")

		// Mapeia eventos globais para os eventos originais que precisam de filas (modo antigo)
		eventMap := map[string][]string{
			"MESSAGE":       {"message"},
			"SEND_MESSAGE":  {"sendmessage"},
			"READ_RECEIPT":  {"receipt"},
			"PRESENCE":      {"presence"},
			"HISTORY_SYNC":  {"historysync"},
			"CHAT_PRESENCE": {"chatpresence", "archive"},
			"CALL":          {"calloffer", "callaccept", "callterminate", "calloffernotice", "callrelaylatency"},
			"CONNECTION":    {"connected", "pairsuccess", "temporaryban", "loggedout", "connectfailure", "disconnected"},
			"LABEL":         {"labeledit", "labelassociationchat", "labelassociationmessage"},
			"CONTACT":       {"contact", "pushname"},
			"GROUP":         {"groupinfo", "joinedgroup"},
			"NEWSLETTER":    {"newsletterjoin", "newsletterleave"},
			"QRCODE":        {"qrcode", "qrtimeout", "qrsuccess"},
		}

		for _, globalEvent := range p.amqpGlobalEvents {
			if queueNames, exists := eventMap[globalEvent]; exists {
				for _, queueName := range queueNames {
					_, err = channel.QueueDeclare(
						queueName, // name
						true,      // durable
						false,     // delete when unused
						false,     // exclusive
						false,     // no-wait
						args,      // arguments
					)
					if err != nil {
						p.loggerWrapper.GetLogger("system").LogError("Failed to create global queue %s: %v", queueName, err)
						return fmt.Errorf("failed to create global queue %s: %v", queueName, err)
					}
					p.loggerWrapper.GetLogger("system").LogInfo("Global queue created: %s", queueName)
					createdQueues++
				}
			}
		}
	}

	p.loggerWrapper.GetLogger("system").LogInfo("Successfully created %d global queues", createdQueues)
	return nil
}
