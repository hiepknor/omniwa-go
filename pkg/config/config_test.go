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

	config := Load()
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
