package minio_storage

import (
	"context"
	"testing"
)

func TestLegacyMediaObjectPathValidation(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "ABC-123.jpg", want: legacyMediaPrefix + "ABC-123.jpg", ok: true},
		{input: legacyMediaPrefix + "ABC-123.jpg", want: legacyMediaPrefix + "ABC-123.jpg", ok: true},
		{input: "../secret", ok: false},
		{input: legacyMediaPrefix + "../secret", ok: false},
		{input: "foreign/key.jpg", ok: false},
		{input: "", ok: false},
	}
	storage := &MinioMediaStorage{}
	for _, test := range tests {
		got, err := storage.resolveFilePath(context.Background(), test.input)
		if (err == nil) != test.ok || got != test.want {
			t.Errorf("resolveFilePath(%q) = %q, %v", test.input, got, err)
		}
	}
}
