package config

import (
	"bytes"
	"encoding/base64"
	"math"
	"testing"
	"time"

	config_env "github.com/evolution-foundation/evolution-go/pkg/config/env"
)

func TestLoadWAInfoGuardDefaults(t *testing.T) {
	setRequiredConfigEnv(t)
	t.Setenv(config_env.WA_INFO_RATE, "")
	t.Setenv(config_env.WA_INFO_BURST, "")
	t.Setenv(config_env.WA_INFO_MAX_WAIT, "")
	t.Setenv(config_env.WA_INFO_COOLDOWN, "")
	t.Setenv(config_env.WA_GROUP_RECONCILE_INTERVAL, "")
	t.Setenv(config_env.WA_MSG_RETENTION, "")
	t.Setenv(config_env.WA_EVENT_RETENTION, "")
	t.Setenv(config_env.WA_OUTBOUND_RATE, "")
	t.Setenv(config_env.WA_OUTBOUND_BURST, "")
	t.Setenv(config_env.WA_OUTBOUND_MAX_WAIT, "")
	t.Setenv(config_env.WA_CAMPAIGN_BATCH, "")
	t.Setenv(config_env.WA_CAMPAIGN_LEASE, "")
	t.Setenv(config_env.WA_CAMPAIGN_POLL_INTERVAL, "")
	t.Setenv(config_env.WA_CAMPAIGN_MAX_ATTEMPTS, "")
	t.Setenv(config_env.WA_CAMPAIGN_RETRY_BASE, "")
	t.Setenv(config_env.WA_CAMPAIGN_DIRECT_CREATE_ENABLED, "")
	t.Setenv(config_env.WA_CAMPAIGN_GROUP_TARGETS_ENABLED, "")
	t.Setenv(config_env.WA_CAMPAIGN_GROUP_COOLDOWN, "")
	t.Setenv(config_env.WA_CAMPAIGN_CIRCUIT_DURATION, "")
	t.Setenv(config_env.WA_CAMPAIGN_RATE_PAUSE_THRESHOLD, "")
	t.Setenv(config_env.WA_CAMPAIGN_FAILURE_PAUSE_THRESHOLD, "")
	t.Setenv(config_env.WA_CAMPAIGN_IMAGE_CONTENT_ENABLED, "")
	t.Setenv(config_env.WA_MEDIA_ASSETS_ENABLED, "")
	t.Setenv(config_env.WA_CHAT_IMAGE_CONTENT_ENABLED, "")
	t.Setenv(config_env.WA_INBOUND_IMAGE_CONTENT_ENABLED, "")
	t.Setenv(config_env.MEDIA_DESCRIPTOR_KEY, "")
	t.Setenv(config_env.MEDIA_DESCRIPTOR_KEY_VERSION, "")
	t.Setenv(config_env.MEDIA_DOWNLOAD_BATCH, "")
	t.Setenv(config_env.MEDIA_DOWNLOAD_LEASE, "")
	t.Setenv(config_env.MEDIA_DOWNLOAD_POLL_INTERVAL, "")
	t.Setenv(config_env.MEDIA_DOWNLOAD_TIMEOUT, "")
	t.Setenv(config_env.MEDIA_DOWNLOAD_MAX_ATTEMPTS, "")
	t.Setenv(config_env.MEDIA_DOWNLOAD_RETRY_BASE, "")
	t.Setenv(config_env.MEDIA_ASSET_BUCKET, "")
	t.Setenv(config_env.MEDIA_ASSET_MAX_BYTES, "")
	t.Setenv(config_env.MEDIA_ASSET_MAX_PIXELS, "")
	t.Setenv(config_env.MEDIA_ASSET_UNBOUND_TTL, "")
	t.Setenv(config_env.CAMPAIGN_MEDIA_BUCKET, "")
	t.Setenv(config_env.CAMPAIGN_MEDIA_MAX_BYTES, "")
	t.Setenv(config_env.CAMPAIGN_MEDIA_MAX_PIXELS, "")
	t.Setenv(config_env.CAMPAIGN_MEDIA_UNBOUND_TTL, "")
	t.Setenv(config_env.WA_GROUP_LISTS_ENABLED, "")
	t.Setenv(config_env.WA_GROUP_LIST_ELIGIBILITY_ENABLED, "")
	t.Setenv(config_env.WA_GROUP_MANAGEMENT_CONTRACT_ENABLED, "")
	t.Setenv(config_env.WA_GROUP_PHOTO_ASSETS_ENABLED, "")
	t.Setenv(config_env.REMOTE_MEDIA_FETCH_POLICY, "")
	t.Setenv(config_env.REMOTE_MEDIA_ALLOWED_HOSTS, "")
	t.Setenv(config_env.REMOTE_MEDIA_FETCH_TIMEOUT, "")
	t.Setenv(config_env.REMOTE_MEDIA_MAX_BYTES, "")
	t.Setenv(config_env.WEBHOOK_ALLOWED_HOSTS, "")
	t.Setenv(config_env.WEBHOOK_ALLOWED_PORTS, "")
	t.Setenv(config_env.WEBHOOK_ALLOW_PRIVATE, "")
	t.Setenv(config_env.WEBHOOK_TIMEOUT, "")
	t.Setenv(config_env.WEBHOOK_MAX_REQUEST_BYTES, "")
	t.Setenv(config_env.WEBHOOK_MAX_RESPONSE_BYTES, "")
	t.Setenv(config_env.WEBHOOK_WORKERS, "")
	t.Setenv(config_env.WEBHOOK_QUEUE_CAPACITY, "")
	t.Setenv(config_env.WEBHOOK_MAX_PENDING_PER_INSTANCE, "")
	t.Setenv(config_env.WEBHOOK_MAX_ATTEMPTS, "")
	t.Setenv(config_env.WEBHOOK_RETRY_BASE, "")
	t.Setenv(config_env.INSTANCE_TOKEN_HMAC_KEY, "")
	t.Setenv(config_env.INSTANCE_TOKEN_HMAC_KEY_VERSION, "")
	t.Setenv(config_env.INSTANCE_TOKEN_BACKFILL_BATCH, "")
	t.Setenv(config_env.INSTANCE_TOKEN_BACKFILL_MAX_BATCHES, "")
	t.Setenv(config_env.WA_CONTACT_IDENTITY_RECONCILIATION_ENABLED, "")
	t.Setenv(config_env.CONTACT_IDENTITY_BACKFILL_BATCH, "")
	t.Setenv(config_env.CONTACT_IDENTITY_BACKFILL_MAX_BATCHES, "")

	config := Load()
	if math.Abs(config.WAInfoRatePerSecond-(5.0/60.0)) > 1e-12 {
		t.Fatalf("WAInfoRatePerSecond = %v", config.WAInfoRatePerSecond)
	}
	if config.WAInfoBurst != 3 {
		t.Fatalf("WAInfoBurst = %d, want 3", config.WAInfoBurst)
	}
	if config.WAInfoMaxWait != 5*time.Second {
		t.Fatalf("WAInfoMaxWait = %v, want 5s", config.WAInfoMaxWait)
	}
	if config.WAInfoCooldown != 90*time.Second {
		t.Fatalf("WAInfoCooldown = %v, want 90s", config.WAInfoCooldown)
	}
	if config.GroupSyncInterval != 6*time.Hour {
		t.Fatalf("GroupSyncInterval = %v, want 6h", config.GroupSyncInterval)
	}
	if config.MessageRetention != 90*24*time.Hour {
		t.Fatalf("MessageRetention = %v, want 2160h", config.MessageRetention)
	}
	if config.EventRetention != 30*24*time.Hour {
		t.Fatalf("EventRetention = %v, want 720h", config.EventRetention)
	}
	if math.Abs(config.WAOutboundRatePerSecond-(30.0/60.0)) > 1e-12 || config.WAOutboundBurst != 5 || config.WAOutboundMaxWait != 5*time.Second {
		t.Fatalf("outbound defaults = %v/%d/%v", config.WAOutboundRatePerSecond, config.WAOutboundBurst, config.WAOutboundMaxWait)
	}
	if config.CampaignBatchSize != 10 || config.CampaignLease != 2*time.Minute || config.CampaignPollInterval != time.Second || config.CampaignMaxAttempts != 3 || config.CampaignRetryBase != 30*time.Second {
		t.Fatalf("campaign defaults = %d/%v/%v/%d/%v", config.CampaignBatchSize, config.CampaignLease, config.CampaignPollInterval, config.CampaignMaxAttempts, config.CampaignRetryBase)
	}
	if config.GroupListsEnabled {
		t.Fatal("expected Group Lists to be disabled by default")
	}
	if config.GroupListEligibilityEnabled {
		t.Fatal("expected Group List eligibility to be disabled by default")
	}
	if config.GroupManagementEnabled {
		t.Fatal("expected Group Management contract to be disabled by default")
	}
	if config.GroupPhotoAssetsEnabled {
		t.Fatal("expected Group photo assets to be disabled by default")
	}
	if config.CampaignDirectCreateEnabled {
		t.Fatal("expected direct campaign creation to be disabled by default")
	}
	if config.CampaignGroupTargetsEnabled || config.CampaignGroupCooldown != time.Hour || config.CampaignCircuitDuration != 5*time.Minute || config.CampaignRatePauseThreshold != 3 || config.CampaignFailurePauseThreshold != 10 {
		t.Fatalf("group campaign defaults = %t/%v/%v/%d/%d", config.CampaignGroupTargetsEnabled, config.CampaignGroupCooldown, config.CampaignCircuitDuration, config.CampaignRatePauseThreshold, config.CampaignFailurePauseThreshold)
	}
	if config.CampaignImageContentEnabled || config.CampaignMediaBucket != "omniwa-campaign-media" || config.CampaignMediaMaxBytes != 8*1024*1024 || config.CampaignMediaMaxPixels != 16_000_000 || config.CampaignMediaUnboundTTL != 24*time.Hour {
		t.Fatalf("campaign media defaults are invalid: enabled=%t bucket=%q bytes=%d pixels=%d ttl=%v", config.CampaignImageContentEnabled, config.CampaignMediaBucket, config.CampaignMediaMaxBytes, config.CampaignMediaMaxPixels, config.CampaignMediaUnboundTTL)
	}
	if config.MediaAssetsEnabled || config.MediaAssetBucket != "omniwa-media-assets" || config.MediaAssetMaxBytes != 8*1024*1024 || config.MediaAssetMaxPixels != 16_000_000 || config.MediaAssetUnboundTTL != 24*time.Hour {
		t.Fatalf("media asset defaults are invalid: enabled=%t bucket=%q bytes=%d pixels=%d ttl=%v", config.MediaAssetsEnabled, config.MediaAssetBucket, config.MediaAssetMaxBytes, config.MediaAssetMaxPixels, config.MediaAssetUnboundTTL)
	}
	if config.ChatImageContentEnabled {
		t.Fatal("expected chat image content to be disabled by default")
	}
	if config.InboundImageContentEnabled || len(config.MediaDescriptorKey) != 0 || config.MediaDescriptorKeyVersion != 0 ||
		config.MediaDownloadBatch != 4 || config.MediaDownloadLease != 3*time.Minute || config.MediaDownloadPollInterval != time.Second ||
		config.MediaDownloadTimeout != 2*time.Minute || config.MediaDownloadMaxAttempts != 3 || config.MediaDownloadRetryBase != 30*time.Second {
		t.Fatalf("inbound media defaults are invalid")
	}
	if config.RemoteMedia.Policy != "public_only" || config.RemoteMedia.Timeout != 15*time.Second || config.RemoteMedia.MaxBytes != 32*1024*1024 || len(config.RemoteMedia.AllowedHosts) != 0 {
		t.Fatalf("remote media defaults = %+v", config.RemoteMedia)
	}
	if config.Webhook.Timeout != 10*time.Second || config.Webhook.MaxRequestBytes != 4*1024*1024 || config.Webhook.MaxResponseBytes != 64*1024 || config.Webhook.AllowPrivate || len(config.Webhook.AllowedHosts) != 0 || len(config.Webhook.AllowedPorts) != 2 || config.Webhook.Workers != 4 || config.Webhook.QueueCapacity != 256 || config.Webhook.MaxPendingPerInstance != 32 || config.Webhook.MaxAttempts != 3 || config.Webhook.RetryBase != time.Second {
		t.Fatalf("webhook defaults are invalid")
	}
	if len(config.InstanceTokenHMACKey) != 0 || config.InstanceTokenHMACKeyVersion != 0 || config.InstanceTokenBackfillBatch != 100 || config.InstanceTokenBackfillMaxBatches != 10 {
		t.Fatalf("instance token digest defaults are invalid")
	}
	if config.ContactIdentityReconciliationEnabled || config.ContactIdentityBackfillBatch != 100 || config.ContactIdentityBackfillMaxBatches != 10 {
		t.Fatalf("contact identity reconciliation defaults are invalid")
	}
}

