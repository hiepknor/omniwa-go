package media_model

import "time"

type DownloadJobStatus string

const (
	DownloadJobPending    DownloadJobStatus = "pending"
	DownloadJobProcessing DownloadJobStatus = "processing"
	DownloadJobRetryWait  DownloadJobStatus = "retry_wait"
	DownloadJobCompleted  DownloadJobStatus = "completed"
	DownloadJobFailed     DownloadJobStatus = "failed"
)

type DownloadJob struct {
	ID                   string            `json:"-" gorm:"column:id;type:uuid;primaryKey"`
	InstanceID           string            `json:"-" gorm:"column:instance_id;type:uuid;not null"`
	MediaAssetID         string            `json:"-" gorm:"column:media_asset_id;type:uuid;not null"`
	MessageID            string            `json:"-" gorm:"column:message_id;size:255;not null"`
	Status               DownloadJobStatus `json:"-" gorm:"column:status;size:32;not null"`
	DescriptorCiphertext []byte            `json:"-" gorm:"column:descriptor_ciphertext"`
	DescriptorNonce      []byte            `json:"-" gorm:"column:descriptor_nonce"`
	DescriptorKeyVersion *int              `json:"-" gorm:"column:descriptor_key_version"`
	AttemptCount         int               `json:"-" gorm:"column:attempt_count;not null"`
	MaxAttempts          int               `json:"-" gorm:"column:max_attempts;not null"`
	NextAttemptAt        time.Time         `json:"-" gorm:"column:next_attempt_at;not null"`
	ClaimToken           *string           `json:"-" gorm:"column:claim_token;type:uuid"`
	LeaseUntil           *time.Time        `json:"-" gorm:"column:lease_until"`
	ProviderExpiresAt    *time.Time        `json:"-" gorm:"column:provider_expires_at"`
	LastErrorCode        *string           `json:"-" gorm:"column:last_error_code;size:64"`
	CreatedAt            time.Time         `json:"-" gorm:"column:created_at;not null"`
	UpdatedAt            time.Time         `json:"-" gorm:"column:updated_at;not null"`
	CompletedAt          *time.Time        `json:"-" gorm:"column:completed_at"`
}

func (DownloadJob) TableName() string { return "media_download_jobs" }
