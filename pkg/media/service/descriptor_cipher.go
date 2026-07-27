package media_service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
)

type InboundImageDescriptor struct {
	DirectPath    string `json:"directPath"`
	FileEncSHA256 []byte `json:"fileEncSha256"`
	FileSHA256    []byte `json:"fileSha256"`
	MediaKey      []byte `json:"mediaKey"`
	MIMEType      string `json:"mimeType"`
	SizeBytes     int64  `json:"sizeBytes"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
}

type EncryptedDescriptor struct {
	Ciphertext []byte
	Nonce      []byte
	KeyVersion int
}

type DescriptorCipher struct {
	keys          map[int]cipher.AEAD
	activeVersion int
	random        io.Reader
}

func NewDescriptorCipher(keys map[int][]byte, activeVersion int) (*DescriptorCipher, error) {
	if activeVersion < 1 || len(keys) == 0 {
		return nil, errors.New("descriptor key ring and active version are required")
	}
	result := &DescriptorCipher{keys: make(map[int]cipher.AEAD, len(keys)), activeVersion: activeVersion, random: rand.Reader}
	for version, key := range keys {
		if version < 1 || len(key) != 32 {
			return nil, errors.New("descriptor keys must be versioned AES-256 keys")
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		result.keys[version] = aead
	}
	if result.keys[activeVersion] == nil {
		return nil, errors.New("active descriptor key version is not present")
	}
	return result, nil
}

func (c *DescriptorCipher) Encrypt(instanceID, messageID, assetID string, descriptor InboundImageDescriptor) (*EncryptedDescriptor, error) {
	if err := validateDescriptorIdentity(c, instanceID, messageID, assetID); err != nil {
		return nil, err
	}
	if err := validateInboundDescriptor(descriptor, 64*1024*1024, 100_000_000); err != nil {
		return nil, err
	}
	plaintext, err := json.Marshal(descriptor)
	if err != nil {
		return nil, err
	}
	aead := c.keys[c.activeVersion]
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(c.random, nonce); err != nil {
		return nil, err
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, descriptorAAD(instanceID, messageID, assetID, c.activeVersion))
	return &EncryptedDescriptor{Ciphertext: ciphertext, Nonce: nonce, KeyVersion: c.activeVersion}, nil
}

func (c *DescriptorCipher) Decrypt(instanceID, messageID, assetID string, encrypted EncryptedDescriptor) (*InboundImageDescriptor, error) {
	if err := validateDescriptorIdentity(c, instanceID, messageID, assetID); err != nil || encrypted.KeyVersion < 1 {
		return nil, errors.New("encrypted descriptor identity is invalid")
	}
	aead := c.keys[encrypted.KeyVersion]
	if aead == nil || len(encrypted.Nonce) != aead.NonceSize() || len(encrypted.Ciphertext) <= aead.Overhead() {
		return nil, errors.New("encrypted descriptor key or envelope is unavailable")
	}
	plaintext, err := aead.Open(nil, encrypted.Nonce, encrypted.Ciphertext, descriptorAAD(instanceID, messageID, assetID, encrypted.KeyVersion))
	if err != nil {
		return nil, errors.New("decrypt inbound media descriptor")
	}
	var descriptor InboundImageDescriptor
	if err := json.Unmarshal(plaintext, &descriptor); err != nil {
		return nil, errors.New("decode inbound media descriptor")
	}
	return &descriptor, nil
}

func validateDescriptorIdentity(c *DescriptorCipher, instanceID, messageID, assetID string) error {
	if c == nil || c.random == nil || len(c.keys) == 0 || c.activeVersion < 1 || strings.TrimSpace(instanceID) == "" ||
		uuid.Validate(instanceID) != nil || strings.TrimSpace(messageID) == "" || len(messageID) > 255 || uuid.Validate(assetID) != nil {
		return errors.New("descriptor cipher and bounded identity are required")
	}
	return nil
}

func descriptorAAD(instanceID, messageID, assetID string, version int) []byte {
	return []byte(fmt.Sprintf("omniwa-media-descriptor-v1\x00%d\x00%s\x00%s\x00%s", version, instanceID, messageID, assetID))
}

func validateInboundDescriptor(descriptor InboundImageDescriptor, maxBytes, maxPixels int64) error {
	validDimensions := descriptor.Width == 0 && descriptor.Height == 0 ||
		descriptor.Width >= 1 && descriptor.Height >= 1 && descriptor.Width <= 32768 && descriptor.Height <= 32768 &&
			int64(descriptor.Width) <= maxPixels/int64(descriptor.Height)
	if !strings.HasPrefix(descriptor.DirectPath, "/") || len(descriptor.DirectPath) > 2048 || len(descriptor.FileEncSHA256) != 32 ||
		len(descriptor.FileSHA256) != 32 || len(descriptor.MediaKey) != 32 || descriptor.SizeBytes < 1 || descriptor.SizeBytes > maxBytes ||
		!validDimensions || len(descriptor.MIMEType) > 128 {
		return ErrInvalidMediaAsset
	}
	return nil
}
