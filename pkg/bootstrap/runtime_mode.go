package bootstrap

import (
	"fmt"
	"strings"
)

type RuntimeMode string

const (
	RuntimeModeActive  RuntimeMode = "active"
	RuntimeModeStandby RuntimeMode = "standby"
)

// ParseRuntimeMode keeps the historical active server as the default and
// rejects unknown topology values instead of silently starting the data plane.
func ParseRuntimeMode(value string) (RuntimeMode, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return RuntimeModeActive, nil
	}
	switch RuntimeMode(value) {
	case RuntimeModeActive, RuntimeModeStandby:
		return RuntimeMode(value), nil
	default:
		return "", fmt.Errorf("unknown runtime mode %q", value)
	}
}
