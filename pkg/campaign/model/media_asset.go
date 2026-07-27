package campaign_model

import "time"

type MediaAssetStatus string

const (
	MediaAssetStatusUploading MediaAssetStatus = "uploading"
	MediaAssetStatusReady     MediaAssetStatus = "ready"
	MediaAssetStatusFailed    MediaAssetStatus = "failed"
	MediaAssetStatusDeleted   MediaAssetStatus = "deleted"
)

type MediaAsset struct {
	ID                   string           `json:"id" gorm:"column:id;type:uuid;primaryKey"`
	InstanceID           string           `json:"-" gorm:"column:instance_id;type:uuid;not null"`
	ObjectKey            string           `json:"-" gorm:"column:object_key;not null"`
	MediaType            string           `json:"type" gorm:"column:media_type;size:32;not null"`
	MIMEType             *string          `json:"mimeType,omitempty" gorm:"column:mime_type;size:128"`
	SizeBytes            *int64           `json:"size,omitempty" gorm:"column:size_bytes"`
	Width                *int             `json:"width,omitempty" gorm:"column:width"`
	Height               *int             `json:"height,omitempty" gorm:"column:height"`
	SHA256               *string          `json:"sha256,omitempty" gorm:"column:sha256;size:64"`
	Status               MediaAssetStatus `json:"status" gorm:"column:status;size:32;not null"`
	RequestReferenceHash *string          `json:"-" gorm:"column:request_reference_hash;size:64"`
	CleanupClaimToken    *string          `json:"-" gorm:"column:cleanup_claim_token;type:uuid"`
	CleanupLeaseUntil    *time.Time       `json:"-" gorm:"column:cleanup_lease_until"`
	ReadyAt              *time.Time       `json:"readyAt,omitempty" gorm:"column:ready_at"`
	ExpiresAt            time.Time        `json:"expiresAt" gorm:"column:expires_at;not null"`
	DeletedAt            *time.Time       `json:"-" gorm:"column:deleted_at"`
	CreatedAt            time.Time        `json:"createdAt" gorm:"column:created_at;not null"`
	UpdatedAt            time.Time        `json:"updatedAt" gorm:"column:updated_at;not null"`
}

func (MediaAsset) TableName() string { return "campaign_media_assets" }
