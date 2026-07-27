package campaign_repository

import (
	"errors"
	"time"
)

type GroupSafetySettings struct {
	Enabled               bool
	Cooldown              time.Duration
	CircuitDuration       time.Duration
	RatePauseThreshold    int
	FailurePauseThreshold int
}

func (settings GroupSafetySettings) validate() error {
	if !settings.Enabled {
		return nil
	}
	if settings.Cooldown <= 0 || settings.CircuitDuration <= 0 || settings.RatePauseThreshold < 1 || settings.FailurePauseThreshold < 1 {
		return errors.New("enabled group campaign safety requires positive bounded policy")
	}
	return nil
}

func WithGroupSafety(settings GroupSafetySettings) RepositoryOption {
	return func(repository *campaignRepository) { repository.groupSafety = settings }
}

type groupDeliveryGuard struct {
	InstanceID         string     `gorm:"column:instance_id;type:uuid;primaryKey"`
	GroupJID           string     `gorm:"column:group_jid;size:255;primaryKey"`
	OwnerRecipientID   *string    `gorm:"column:owner_recipient_id;type:uuid"`
	OwnerCampaignID    *string    `gorm:"column:owner_campaign_id;type:uuid"`
	ClaimToken         *string    `gorm:"column:claim_token;size:64"`
	LeaseUntil         *time.Time `gorm:"column:lease_until"`
	LastAcknowledgedAt *time.Time `gorm:"column:last_acknowledged_at"`
	UpdatedAt          time.Time  `gorm:"column:updated_at"`
}

func (groupDeliveryGuard) TableName() string { return "campaign_group_delivery_guards" }

type instanceCircuit struct {
	InstanceID string    `gorm:"column:instance_id;type:uuid;primaryKey"`
	OpenUntil  time.Time `gorm:"column:open_until"`
	Reason     string    `gorm:"column:reason;size:64"`
	UpdatedAt  time.Time `gorm:"column:updated_at"`
}

func (instanceCircuit) TableName() string { return "campaign_instance_circuits" }
