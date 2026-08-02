package bootstrap

import "testing"

func TestParseRuntimeMode(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		want      RuntimeMode
		wantError bool
	}{
		{name: "default active", want: RuntimeModeActive},
		{name: "explicit active", value: "active", want: RuntimeModeActive},
		{name: "standby", value: "standby", want: RuntimeModeStandby},
		{name: "surrounding whitespace", value: " standby ", want: RuntimeModeStandby},
		{name: "unknown", value: "passive", wantError: true},
		{name: "wrong case", value: "STANDBY", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseRuntimeMode(test.value)
			if test.wantError {
				if err == nil {
					t.Fatalf("ParseRuntimeMode(%q) unexpectedly succeeded", test.value)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("ParseRuntimeMode(%q) = %q, %v; want %q", test.value, got, err, test.want)
			}
		})
	}
}
