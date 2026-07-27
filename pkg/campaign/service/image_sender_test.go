package campaign_service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	campaign_repository "github.com/evolution-foundation/evolution-go/pkg/campaign/repository"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	send_service "github.com/evolution-foundation/evolution-go/pkg/sendMessage/service"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types"
)

type imageMediaRepositoryFake struct {
	campaign_repository.MediaAssetRepository
	asset *campaign_model.MediaAsset
	err   error
}

func (f *imageMediaRepositoryFake) Get(context.Context, string, string) (*campaign_model.MediaAsset, error) {
	return f.asset, f.err
}

type imageStoreFake struct {
	data    []byte
	err     error
	openKey string
}

func (f *imageStoreFake) Put(context.Context, string, io.Reader, int64, string) error { return nil }
func (f *imageStoreFake) Open(_ context.Context, key string) (io.ReadCloser, error) {
	f.openKey = key
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(bytes.NewReader(f.data)), nil
}
func (f *imageStoreFake) Delete(context.Context, string) error { return nil }
func (f *imageStoreFake) Health(context.Context) error         { return nil }

type imageSendServiceFake struct {
	input *send_service.MediaStruct
	data  []byte
	info  *send_service.MessageSendStruct
	err   error
	calls int
}

func (f *imageSendServiceFake) SendImageOnce(_ context.Context, input *send_service.MediaStruct, data []byte, _ *instance_model.Instance) (*send_service.MessageSendStruct, error) {
	f.calls++
	f.input, f.data = input, append([]byte(nil), data...)
	return f.info, f.err
}

func TestImageSenderVerifiesSnapshotAndUsesSingleAttemptBoundary(t *testing.T) {
	content := []byte("normalized-private-image")
	campaign, recipient, asset := imageSenderFixture(content)
	store := &imageStoreFake{data: content}
	sends := &imageSendServiceFake{info: &send_service.MessageSendStruct{Info: types.MessageInfo{ID: "provider-image-id"}}}
	sender := NewImageSender(instanceReaderFake{instance: &instance_model.Instance{Id: campaign.InstanceID}}, &imageMediaRepositoryFake{asset: asset}, store, sends, 1024)
	providerID, err := sender.Send(context.Background(), campaign, recipient)
	if err != nil || providerID != "provider-image-id" || sends.calls != 1 || !bytes.Equal(sends.data, content) || store.openKey != asset.ObjectKey {
		t.Fatalf("provider=%q err=%v calls=%d data=%q key=%q", providerID, err, sends.calls, sends.data, store.openKey)
	}
	if sends.input == nil || sends.input.Type != "image" || sends.input.Caption != "Device caption" ||
		sends.input.Number != recipient.RecipientJID || sends.input.Id != deterministicMessageID(recipient.ID) {
		t.Fatalf("send input=%+v", sends.input)
	}
}

func TestImageSenderRejectsSnapshotOrObjectMismatchBeforeProviderBoundary(t *testing.T) {
	content := []byte("normalized-private-image")
	for _, mutate := range []func(*campaign_model.Campaign, *campaign_model.MediaAsset, *imageStoreFake){
		func(_ *campaign_model.Campaign, asset *campaign_model.MediaAsset, _ *imageStoreFake) {
			*asset.SHA256 = strings.Repeat("a", 64)
		},
		func(_ *campaign_model.Campaign, _ *campaign_model.MediaAsset, store *imageStoreFake) {
			store.data = []byte("corrupt")
		},
	} {
		campaign, recipient, asset := imageSenderFixture(content)
		store := &imageStoreFake{data: content}
		mutate(campaign, asset, store)
		sends := &imageSendServiceFake{}
		sender := NewImageSender(instanceReaderFake{instance: &instance_model.Instance{Id: campaign.InstanceID}}, &imageMediaRepositoryFake{asset: asset}, store, sends, 1024)
		_, err := sender.Send(context.Background(), campaign, recipient)
		var delivery *DeliveryError
		if !errors.As(err, &delivery) || delivery.Kind != DeliveryFailureTerminal || delivery.Code != "campaign_media_integrity_failed" || sends.calls != 0 {
			t.Fatalf("delivery=%+v err=%v calls=%d", delivery, err, sends.calls)
		}
	}
}

