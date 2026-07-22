package config

import (
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
	t.Setenv(config_env.INSTANCE_TOKEN_HMAC_KEY, "")
	t.Setenv(config_env.INSTANCE_TOKEN_HMAC_KEY_VERSION, "")
	t.Setenv(config_env.INSTANCE_TOKEN_BACKFILL_BATCH, "")
	t.Setenv(config_env.INSTANCE_TOKEN_BACKFILL_MAX_BATCHES, "")

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
	if config.RemoteMedia.Policy != "public_only" || config.RemoteMedia.Timeout != 15*time.Second || config.RemoteMedia.MaxBytes != 32*1024*1024 || len(config.RemoteMedia.AllowedHosts) != 0 {
		t.Fatalf("remote media defaults = %+v", config.RemoteMedia)
	}
	if config.Webhook.Timeout != 10*time.Second || config.Webhook.MaxRequestBytes != 4*1024*1024 || config.Webhook.MaxResponseBytes != 64*1024 || config.Webhook.AllowPrivate || len(config.Webhook.AllowedHosts) != 0 || len(config.Webhook.AllowedPorts) != 2 {
		t.Fatalf("webhook defaults are invalid")
	}
	if len(config.InstanceTokenHMACKey) != 0 || config.InstanceTokenHMACKeyVersion != 0 || config.InstanceTokenBackfillBatch != 100 || config.InstanceTokenBackfillMaxBatches != 10 {
		t.Fatalf("instance token digest defaults are invalid")
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
	t.Setenv(config_env.INSTANCE_TOKEN_HMAC_KEY, "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv(config_env.INSTANCE_TOKEN_HMAC_KEY_VERSION, "7")
	t.Setenv(config_env.INSTANCE_TOKEN_BACKFILL_BATCH, "25")
	t.Setenv(config_env.INSTANCE_TOKEN_BACKFILL_MAX_BATCHES, "4")

	config := Load()
	if config.RemoteMedia.Policy != "allowlist" || config.RemoteMedia.Timeout != 3*time.Second || config.RemoteMedia.MaxBytes != 4096 || len(config.RemoteMedia.AllowedHosts) != 2 {
		t.Fatalf("remote media overrides = %+v", config.RemoteMedia)
	}
	if config.Webhook.Timeout != 4*time.Second || config.Webhook.MaxRequestBytes != 2048 || config.Webhook.MaxResponseBytes != 1024 || !config.Webhook.AllowPrivate || len(config.Webhook.AllowedHosts) != 2 || len(config.Webhook.AllowedPorts) != 2 {
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
	if len(config.InstanceTokenHMACKey) != 32 || config.InstanceTokenHMACKeyVersion != 7 || config.InstanceTokenBackfillBatch != 25 || config.InstanceTokenBackfillMaxBatches != 4 {
		t.Fatalf("instance token digest overrides are invalid")
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
