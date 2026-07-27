package media_model

import "time"

type AssetOrigin string
type AssetStatus string
type VariantKind string
type ReferenceOwnerType string

const (
	AssetOriginDeviceUpload    AssetOrigin = "device_upload"
	AssetOriginWhatsAppInbound AssetOrigin = "whatsapp_inbound"

	AssetStatusPending     AssetStatus = "pending"
	AssetStatusUploading   AssetStatus = "uploading"
	AssetStatusDownloading AssetStatus = "downloading"
	AssetStatusProcessing  AssetStatus = "processing"
	AssetStatusReady       AssetStatus = "ready"
	AssetStatusFailed      AssetStatus = "failed"
	AssetStatusDeleting    AssetStatus = "deleting"
	AssetStatusDeleted     AssetStatus = "deleted"

	VariantProviderOriginal VariantKind = "provider_original"
	VariantCanonical        VariantKind = "canonical"

	ReferenceOwnerCampaign ReferenceOwnerType = "campaign"
	ReferenceOwnerMessage  ReferenceOwnerType = "message"
)

type Asset struct {
	ID                   string        `json:"id" gorm:"column:id;type:uuid;primaryKey"`
	InstanceID           string        `json:"-" gorm:"column:instance_id;type:uuid;not null"`
	MediaType            string        `json:"mediaType" gorm:"column:media_type;size:32;not null"`
	Origin               AssetOrigin   `json:"origin" gorm:"column:origin;size:32;not null"`
	Status               AssetStatus   `json:"status" gorm:"column:status;size:32;not null"`
	FailureCode          *string       `json:"failureCode,omitempty" gorm:"column:failure_code;size:64"`
	RequestReferenceHash *string       `json:"-" gorm:"column:request_reference_hash;size:64"`
	CleanupClaimToken    *string       `json:"-" gorm:"column:cleanup_claim_token;type:uuid"`
	CleanupLeaseUntil    *time.Time    `json:"-" gorm:"column:cleanup_lease_until"`
	ReadyAt              *time.Time    `json:"readyAt,omitempty" gorm:"column:ready_at"`
	ExpiresAt            *time.Time    `json:"expiresAt,omitempty" gorm:"column:expires_at"`
	DeletedAt            *time.Time    `json:"-" gorm:"column:deleted_at"`
	CreatedAt            time.Time     `json:"createdAt" gorm:"column:created_at;not null"`
	UpdatedAt            time.Time     `json:"updatedAt" gorm:"column:updated_at;not null"`
	Canonical            *AssetVariant `json:"canonical,omitempty" gorm:"-"`
}

func (Asset) TableName() string { return "media_assets" }

type AssetVariant struct {
	MediaAssetID string      `json:"-" gorm:"column:media_asset_id;type:uuid;primaryKey"`
	InstanceID   string      `json:"-" gorm:"column:instance_id;type:uuid;not null"`
	Kind         VariantKind `json:"variant" gorm:"column:variant;size:32;primaryKey"`
	ObjectKey    string      `json:"-" gorm:"column:object_key;not null"`
	MIMEType     string      `json:"mimeType" gorm:"column:mime_type;size:128;not null"`
	SizeBytes    int64       `json:"sizeBytes" gorm:"column:size_bytes;not null"`
	Width        int         `json:"width" gorm:"column:width;not null"`
	Height       int         `json:"height" gorm:"column:height;not null"`
	SHA256       string      `json:"sha256" gorm:"column:sha256;size:64;not null"`
	CreatedAt    time.Time   `json:"createdAt" gorm:"column:created_at;not null"`
}

func (AssetVariant) TableName() string { return "media_asset_variants" }

type AssetReference struct {
	InstanceID     string             `json:"-" gorm:"column:instance_id;type:uuid;primaryKey"`
	MediaAssetID   string             `json:"mediaAssetId" gorm:"column:media_asset_id;type:uuid;primaryKey"`
	OwnerType      ReferenceOwnerType `json:"ownerType" gorm:"column:owner_type;size:32;primaryKey"`
	OwnerID        string             `json:"ownerId" gorm:"column:owner_id;size:255;primaryKey"`
	RetentionUntil *time.Time         `json:"retentionUntil,omitempty" gorm:"column:retention_until"`
	CreatedAt      time.Time          `json:"createdAt" gorm:"column:created_at;not null"`
}

func (AssetReference) TableName() string { return "media_asset_references" }
