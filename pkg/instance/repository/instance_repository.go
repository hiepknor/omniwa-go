package instance_repository

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/gomessguii/logger"
	"github.com/google/uuid"
	"gorm.io/gorm"
	gorm_logger "gorm.io/gorm/logger"

	label_model "github.com/evolution-foundation/evolution-go/pkg/label/model"
	label_repository "github.com/evolution-foundation/evolution-go/pkg/label/repository"

	message_model "github.com/evolution-foundation/evolution-go/pkg/message/model"
	message_repository "github.com/evolution-foundation/evolution-go/pkg/message/repository"
)

type InstanceRepository interface {
	Create(instance instance_model.Instance) (*instance_model.Instance, error)
	GetInstanceByID(instanceId string) (*instance_model.Instance, error)
	GetConnectedInstanceByID(instanceId string) (*instance_model.Instance, error)
	GetInstanceByToken(token string) (*instance_model.Instance, error)
	GetInstanceByName(name string) (*instance_model.Instance, error)
	Update(*instance_model.Instance) error
	UpdateConnected(userId string, status bool, disconnectReason string) error
	UpdateQrcode(userId string, qr string) error
	UpdateProxy(userId string, proxy string) error
	UpdateJid(userId string, jid string) error
	GetAllConnectedInstances() ([]*instance_model.Instance, error)
	GetAllConnectedInstancesByClientName(clientName string) ([]*instance_model.Instance, error)
	GetAll(clientName string) ([]*instance_model.Instance, error)
	Delete(instanceId string) error
	GetAdvancedSettings(instanceId string) (*instance_model.AdvancedSettings, error)
	UpdateAdvancedSettings(instanceId string, settings *instance_model.AdvancedSettings) error
}

type instanceRepository struct {
	db            *gorm.DB
	tokenDigester TokenDigester
	labelRepo     label_repository.LabelRepository
	messageRepo   message_repository.MessageRepository
}

type TokenDigester interface {
	Digest(token string) (digest string, keyVersion int, err error)
}

type TokenBackfiller interface {
	BackfillTokenDigests(ctx context.Context, batchSize int) (int, error)
}

var (
	ErrTokenRotationUnavailable = errors.New("instance token rotation is unavailable")
	ErrTokenRotationNotFound    = errors.New("instance was not found")
	ErrTokenRotationConflict    = errors.New("instance token rotation conflicted")
	ErrInvalidTokenRotation     = errors.New("valid instance token rotation is required")
)

var safeTokenRotationRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{16,64}$`)

type TokenRotation struct {
	InstanceID         string
	ExpectedGeneration int64
	NewToken           string
	Reason             string
	ActorReferenceHash string
	RequestID          string
	OccurredAt         time.Time
}

type TokenRotationResult struct {
	InstanceID      string
	TokenGeneration int64
	RotatedAt       time.Time
}

type TokenRotator interface {
	RotateToken(ctx context.Context, rotation TokenRotation) (*TokenRotationResult, error)
}

func (i *instanceRepository) setTokenDigest(instance *instance_model.Instance) error {
	if i.tokenDigester == nil {
		return nil
	}
	digest, version, err := i.tokenDigester.Digest(instance.Token)
	if err != nil {
		return err
	}
	instance.TokenDigest = &digest
	instance.TokenKeyVersion = &version
	return nil
}

func (i *instanceRepository) credentialDB() *gorm.DB {
	return i.db.Session(&gorm.Session{Logger: i.db.Logger.LogMode(gorm_logger.Silent)})
}

func (i *instanceRepository) Create(instance instance_model.Instance) (*instance_model.Instance, error) {
	if err := i.setTokenDigest(&instance); err != nil {
		return nil, fmt.Errorf("derive instance token digest: %w", err)
	}
	if err := i.credentialDB().Create(&instance).Error; err != nil {
		return nil, err
	}
	return &instance, nil
}

func (i *instanceRepository) GetInstanceByToken(token string) (*instance_model.Instance, error) {
	if token == "" {
		return nil, errors.New("instance token is required")
	}
	var instance instance_model.Instance
	if i.tokenDigester != nil {
		digest, version, err := i.tokenDigester.Digest(token)
		if err != nil {
			return nil, fmt.Errorf("derive instance token digest: %w", err)
		}
		err = i.credentialDB().Where("token_key_version = ? AND token_digest = ?", version, digest).First(&instance).Error
		if err == nil {
			return &instance, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	err := i.credentialDB().Where("token = ?", token).First(&instance).Error
	if err != nil {
		return nil, err
	}

	return &instance, nil
}

func (i *instanceRepository) GetInstanceByName(name string) (*instance_model.Instance, error) {
	var instance instance_model.Instance
	err := i.db.Where("name = ?", name).First(&instance).Error
	if err != nil {
		return nil, err
	}

	return &instance, nil
}

func (i *instanceRepository) GetInstanceByID(instanceId string) (*instance_model.Instance, error) {
	// Valida o formato do UUID
	if _, err := uuid.Parse(instanceId); err != nil {
		return nil, fmt.Errorf("invalid UUID format: %v", err)
	}

	var instance instance_model.Instance
	err := i.db.Where("id = ?", instanceId).First(&instance).Error
	if err != nil {
		return nil, err
	}

	return &instance, nil
}

func (i *instanceRepository) GetConnectedInstanceByID(instanceId string) (*instance_model.Instance, error) {
	var instance instance_model.Instance
	err := i.db.Where("id = ? AND connected = ?", instanceId, true).First(&instance).Error
	if err != nil {
		return nil, err
	}

	return &instance, nil
}

func (i *instanceRepository) Update(instance *instance_model.Instance) error {
	if err := i.setTokenDigest(instance); err != nil {
		return fmt.Errorf("derive instance token digest: %w", err)
	}
	err := i.credentialDB().Save(instance).Error
	if err != nil {
		logger.LogError("Error updating instance in DB: %v", err)
	}
	return err
}

func (i *instanceRepository) BackfillTokenDigests(ctx context.Context, batchSize int) (int, error) {
	if i.tokenDigester == nil {
		return 0, nil
	}
	if batchSize <= 0 || batchSize > 1000 {
		return 0, errors.New("token digest backfill batch must be between 1 and 1000")
	}
	updated := 0
	err := i.credentialDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []struct {
			ID    string
			Token string
		}
		if err := tx.Raw(`SELECT id, token FROM instances
