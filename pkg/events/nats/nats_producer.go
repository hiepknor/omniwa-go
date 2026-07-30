package nats_producer

import (
	"errors"
	"strings"
	"time"

	producer_interfaces "github.com/evolution-foundation/evolution-go/pkg/events/interfaces"
	eventpayload "github.com/evolution-foundation/evolution-go/pkg/events/payload"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	"github.com/gomessguii/logger"
	"github.com/nats-io/nats.go"
)

const publishFlushTimeout = 5 * time.Second

type publishConnection interface {
	Publish(string, []byte) error
	FlushTimeout(time.Duration) error
	LastError() error
}

type natsProducer struct {
	conn              publishConnection
	natsGlobalEnabled bool
	natsGlobalEvents  []string
	loggerWrapper     *logger_wrapper.LoggerManager
}

func NewNatsProducer(
	url string,
	natsGlobalEnabled bool,
	natsGlobalEvents []string,
	loggerWrapper *logger_wrapper.LoggerManager,
) producer_interfaces.Producer {
	return newNatsProducer(url, natsGlobalEnabled, natsGlobalEvents, loggerWrapper, nats.Connect)
}

func newNatsProducer(
	url string,
	natsGlobalEnabled bool,
	natsGlobalEvents []string,
	loggerWrapper *logger_wrapper.LoggerManager,
	connect func(string, ...nats.Option) (*nats.Conn, error),
) producer_interfaces.Producer {
	if strings.TrimSpace(url) == "" {
		return &natsProducer{
			conn:              nil,
			natsGlobalEnabled: false,
			natsGlobalEvents:  nil,
			loggerWrapper:     loggerWrapper,
		}
	}

	conn, err := connect(url)
	if err != nil {
		logger.LogError("Failed to connect to NATS: %v", err)
		return &natsProducer{
			conn:              nil,
			natsGlobalEnabled: false,
			natsGlobalEvents:  nil,
			loggerWrapper:     loggerWrapper,
		}
	}

	return &natsProducer{
		conn:              conn,
		natsGlobalEnabled: natsGlobalEnabled,
		natsGlobalEvents:  natsGlobalEvents,
		loggerWrapper:     loggerWrapper,
	}
}

func (p *natsProducer) Produce(
	queueName string,
	payload []byte,
	natsEnable string,
	userID string,
) error {
	safePayload, err := eventpayload.SanitizeJSON(payload)
	if err != nil {
		return err
	}
	p.loggerWrapper.GetLogger(userID).LogInfo("[%s] NATS Producer - Starting produce for subject: %s", userID, queueName)
	p.loggerWrapper.GetLogger(userID).LogInfo("[%s] NATS Producer - Global enabled: %v", userID, p.natsGlobalEnabled)

	if p.conn == nil {
		p.loggerWrapper.GetLogger(userID).LogWarn("[%s] NATS connection is nil", userID)
		if natsEnable == "global" || natsEnable == "enabled" {
			return errors.New("NATS connection is unavailable")
		}
		return nil
	}

	if natsEnable == "global" {
		p.loggerWrapper.GetLogger(userID).LogInfo("[%s] Publishing to global subject: %s", userID, queueName)
		err := publishAndFlush(p.conn, queueName, safePayload, publishFlushTimeout)
		if err != nil {
			p.loggerWrapper.GetLogger(userID).LogError("[%s] Failed to publish message to subject %s: %v", userID, queueName, err)
			return err
		}
		p.loggerWrapper.GetLogger(userID).LogInfo("[%s] Message published successfully to subject: %s", userID, queueName)
	}

	if natsEnable == "enabled" {
		err := publishAndFlush(p.conn, queueName, safePayload, publishFlushTimeout)
		if err != nil {
			p.loggerWrapper.GetLogger(userID).LogError("[%s] Failed to publish message to instance subject %s: %v", userID, queueName, err)
			return err
		}
		p.loggerWrapper.GetLogger(userID).LogInfo("[%s] Message published successfully to instance subject: %s", userID, queueName)
	}

	return nil
}

func publishAndFlush(conn publishConnection, subject string, payload []byte, timeout time.Duration) error {
	if conn == nil {
		return errors.New("NATS connection is unavailable")
	}
	if strings.TrimSpace(subject) == "" || timeout <= 0 {
		return errors.New("NATS subject and positive flush timeout are required")
	}
	if err := conn.Publish(subject, payload); err != nil {
		return err
	}
	if err := conn.FlushTimeout(timeout); err != nil {
		return err
	}
	return conn.LastError()
}

// CreateGlobalQueues não faz nada para NATS producer pois os subjects são criados dinamicamente
func (p *natsProducer) CreateGlobalQueues() error {
	return nil
}
