package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSensitiveValue(t *testing.T) {
	const name = "OMNIWA_TEST_SECRET"
	t.Run("direct value", func(t *testing.T) {
		t.Setenv(name, "direct-secret")
		t.Setenv(name+"_FILE", "")
		value, err := readSensitiveValue(name)
		if err != nil || value != "direct-secret" {
			t.Fatalf("value=%q error=%v", value, err)
		}
	})

	t.Run("file value trims line ending only", func(t *testing.T) {
		path := writeSecretFixture(t, "  file-secret  \r\n")
		t.Setenv(name, "")
		t.Setenv(name+"_FILE", path)
		value, err := readSensitiveValue(name)
		if err != nil || value != "  file-secret  " {
			t.Fatalf("value=%q error=%v", value, err)
		}
	})

	t.Run("empty direct value permits file source", func(t *testing.T) {
		path := writeSecretFixture(t, "file-secret\n")
		t.Setenv(name, "")
		t.Setenv(name+"_FILE", path)
		value, err := readSensitiveValue(name)
		if err != nil || value != "file-secret" {
			t.Fatalf("value=%q error=%v", value, err)
		}
	})
}

func TestReadSensitiveValueRejectsAmbiguousOrUnsafeSources(t *testing.T) {
	const name = "OMNIWA_TEST_SECRET"
	t.Run("ambiguous", func(t *testing.T) {
		t.Setenv(name, "direct-secret")
		t.Setenv(name+"_FILE", writeSecretFixture(t, "file-secret"))
		if _, err := readSensitiveValue(name); err == nil {
			t.Fatal("ambiguous secret sources were accepted")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		t.Setenv(name, "")
		t.Setenv(name+"_FILE", filepath.Join(t.TempDir(), "missing"))
		if _, err := readSensitiveValue(name); err == nil {
			t.Fatal("missing secret file was accepted")
		}
	})

	t.Run("non-regular file", func(t *testing.T) {
		t.Setenv(name, "")
		t.Setenv(name+"_FILE", t.TempDir())
		if _, err := readSensitiveValue(name); err == nil {
			t.Fatal("non-regular secret source was accepted")
		}
	})

	t.Run("oversized", func(t *testing.T) {
		t.Setenv(name, "")
		t.Setenv(name+"_FILE", writeSecretFixture(t, strings.Repeat("x", maxSecretFileBytes+1)))
		if _, err := readSensitiveValue(name); err == nil {
			t.Fatal("oversized secret file was accepted")
		}
	})

	t.Run("NUL byte", func(t *testing.T) {
		t.Setenv(name, "")
		t.Setenv(name+"_FILE", writeSecretFixture(t, "secret\x00suffix"))
		if _, err := readSensitiveValue(name); err == nil {
			t.Fatal("secret file with NUL byte was accepted")
		}
	})
}

func writeSecretFixture(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
