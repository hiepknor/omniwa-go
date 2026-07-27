package media_service

import (
	"bytes"
	"testing"

	"github.com/google/uuid"
)

func TestDescriptorCipherRoundTripBindsIdentityAndVersion(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	cipher, err := NewDescriptorCipher(map[int][]byte{7: key}, 7)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := validInboundDescriptor()
	instanceID, assetID := uuid.NewString(), uuid.NewString()
	encrypted, err := cipher.Encrypt(instanceID, "message-a", assetID, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if encrypted.KeyVersion != 7 || len(encrypted.Nonce) != 12 || bytes.Contains(encrypted.Ciphertext, []byte(descriptor.DirectPath)) {
		t.Fatalf("unsafe encrypted envelope: %+v", encrypted)
	}
	decoded, err := cipher.Decrypt(instanceID, "message-a", assetID, *encrypted)
	if err != nil || decoded.DirectPath != descriptor.DirectPath || !bytes.Equal(decoded.MediaKey, descriptor.MediaKey) {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	if _, err := cipher.Decrypt(instanceID, "message-b", assetID, *encrypted); err == nil {
		t.Fatal("expected AAD identity mismatch to fail")
	}
	tampered := *encrypted
	tampered.Ciphertext = append([]byte(nil), encrypted.Ciphertext...)
	tampered.Ciphertext[0] ^= 0xff
	if _, err := cipher.Decrypt(instanceID, "message-a", assetID, tampered); err == nil {
		t.Fatal("expected ciphertext tampering to fail")
	}
}

func validInboundDescriptor() InboundImageDescriptor {
	return InboundImageDescriptor{
		DirectPath: "/provider/image", FileEncSHA256: bytes.Repeat([]byte{1}, 32),
		FileSHA256: bytes.Repeat([]byte{2}, 32), MediaKey: bytes.Repeat([]byte{3}, 32),
		MIMEType: "image/png", SizeBytes: 128, Width: 8, Height: 8,
	}
}
