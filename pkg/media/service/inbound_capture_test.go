package media_service

import (
	"bytes"
	"context"
	"testing"
	"time"

	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	media_repository "github.com/evolution-foundation/evolution-go/pkg/media/repository"
	"github.com/google/uuid"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

type inboundCaptureRepositoryFake struct {
	media_repository.InboundRepository
	input media_repository.CaptureInboundInput
}

func (f *inboundCaptureRepositoryFake) Capture(_ context.Context, input media_repository.CaptureInboundInput) (*media_model.Asset, bool, error) {
	f.input = input
	return &media_model.Asset{ID: input.AssetID, InstanceID: input.InstanceID}, true, nil
}

func TestInboundCaptureStoresOnlyEncryptedDescriptor(t *testing.T) {
	repository := &inboundCaptureRepositoryFake{}
	cipher, err := NewDescriptorCipher(map[int][]byte{3: bytes.Repeat([]byte{9}, 32)}, 3)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	service := NewInboundCaptureService(repository, cipher, InboundCaptureSettings{
		MaxBytes: 1024, MaxPixels: 100, MaxAttempts: 4, Retention: 24 * time.Hour,
	})
	service.now = func() time.Time { return now }
	instanceID := uuid.NewString()
	event := inboundImageEvent(now, false)

	assetID, relevant, err := service.Capture(context.Background(), instanceID, event)
	if err != nil || !relevant || uuid.Validate(assetID) != nil {
		t.Fatalf("capture=%q/%t/%v", assetID, relevant, err)
	}
	input := repository.input
	if input.InstanceID != instanceID || input.MessageID != "message-1" || input.MaxAttempts != 4 || input.DescriptorKeyVersion != 3 ||
		!input.ExpiresAt.Equal(now.Add(24*time.Hour)) || bytes.Contains(input.DescriptorCiphertext, []byte("/provider/image")) {
		t.Fatalf("capture input=%+v", input)
	}
	decoded, err := cipher.Decrypt(instanceID, input.MessageID, input.AssetID, EncryptedDescriptor{
		Ciphertext: input.DescriptorCiphertext, Nonce: input.DescriptorNonce, KeyVersion: input.DescriptorKeyVersion,
	})
	if err != nil || decoded.DirectPath != "/provider/image" || decoded.SizeBytes != 128 {
		t.Fatalf("descriptor=%+v err=%v", decoded, err)
	}
}

func TestInboundCaptureIgnoresOutgoingAndNonImageMessages(t *testing.T) {
	cipher, _ := NewDescriptorCipher(map[int][]byte{1: bytes.Repeat([]byte{1}, 32)}, 1)
	service := NewInboundCaptureService(&inboundCaptureRepositoryFake{}, cipher, InboundCaptureSettings{
		MaxBytes: 1024, MaxPixels: 100, MaxAttempts: 3, Retention: time.Hour,
	})
	now := time.Now().UTC()
	for _, raw := range []any{inboundImageEvent(now, true), &events.Message{Message: &waE2E.Message{Conversation: proto.String("hello")}}} {
		assetID, relevant, err := service.Capture(context.Background(), uuid.NewString(), raw)
		if err != nil || relevant || assetID != "" {
			t.Fatalf("ignored capture=%q/%t/%v", assetID, relevant, err)
		}
	}
}

func inboundImageEvent(at time.Time, fromMe bool) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{MessageSource: types.MessageSource{IsFromMe: fromMe}, ID: "message-1", Timestamp: at},
		Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			DirectPath: proto.String("/provider/image"), FileEncSHA256: bytes.Repeat([]byte{1}, 32),
			FileSHA256: bytes.Repeat([]byte{2}, 32), MediaKey: bytes.Repeat([]byte{3}, 32),
			Mimetype: proto.String("image/png"), FileLength: proto.Uint64(128), Width: proto.Uint32(8), Height: proto.Uint32(8),
		}},
	}
}