func TestImageSenderSeparatesRetryableUploadFromUnknownSendOutcome(t *testing.T) {
	content := []byte("normalized-private-image")
	for _, test := range []struct {
		err  error
		kind DeliveryFailureKind
		code string
	}{
		{err: &send_service.ProviderMediaUploadError{Cause: errors.New("upload unavailable")}, kind: DeliveryFailureTransient, code: "provider_media_upload_failed"},
		{err: &send_service.ProviderSendError{Cause: errors.New("ack lost")}, kind: DeliveryFailureUnknown, code: "unknown_send_outcome"},
	} {
		campaign, recipient, asset := imageSenderFixture(content)
		sender := NewImageSender(
			instanceReaderFake{instance: &instance_model.Instance{Id: campaign.InstanceID}},
			&imageMediaRepositoryFake{asset: asset}, &imageStoreFake{data: content}, &imageSendServiceFake{err: test.err}, 1024,
		)
		_, err := sender.Send(context.Background(), campaign, recipient)
		var delivery *DeliveryError
		if !errors.As(err, &delivery) || delivery.Kind != test.kind || delivery.Code != test.code {
			t.Fatalf("delivery=%+v err=%v", delivery, err)
		}
	}
}

func TestContentSenderDispatchesByPersistedContentType(t *testing.T) {
	text := senderFake{providerID: "text-provider-id"}
	image := senderFake{providerID: "image-provider-id"}
	sender := NewContentSender(text, image)
	for _, test := range []struct {
		contentType campaign_model.CampaignContentType
		expected    string
	}{
		{contentType: campaign_model.CampaignContentText, expected: "text-provider-id"},
		{contentType: campaign_model.CampaignContentImage, expected: "image-provider-id"},
	} {
		providerID, err := sender.Send(context.Background(), &campaign_model.Campaign{ContentType: test.contentType}, &campaign_model.Recipient{})
		if err != nil || providerID != test.expected {
			t.Fatalf("content=%q provider=%q err=%v", test.contentType, providerID, err)
		}
	}

	_, err := NewContentSender(text, nil).Send(context.Background(), &campaign_model.Campaign{ContentType: campaign_model.CampaignContentImage}, &campaign_model.Recipient{})
	var delivery *DeliveryError
	if !errors.As(err, &delivery) || delivery.Kind != DeliveryFailureTerminal || delivery.Code != "campaign_image_content_disabled" {
		t.Fatalf("disabled image delivery=%+v err=%v", delivery, err)
	}
}

func imageSenderFixture(content []byte) (*campaign_model.Campaign, *campaign_model.Recipient, *campaign_model.MediaAsset) {
	instanceID, campaignID, recipientID, assetID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	mime, size, digest := "image/jpeg", int64(len(content)), sha256Hex(content)
	width, height := 4, 3
	campaign := &campaign_model.Campaign{
		ID: campaignID, InstanceID: instanceID, ContentType: campaign_model.CampaignContentImage, TextBody: "Device caption",
		MediaAssetID: &assetID, MediaMIMEType: &mime, MediaSizeBytes: &size, MediaWidth: &width, MediaHeight: &height, MediaSHA256: &digest,
	}
	recipient := &campaign_model.Recipient{
		ID: recipientID, CampaignID: campaignID, InstanceID: instanceID, RecipientJID: "120363000001@g.us", TargetType: campaign_model.RecipientTargetGroup,
	}
	asset := &campaign_model.MediaAsset{
		ID: assetID, InstanceID: instanceID, ObjectKey: "campaign-media/" + instanceID + "/" + assetID + "/image",
		Status: campaign_model.MediaAssetStatusReady, MIMEType: &mime, SizeBytes: &size, Width: &width, Height: &height, SHA256: &digest,
	}
	return campaign, recipient, asset
}