func TestLoadWAInfoGuardOverrides(t *testing.T) {
	setRequiredConfigEnv(t)
	t.Setenv(config_env.WA_INFO_RATE, "12/hour")
	t.Setenv(config_env.WA_INFO_BURST, "7")
	t.Setenv(config_env.WA_INFO_MAX_WAIT, "250ms")
	t.Setenv(config_env.WA_INFO_COOLDOWN, "2m")
	t.Setenv(config_env.WA_GROUP_RECONCILE_INTERVAL, "45m")
	t.Setenv(config_env.WA_MSG_RETENTION, "720h")
	t.Setenv(config_env.WA_EVENT_RETENTION, "168h")
	t.Setenv(config_env.WA_OUTBOUND_RATE, "120/hour")
	t.Setenv(config_env.WA_OUTBOUND_BURST, "7")
	t.Setenv(config_env.WA_OUTBOUND_MAX_WAIT, "2s")
	t.Setenv(config_env.WA_CAMPAIGN_BATCH, "20")
	t.Setenv(config_env.WA_CAMPAIGN_LEASE, "3m")
	t.Setenv(config_env.WA_CAMPAIGN_POLL_INTERVAL, "2s")
	t.Setenv(config_env.WA_CAMPAIGN_MAX_ATTEMPTS, "5")
	t.Setenv(config_env.WA_CAMPAIGN_RETRY_BASE, "45s")
	t.Setenv(config_env.WA_CAMPAIGN_DIRECT_CREATE_ENABLED, "true")
	t.Setenv(config_env.WA_CAMPAIGN_GROUP_TARGETS_ENABLED, "true")
	t.Setenv(config_env.WA_CAMPAIGN_GROUP_COOLDOWN, "2h")
	t.Setenv(config_env.WA_CAMPAIGN_CIRCUIT_DURATION, "7m")
	t.Setenv(config_env.WA_CAMPAIGN_RATE_PAUSE_THRESHOLD, "4")
	t.Setenv(config_env.WA_CAMPAIGN_FAILURE_PAUSE_THRESHOLD, "12")
	t.Setenv(config_env.WA_CAMPAIGN_IMAGE_CONTENT_ENABLED, "true")
	t.Setenv(config_env.WA_MEDIA_ASSETS_ENABLED, "true")
	t.Setenv(config_env.WA_GROUP_PHOTO_ASSETS_ENABLED, "true")
	t.Setenv(config_env.WA_CHAT_IMAGE_CONTENT_ENABLED, "true")
	t.Setenv(config_env.WA_INBOUND_IMAGE_CONTENT_ENABLED, "true")
	t.Setenv(config_env.MEDIA_DESCRIPTOR_KEY, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)))
	t.Setenv(config_env.MEDIA_DESCRIPTOR_KEY_VERSION, "5")
	t.Setenv(config_env.MEDIA_DOWNLOAD_BATCH, "8")
	t.Setenv(config_env.MEDIA_DOWNLOAD_LEASE, "90s")
	t.Setenv(config_env.MEDIA_DOWNLOAD_POLL_INTERVAL, "2s")
	t.Setenv(config_env.MEDIA_DOWNLOAD_TIMEOUT, "45s")
	t.Setenv(config_env.MEDIA_DOWNLOAD_MAX_ATTEMPTS, "6")
	t.Setenv(config_env.MEDIA_DOWNLOAD_RETRY_BASE, "10s")
	t.Setenv(config_env.MEDIA_ASSET_BUCKET, "private-media-assets")
	t.Setenv(config_env.MEDIA_ASSET_MAX_BYTES, "3145728")
	t.Setenv(config_env.MEDIA_ASSET_MAX_PIXELS, "9000000")
	t.Setenv(config_env.MEDIA_ASSET_UNBOUND_TTL, "6h")
	t.Setenv(config_env.CAMPAIGN_MEDIA_BUCKET, "private-campaign-images")
	t.Setenv(config_env.CAMPAIGN_MEDIA_MAX_BYTES, "4194304")
	t.Setenv(config_env.CAMPAIGN_MEDIA_MAX_PIXELS, "12000000")
	t.Setenv(config_env.CAMPAIGN_MEDIA_UNBOUND_TTL, "12h")
	t.Setenv(config_env.WA_GROUP_LISTS_ENABLED, "true")
	t.Setenv(config_env.WA_GROUP_LIST_ELIGIBILITY_ENABLED, "true")
	t.Setenv(config_env.REMOTE_MEDIA_FETCH_POLICY, "allowlist")
	t.Setenv(config_env.REMOTE_MEDIA_ALLOWED_HOSTS, "cdn.example.com, media.example.com")
	t.Setenv(config_env.REMOTE_MEDIA_FETCH_TIMEOUT, "3s")
	t.Setenv(config_env.REMOTE_MEDIA_MAX_BYTES, "4096")
	t.Setenv(config_env.WEBHOOK_ALLOWED_HOSTS, "hooks.example.com, internal.example.com")
	t.Setenv(config_env.WEBHOOK_ALLOWED_PORTS, "443,8443")
	t.Setenv(config_env.WEBHOOK_ALLOW_PRIVATE, "true")
	t.Setenv(config_env.WEBHOOK_TIMEOUT, "4s")
	t.Setenv(config_env.WEBHOOK_MAX_REQUEST_BYTES, "2048")
	t.Setenv(config_env.WEBHOOK_MAX_RESPONSE_BYTES, "1024")
	t.Setenv(config_env.WEBHOOK_WORKERS, "8")
	t.Setenv(config_env.WEBHOOK_QUEUE_CAPACITY, "100")
	t.Setenv(config_env.WEBHOOK_MAX_PENDING_PER_INSTANCE, "20")
	t.Setenv(config_env.WEBHOOK_MAX_ATTEMPTS, "4")
	t.Setenv(config_env.WEBHOOK_RETRY_BASE, "250ms")
	t.Setenv(config_env.INSTANCE_TOKEN_HMAC_KEY, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	t.Setenv(config_env.INSTANCE_TOKEN_HMAC_KEY_VERSION, "7")
	t.Setenv(config_env.INSTANCE_TOKEN_BACKFILL_BATCH, "25")
	t.Setenv(config_env.INSTANCE_TOKEN_BACKFILL_MAX_BATCHES, "4")
	t.Setenv(config_env.WA_CONTACT_IDENTITY_RECONCILIATION_ENABLED, "true")
	t.Setenv(config_env.CONTACT_IDENTITY_BACKFILL_BATCH, "25")
	t.Setenv(config_env.CONTACT_IDENTITY_BACKFILL_MAX_BATCHES, "4")

	config := Load()
	if config.RemoteMedia.Policy != "allowlist" || config.RemoteMedia.Timeout != 3*time.Second || config.RemoteMedia.MaxBytes != 4096 || len(config.RemoteMedia.AllowedHosts) != 2 {
		t.Fatalf("remote media overrides = %+v", config.RemoteMedia)
	}
	if config.Webhook.Timeout != 4*time.Second || config.Webhook.MaxRequestBytes != 2048 || config.Webhook.MaxResponseBytes != 1024 || !config.Webhook.AllowPrivate || len(config.Webhook.AllowedHosts) != 2 || len(config.Webhook.AllowedPorts) != 2 || config.Webhook.Workers != 8 || config.Webhook.QueueCapacity != 100 || config.Webhook.MaxPendingPerInstance != 20 || config.Webhook.MaxAttempts != 4 || config.Webhook.RetryBase != 250*time.Millisecond {
		t.Fatalf("webhook overrides are invalid")
	}
	if math.Abs(config.WAInfoRatePerSecond-(12.0/3600.0)) > 1e-12 {
		t.Fatalf("WAInfoRatePerSecond = %v", config.WAInfoRatePerSecond)
	}
	if config.WAInfoBurst != 7 || config.WAInfoMaxWait != 250*time.Millisecond || config.WAInfoCooldown != 2*time.Minute {
		t.Fatalf("unexpected guard config: burst=%d maxWait=%v cooldown=%v", config.WAInfoBurst, config.WAInfoMaxWait, config.WAInfoCooldown)
	}
	if config.GroupSyncInterval != 45*time.Minute {
		t.Fatalf("GroupSyncInterval = %v, want 45m", config.GroupSyncInterval)
	}
	if config.MessageRetention != 30*24*time.Hour {
		t.Fatalf("MessageRetention = %v, want 720h", config.MessageRetention)
	}
	if config.EventRetention != 7*24*time.Hour {
		t.Fatalf("EventRetention = %v, want 168h", config.EventRetention)
	}
	if math.Abs(config.WAOutboundRatePerSecond-(120.0/3600.0)) > 1e-12 || config.WAOutboundBurst != 7 || config.WAOutboundMaxWait != 2*time.Second {
		t.Fatalf("outbound overrides = %v/%d/%v", config.WAOutboundRatePerSecond, config.WAOutboundBurst, config.WAOutboundMaxWait)
	}
	if config.CampaignBatchSize != 20 || config.CampaignLease != 3*time.Minute || config.CampaignPollInterval != 2*time.Second || config.CampaignMaxAttempts != 5 || config.CampaignRetryBase != 45*time.Second {
		t.Fatalf("campaign overrides = %d/%v/%v/%d/%v", config.CampaignBatchSize, config.CampaignLease, config.CampaignPollInterval, config.CampaignMaxAttempts, config.CampaignRetryBase)
	}
	if !config.GroupListsEnabled {
		t.Fatal("expected Group Lists feature flag override to be enabled")
	}
	if !config.GroupListEligibilityEnabled {
		t.Fatal("expected Group List eligibility feature flag override to be enabled")
	}
	if !config.CampaignDirectCreateEnabled {
		t.Fatal("expected direct campaign creation emergency override to be enabled")
	}
	if !config.CampaignGroupTargetsEnabled || config.CampaignGroupCooldown != 2*time.Hour || config.CampaignCircuitDuration != 7*time.Minute || config.CampaignRatePauseThreshold != 4 || config.CampaignFailurePauseThreshold != 12 {
		t.Fatalf("group campaign overrides = %t/%v/%v/%d/%d", config.CampaignGroupTargetsEnabled, config.CampaignGroupCooldown, config.CampaignCircuitDuration, config.CampaignRatePauseThreshold, config.CampaignFailurePauseThreshold)
	}
	if !config.CampaignImageContentEnabled || config.CampaignMediaBucket != "private-campaign-images" || config.CampaignMediaMaxBytes != 4*1024*1024 || config.CampaignMediaMaxPixels != 12_000_000 || config.CampaignMediaUnboundTTL != 12*time.Hour {
		t.Fatalf("campaign media overrides are invalid")
	}
	if !config.MediaAssetsEnabled || config.MediaAssetBucket != "private-media-assets" || config.MediaAssetMaxBytes != 3*1024*1024 || config.MediaAssetMaxPixels != 9_000_000 || config.MediaAssetUnboundTTL != 6*time.Hour {
		t.Fatalf("media asset overrides are invalid: enabled=%t bucket=%q bytes=%d pixels=%d ttl=%v", config.MediaAssetsEnabled, config.MediaAssetBucket, config.MediaAssetMaxBytes, config.MediaAssetMaxPixels, config.MediaAssetUnboundTTL)
	}
	if !config.GroupPhotoAssetsEnabled {
		t.Fatal("expected Group photo asset override to be enabled")
	}
	if !config.ChatImageContentEnabled {
		t.Fatal("expected chat image content override to be enabled")
	}
	if !config.InboundImageContentEnabled || len(config.MediaDescriptorKey) != 32 || config.MediaDescriptorKeyVersion != 5 ||
		config.MediaDownloadBatch != 8 || config.MediaDownloadLease != 90*time.Second || config.MediaDownloadPollInterval != 2*time.Second ||
		config.MediaDownloadTimeout != 45*time.Second || config.MediaDownloadMaxAttempts != 6 || config.MediaDownloadRetryBase != 10*time.Second {
		t.Fatal("inbound media overrides are invalid")
	}
	if len(config.InstanceTokenHMACKey) != 32 || config.InstanceTokenHMACKeyVersion != 7 || config.InstanceTokenBackfillBatch != 25 || config.InstanceTokenBackfillMaxBatches != 4 {
		t.Fatalf("instance token digest overrides are invalid")
	}
	if !config.ContactIdentityReconciliationEnabled || config.ContactIdentityBackfillBatch != 25 || config.ContactIdentityBackfillMaxBatches != 4 {
		t.Fatalf("contact identity reconciliation overrides are invalid")
	}
}

