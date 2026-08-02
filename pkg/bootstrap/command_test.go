package bootstrap

import "testing"

func TestParseCommand(t *testing.T) {
	tests := []struct {
		name      string
		arguments []string
		want      Command
		wantError bool
	}{
		{name: "default serve", want: CommandServe},
		{name: "migrate", arguments: []string{"migrate"}, want: CommandMigrate},
		{name: "unknown", arguments: []string{"unknown"}, wantError: true},
		{name: "too many", arguments: []string{"migrate", "extra"}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseCommand(test.arguments)
			if test.wantError {
				if err == nil {
					t.Fatalf("ParseCommand(%q) unexpectedly succeeded", test.arguments)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("ParseCommand(%q) = %q, %v; want %q", test.arguments, got, err, test.want)
			}
		})
	}
}
