package label_service

import (
	"context"
	"errors"
	"strconv"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	label_model "github.com/evolution-foundation/evolution-go/pkg/label/model"
	label_repository "github.com/evolution-foundation/evolution-go/pkg/label/repository"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/evolution-foundation/evolution-go/pkg/utils"
	whatsmeow_service "github.com/evolution-foundation/evolution-go/pkg/whatsmeow/service"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
)

type LabelService interface {
	ChatLabel(data *ChatLabelStruct, instance *instance_model.Instance) error
	MessageLabel(data *MessageLabelStruct, instance *instance_model.Instance) error
	EditLabel(data *EditLabelStruct, instance *instance_model.Instance) error
	ChatUnlabel(data *ChatLabelStruct, instance *instance_model.Instance) error
	MessageUnlabel(data *MessageLabelStruct, instance *instance_model.Instance) error
	GetLabels(context.Context, *instance_model.Instance) ([]label_model.Label, error)
	GetLabel(context.Context, *instance_model.Instance, string) (*label_model.Label, *projection_service.ProjectionReadMeta, error)
}

type labelService struct {
	clientPointer    map[string]*whatsmeow.Client
	whatsmeowService whatsmeow_service.WhatsmeowService
	labelRepository  label_repository.LabelRepository
	projectionReader *projection_service.LabelReader
	projectionWriter *projection_service.LabelWriter
	loggerWrapper    *logger_wrapper.LoggerManager
}

const labelProjectionWriteTimeout = 5 * time.Second

type ChatLabelStruct struct {
	JID     string `json:"jid"`
	LabelID string `json:"labelId"`
}

type MessageLabelStruct struct {
	JID       string `json:"jid"`
	LabelID   string `json:"labelId"`
	MessageID string `json:"messageId"`
}

type EditLabelStruct struct {
	LabelID string `json:"labelId"`
	Name    string `json:"name"`
	Color   int    `json:"color"`
	Deleted bool   `json:"deleted"`
}

func (l *labelService) ensureClientConnected(instanceId string) (*whatsmeow.Client, error) {
	client := l.clientPointer[instanceId]
	l.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] Checking client connection status - Client exists: %v", instanceId, client != nil)

	if client == nil {
		l.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] No client found, attempting to start new instance", instanceId)
		err := l.whatsmeowService.StartInstance(instanceId)
		if err != nil {
			l.loggerWrapper.GetLogger(instanceId).LogError("[%s] Failed to start instance: %v", instanceId, err)
			return nil, errors.New("no active session found")
		}

		l.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] Instance started, waiting 2 seconds...", instanceId)
		time.Sleep(2 * time.Second)

		client = l.clientPointer[instanceId]
		l.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] Checking new client - Exists: %v, Connected: %v",
			instanceId,
			client != nil,
			client != nil && client.IsConnected())

		if client == nil || !client.IsConnected() {
			l.loggerWrapper.GetLogger(instanceId).LogError("[%s] New client validation failed - Exists: %v, Connected: %v",
				instanceId,
				client != nil,
				client != nil && client.IsConnected())
			return nil, errors.New("no active session found")
		}
	} else if !client.IsConnected() {
		l.loggerWrapper.GetLogger(instanceId).LogError("[%s] Existing client is disconnected - Connected status: %v",
			instanceId,
			client.IsConnected())
		return nil, errors.New("client disconnected")
	}

	l.loggerWrapper.GetLogger(instanceId).LogInfo("[%s] Client successfully validated - Connected: %v", instanceId, client.IsConnected())
	return client, nil
}

func (l *labelService) ChatLabel(data *ChatLabelStruct, instance *instance_model.Instance) error {
	client, err := l.ensureClientConnected(instance.Id)
	if err != nil {
		return err
	}

	jid, ok := utils.ParseJID(data.JID)
	if !ok {
		l.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error parse community jid", instance.Id)
		return errors.New("error parse community jid")
	}

	err = client.SendAppState(context.Background(), appstate.BuildLabelChat(
		jid,
		data.LabelID,
		true,
	))
	if err != nil {
		l.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error label chat: %v", instance.Id, err)
		return err
	}
	l.writeProjection(instance.Id, func(ctx context.Context) error {
		return l.projectionWriter.WriteChatAssociation(ctx, instance.Id, data.LabelID, jid.String(), true)
	})

	return nil
}

func (l *labelService) MessageLabel(data *MessageLabelStruct, instance *instance_model.Instance) error {
	client, err := l.ensureClientConnected(instance.Id)
	if err != nil {
		return err
	}

	jid, ok := utils.ParseJID(data.JID)
	if !ok {
		l.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error parse community jid", instance.Id)
		return errors.New("error parse community jid")
	}

	err = client.SendAppState(context.Background(), appstate.BuildLabelMessage(
		jid,
		data.LabelID,
		data.MessageID,
		true,
	))
	if err != nil {
		l.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error label message: %v", instance.Id, err)
		return err
	}
	l.writeProjection(instance.Id, func(ctx context.Context) error {
		return l.projectionWriter.WriteMessageAssociation(ctx, instance.Id, data.LabelID, jid.String(), data.MessageID, true)
	})

	return nil
}

