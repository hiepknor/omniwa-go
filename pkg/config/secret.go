package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/gomessguii/logger"
)

const maxSecretFileBytes = 1 << 20

// sensitiveValue reads NAME or NAME_FILE. File-backed values support Docker
// Compose, Swarm, and Kubernetes secret mounts without placing credentials in
// the process environment. A non-empty direct value and file path are rejected
// together so deployment precedence cannot silently select the wrong secret.
func sensitiveValue(name string) string {
	value, err := readSensitiveValue(name)
	if err != nil {
		logger.LogFatal("[CONFIG] invalid secret source for %s: %v", name, err)
	}
	return value
}

func readSensitiveValue(name string) (string, error) {
	direct := os.Getenv(name)
	filePath := strings.TrimSpace(os.Getenv(name + "_FILE"))
	if direct != "" && filePath != "" {
		return "", fmt.Errorf("set either %s or %s_FILE, not both", name, name)
	}
	if filePath == "" {
		return direct, nil
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return "", fmt.Errorf("stat %s_FILE: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s_FILE must reference a regular file", name)
	}

	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("open %s_FILE: %w", name, err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxSecretFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("read %s_FILE: %w", name, err)
	}
	if len(data) > maxSecretFileBytes {
		return "", fmt.Errorf("%s_FILE exceeds %d bytes", name, maxSecretFileBytes)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", errors.New("secret files must not contain NUL bytes")
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}
