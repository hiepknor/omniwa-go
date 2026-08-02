package instance_credential

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
)

const (
	generatedInstanceTokenBytes = 32
	minCustomInstanceTokenBytes = 32
	maxInstanceTokenBytes       = 512
)

var ErrInvalidNewInstanceToken = errors.New("new instance token must be 32 to 512 visible ASCII characters")

// GenerateInstanceToken returns a URL-safe bearer credential with 256 bits of
// entropy. Callers must treat it as secret response material.
func GenerateInstanceToken() (string, error) {
	value := make([]byte, generatedInstanceTokenBytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// PrepareNewInstanceToken generates the secure default when no custom token is
// supplied and applies a bounded transport-safe policy to custom credentials.
// Existing stored credentials are intentionally unaffected.
func PrepareNewInstanceToken(requested string) (string, error) {
	if requested == "" {
		return GenerateInstanceToken()
	}
	if len(requested) < minCustomInstanceTokenBytes || len(requested) > maxInstanceTokenBytes {
		return "", ErrInvalidNewInstanceToken
	}
	for index := 0; index < len(requested); index++ {
		if requested[index] < 0x21 || requested[index] > 0x7e {
			return "", ErrInvalidNewInstanceToken
		}
	}
	return requested, nil
}