WHERE token_digest IS NULL
ORDER BY id
LIMIT ?
FOR UPDATE SKIP LOCKED`, batchSize).Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			digest, version, err := i.tokenDigester.Digest(row.Token)
			if err != nil {
				return fmt.Errorf("derive token digest for instance %s: %w", row.ID, err)
			}
			result := tx.Model(&instance_model.Instance{}).
				Where("id = ? AND token_digest IS NULL", row.ID).
				Updates(map[string]interface{}{"token_digest": digest, "token_key_version": version})
			if result.Error != nil {
				return result.Error
			}
			updated += int(result.RowsAffected)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return updated, nil
}

func (i *instanceRepository) RotateToken(ctx context.Context, rotation TokenRotation) (*TokenRotationResult, error) {
	rotation.Reason = strings.TrimSpace(rotation.Reason)
	if i == nil || i.db == nil || i.tokenDigester == nil {
		return nil, ErrTokenRotationUnavailable
	}
	if !validTokenRotation(rotation) {
		return nil, ErrInvalidTokenRotation
	}
	digest, keyVersion, err := i.tokenDigester.Digest(rotation.NewToken)
	if err != nil {
		return nil, fmt.Errorf("derive rotated instance token digest: %w", err)
	}
	rotation.OccurredAt = rotation.OccurredAt.UTC()
	newGeneration := rotation.ExpectedGeneration + 1
	db := i.credentialDB().WithContext(ctx)
	err = db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&instance_model.Instance{}).
			Where("id = ? AND token_generation = ?", rotation.InstanceID, rotation.ExpectedGeneration).
			Updates(map[string]interface{}{
				"token": rotation.NewToken, "token_digest": digest, "token_key_version": keyVersion,
				"token_generation": newGeneration, "token_rotated_at": rotation.OccurredAt,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			var count int64
			if err := tx.Model(&instance_model.Instance{}).Where("id = ?", rotation.InstanceID).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return ErrTokenRotationNotFound
			}
			return ErrTokenRotationConflict
		}
		audit := instance_model.TokenRotationAudit{
			ID: uuid.NewString(), InstanceID: rotation.InstanceID, PreviousGeneration: rotation.ExpectedGeneration,
			NewGeneration: newGeneration, Reason: rotation.Reason, ActorReferenceHash: rotation.ActorReferenceHash,
			RequestID: rotation.RequestID, OccurredAt: rotation.OccurredAt,
		}
		return tx.Create(&audit).Error
	})
	if err != nil {
		if !errors.Is(err, ErrTokenRotationNotFound) && !errors.Is(err, ErrTokenRotationConflict) {
			var count int64
			if queryErr := db.Model(&instance_model.TokenRotationAudit{}).
				Where("instance_id = ? AND request_id = ?", rotation.InstanceID, rotation.RequestID).Count(&count).Error; queryErr == nil && count > 0 {
				return nil, ErrTokenRotationConflict
			}
		}
		return nil, err
	}
	return &TokenRotationResult{InstanceID: rotation.InstanceID, TokenGeneration: newGeneration, RotatedAt: rotation.OccurredAt}, nil
}

func validTokenRotation(rotation TokenRotation) bool {
	if uuid.Validate(rotation.InstanceID) != nil || rotation.ExpectedGeneration <= 0 || rotation.NewToken == "" || rotation.OccurredAt.IsZero() ||
		!safeTokenRotationRequestID.MatchString(rotation.RequestID) || !validTokenRotationReason(rotation.Reason) {
		return false
	}
	decoded, err := hex.DecodeString(rotation.ActorReferenceHash)
	return err == nil && len(decoded) == 32 && rotation.ActorReferenceHash == strings.ToLower(rotation.ActorReferenceHash)
}

func validTokenRotationReason(reason string) bool {
	if reason == "" || !utf8.ValidString(reason) || utf8.RuneCountInString(reason) > 500 {
		return false
	}
	for _, value := range reason {
		if unicode.IsControl(value) {
			return false
		}
	}
	return true
}

func (i *instanceRepository) UpdateConnected(userId string, status bool, disconnectReason string) error {
	return i.db.Model(&instance_model.Instance{}).Where("id = ?", userId).Update("connected", status).Update("disconnect_reason", disconnectReason).Error
}

func (i *instanceRepository) UpdateQrcode(userId string, qr string) error {
	return i.db.Model(&instance_model.Instance{}).Where("id = ?", userId).Update("qrcode", qr).Error
}

func (i *instanceRepository) UpdateProxy(userId string, proxy string) error {
	return i.db.Model(&instance_model.Instance{}).Where("id = ?", userId).Update("proxy", proxy).Error
}

func (i *instanceRepository) UpdateJid(userId string, jid string) error {
	return i.db.Model(&instance_model.Instance{}).Where("id = ?", userId).Update("jid", jid).Error
}

func (i *instanceRepository) GetAllConnectedInstances() ([]*instance_model.Instance, error) {
	var instances []*instance_model.Instance
	err := i.db.Where("connected = ?", true).Find(&instances).Error
	if err != nil {
		return nil, err
	}

	return instances, nil
}

func (i *instanceRepository) GetAllConnectedInstancesByClientName(clientName string) ([]*instance_model.Instance, error) {
	var instances []*instance_model.Instance
	err := i.db.Where("connected = ? AND client_name = ?", true, clientName).Find(&instances).Error
	if err != nil {
		return nil, err
	}

	return instances, nil
}

func (i *instanceRepository) GetAll(clientName string) ([]*instance_model.Instance, error) {
	var instances []*instance_model.Instance
	err := i.db.Where("client_name = ?", clientName).Find(&instances).Error
	if err != nil {
		return nil, err
	}

	return instances, nil
}

func (i *instanceRepository) Delete(instanceId string) error {
	return i.db.Transaction(func(tx *gorm.DB) error {
		// Deleta todas as labels associadas à instância
		if err := tx.Where("instance_id = ?", instanceId).Delete(&label_model.Label{}).Error; err != nil {
			return fmt.Errorf("erro ao deletar labels: %v", err)
		}

		// Deleta todas as mensagens associadas à instância
		if err := tx.Where("source = ?", instanceId).Delete(&message_model.Message{}).Error; err != nil {
			return fmt.Errorf("erro ao deletar mensagens: %v", err)
		}

		// Deleta a instância
		if err := tx.Where("id = ?", instanceId).Delete(&instance_model.Instance{}).Error; err != nil {
			return fmt.Errorf("erro ao deletar instância: %v", err)
		}

		return nil
	})
}

func (i *instanceRepository) GetAdvancedSettings(instanceId string) (*instance_model.AdvancedSettings, error) {
	// Valida o formato do UUID
	if _, err := uuid.Parse(instanceId); err != nil {
		return nil, fmt.Errorf("invalid UUID format: %v", err)
	}

	var instance instance_model.Instance
	err := i.db.Select("always_online, reject_call, msg_reject_call, read_messages, ignore_groups, ignore_status").
		Where("id = ?", instanceId).First(&instance).Error
	if err != nil {
		return nil, err
	}

	settings := &instance_model.AdvancedSettings{
		AlwaysOnline:  instance.AlwaysOnline,
		RejectCall:    instance.RejectCall,
		MsgRejectCall: instance.MsgRejectCall,
		ReadMessages:  instance.ReadMessages,
		IgnoreGroups:  instance.IgnoreGroups,
		IgnoreStatus:  instance.IgnoreStatus,
	}

	return settings, nil
}

func (i *instanceRepository) UpdateAdvancedSettings(instanceId string, settings *instance_model.AdvancedSettings) error {
	// Valida o formato do UUID
	if _, err := uuid.Parse(instanceId); err != nil {
		return fmt.Errorf("invalid UUID format: %v", err)
	}

	updates := map[string]interface{}{
		"always_online":   settings.AlwaysOnline,
		"reject_call":     settings.RejectCall,
		"msg_reject_call": settings.MsgRejectCall,
		"read_messages":   settings.ReadMessages,
		"ignore_groups":   settings.IgnoreGroups,
		"ignore_status":   settings.IgnoreStatus,
	}

	err := i.db.Model(&instance_model.Instance{}).Where("id = ?", instanceId).Updates(updates).Error
	if err != nil {
		logger.LogError("Error updating advanced settings in DB: %v", err)
		return err
	}

	return nil
}

func NewInstanceRepository(db *gorm.DB) InstanceRepository {
	return NewInstanceRepositoryWithTokenDigester(db, nil)
}

func NewInstanceRepositoryWithTokenDigester(db *gorm.DB, digester TokenDigester) InstanceRepository {
	return &instanceRepository{
		db:            db,
		tokenDigester: digester,
	}
}