func (l *labelService) EditLabel(data *EditLabelStruct, instance *instance_model.Instance) error {
	client, err := l.ensureClientConnected(instance.Id)
	if err != nil {
		return err
	}

	err = client.SendAppState(context.Background(), appstate.BuildLabelEdit(
		data.LabelID,
		data.Name,
		int32(data.Color),
		data.Deleted,
	))
	if err != nil {
		l.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error label message: %v", instance.Id, err)
		return err
	}
	l.writeProjection(instance.Id, func(ctx context.Context) error {
		return l.projectionWriter.WriteLabel(ctx, instance.Id, data.LabelID, data.Name, int32(data.Color), data.Deleted)
	})

	return nil
}

func (l *labelService) ChatUnlabel(data *ChatLabelStruct, instance *instance_model.Instance) error {
	client, err := l.ensureClientConnected(instance.Id)
	if err != nil {
		return err
	}

	jid, ok := utils.ParseJID(data.JID)
	if !ok {
		l.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error parse community jid", instance.Id)
		return errors.New("error parse community jid")
	}

	err = client.SendAppState(context.Background(), appstate.BuildLabelChat(
		jid,
		data.LabelID,
		false,
	))
	if err != nil {
		l.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error label chat: %v", instance.Id, err)
		return err
	}
	l.writeProjection(instance.Id, func(ctx context.Context) error {
		return l.projectionWriter.WriteChatAssociation(ctx, instance.Id, data.LabelID, jid.String(), false)
	})

	return nil
}

func (l *labelService) MessageUnlabel(data *MessageLabelStruct, instance *instance_model.Instance) error {
	client, err := l.ensureClientConnected(instance.Id)
	if err != nil {
		return err
	}

	jid, ok := utils.ParseJID(data.JID)
	if !ok {
		l.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error parse community jid", instance.Id)
		return errors.New("error parse community jid")
	}

	err = client.SendAppState(context.Background(), appstate.BuildLabelMessage(
		jid,
		data.LabelID,
		data.MessageID,
		false,
	))
	if err != nil {
		l.loggerWrapper.GetLogger(instance.Id).LogError("[%s] error label message: %v", instance.Id, err)
		return err
	}
	l.writeProjection(instance.Id, func(ctx context.Context) error {
		return l.projectionWriter.WriteMessageAssociation(ctx, instance.Id, data.LabelID, jid.String(), data.MessageID, false)
	})

	return nil
}

func (l *labelService) writeProjection(instanceID string, write func(context.Context) error) {
	if l.projectionWriter == nil || write == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), labelProjectionWriteTimeout)
	defer cancel()
	if err := write(ctx); err != nil {
		l.loggerWrapper.GetLogger(instanceID).LogError("component=projection action=write_through instance_id=%s resource=labels result=failed error_code=projection_write_failed", instanceID)
		if staleErr := l.projectionWriter.MarkStale(instanceID); staleErr != nil {
			l.loggerWrapper.GetLogger(instanceID).LogError("component=projection action=mark_stale instance_id=%s resource=labels result=failed error_code=projection_state_write_failed", instanceID)
		}
	}
}

func (l *labelService) GetLabels(ctx context.Context, instance *instance_model.Instance) ([]label_model.Label, error) {
	if instance == nil || l.projectionReader == nil {
		return nil, errors.New("label projection reader and instance are required")
	}
	labels, _, err := l.projectionReader.List(ctx, instance.Id)
	if err != nil {
		return nil, err
	}
	result := make([]label_model.Label, len(labels))
	for index := range labels {
		result[index] = legacyLabelFromProjection(instance.Id, &labels[index])
	}
	return result, nil
}

func (l *labelService) GetLabel(ctx context.Context, instance *instance_model.Instance, labelID string) (*label_model.Label, *projection_service.ProjectionReadMeta, error) {
	if instance == nil || l.projectionReader == nil {
		return nil, nil, errors.New("label projection reader and instance are required")
	}
	label, meta, err := l.projectionReader.Get(ctx, instance.Id, labelID)
	if err != nil {
		return nil, nil, err
	}
	if label == nil {
		return nil, nil, errors.New("projected label is missing")
	}
	result := legacyLabelFromProjection(instance.Id, label)
	return &result, meta, nil
}

func legacyLabelFromProjection(instanceID string, label *projection_model.Label) label_model.Label {
	result := label_model.Label{Id: label.LabelID, InstanceID: instanceID, LabelID: label.LabelID}
	if label.Name != nil {
		result.LabelName = *label.Name
	}
	if label.Color != nil {
		result.LabelColor = strconv.FormatInt(int64(*label.Color), 10)
	}
	if label.PredefinedID != nil {
		result.PredefinedId = strconv.FormatInt(int64(*label.PredefinedID), 10)
	}
	return result
}

func NewLabelService(
	clientPointer map[string]*whatsmeow.Client,
	whatsmeowService whatsmeow_service.WhatsmeowService,
	labelRepository label_repository.LabelRepository,
	projectionReader *projection_service.LabelReader,
	projectionWriter *projection_service.LabelWriter,
	loggerWrapper *logger_wrapper.LoggerManager,
) LabelService {
	return &labelService{
		clientPointer:    clientPointer,
		whatsmeowService: whatsmeowService,
		labelRepository:  labelRepository,
		projectionReader: projectionReader,
		projectionWriter: projectionWriter,
		loggerWrapper:    loggerWrapper,
	}
}
