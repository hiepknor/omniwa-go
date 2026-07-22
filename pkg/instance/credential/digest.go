// Package instance_credential provides one-way lookup digests for instance
// bearer tokens. It deliberately does not expose or retain plaintext tokens.
package instance_credential

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

const digestDomain = "omniwa-instance-token\x00"

type Digester struct {
	key     []byte
	version int
}

func NewDigester(key []byte, version int) (*Digester, error) {
	if len(key) < 32 {
		return nil, errors.New("instance token HMAC key must contain at least 32 bytes")
	}
	if version <= 0 {
		return nil, errors.New("instance token HMAC key version must be positive")
	}
	return &Digester{key: append([]byte(nil), key...), version: version}, nil
}

func (d *Digester) Digest(token string) (string, int, error) {
	if d == nil || len(d.key) == 0 {
		return "", 0, errors.New("instance token digester is not configured")
	}
	if token == "" {
		return "", 0, errors.New("instance token is required")
	}
	mac := hmac.New(sha256.New, d.key)
	_, _ = mac.Write([]byte(digestDomain))
	_, _ = mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil)), d.version, nil
}