func TestParseOptionalBase64Key(t *testing.T) {
	if key, err := parseOptionalBase64Key("", 32); err != nil || key != nil {
		t.Fatalf("disabled key = %v, %v", key, err)
	}
	if _, err := parseOptionalBase64Key("not-base64", 32); err == nil {
		t.Fatal("invalid base64 key was accepted")
	}
	if _, err := parseOptionalBase64Key("c2hvcnQ=", 32); err == nil {
		t.Fatal("short key was accepted")
	}
}

func TestLoadAllowsDisablingPeriodicGroupReconciliation(t *testing.T) {
	setRequiredConfigEnv(t)
	t.Setenv(config_env.WA_GROUP_RECONCILE_INTERVAL, "0")

	if got := Load().GroupSyncInterval; got != 0 {
		t.Fatalf("GroupSyncInterval = %v, want disabled", got)
	}
}

func TestParseRatePerSecond(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  float64
	}{
		{name: "per second", value: "2/sec", want: 2},
		{name: "per minute", value: "5/min", want: 5.0 / 60.0},
		{name: "per hour", value: "120/hour", want: 120.0 / 3600.0},
		{name: "decimal", value: "2.5/minutes", want: 2.5 / 60.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRatePerSecond(tt.value)
			if err != nil {
				t.Fatalf("parseRatePerSecond() error = %v", err)
			}
			if math.Abs(got-tt.want) > 1e-12 {
				t.Fatalf("parseRatePerSecond() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseRatePerSecondRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"", "5", "zero/min", "0/min", "-1/min", "1/day", "1/min/extra"} {
		t.Run(value, func(t *testing.T) {
			if _, err := parseRatePerSecond(value); err == nil {
				t.Fatalf("parseRatePerSecond(%q) unexpectedly succeeded", value)
			}
		})
	}
}

func TestParseGuardDurations(t *testing.T) {
	if got, err := parseNonNegativeDuration("0s"); err != nil || got != 0 {
		t.Fatalf("parseNonNegativeDuration(0s) = %v, %v", got, err)
	}
	if got, err := parsePositiveDuration("90s"); err != nil || got != 90*time.Second {
		t.Fatalf("parsePositiveDuration(90s) = %v, %v", got, err)
	}
	for _, value := range []string{"-1s", "invalid"} {
		if _, err := parseNonNegativeDuration(value); err == nil {
			t.Fatalf("parseNonNegativeDuration(%q) unexpectedly succeeded", value)
		}
	}
	if _, err := parsePositiveDuration("0s"); err == nil {
		t.Fatal("parsePositiveDuration(0s) unexpectedly succeeded")
	}
}

func TestParsePositiveInt(t *testing.T) {
	if got, err := parsePositiveInt("3"); err != nil || got != 3 {
		t.Fatalf("parsePositiveInt(3) = %d, %v", got, err)
	}
	for _, value := range []string{"0", "-1", "1.5", "invalid"} {
		if _, err := parsePositiveInt(value); err == nil {
			t.Fatalf("parsePositiveInt(%q) unexpectedly succeeded", value)
		}
	}
}

func setRequiredConfigEnv(t *testing.T) {
	t.Helper()
	t.Setenv(config_env.POSTGRES_USERS_DB, "postgres://user:password@localhost:5432/test")
	t.Setenv(config_env.DATABASE_SAVE_MESSAGES, "false")
	t.Setenv(config_env.GLOBAL_API_KEY, "test-api-key")
	t.Setenv(config_env.AMQP_URL, "")
	t.Setenv(config_env.MINIO_ENABLED, "false")
}
