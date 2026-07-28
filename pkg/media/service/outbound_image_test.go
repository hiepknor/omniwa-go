package media_service

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	media_model "github.com/evolution-foundation/evolution-go/pkg/media/model"
	media_repository "github.com/evolution-foundation/evolution-go/pkg/media/repository"
	send_service "github.com/evolution-foundation/evolution-go/pkg/sendMessage/service"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types"
)

type outboundRepositoryFake struct {
	media_repository.Repository
	asset   *media_model.Asset
	getErr  error
	addErr  error
	added   []media_model.AssetReference
	removed []media_model.AssetReference
}

func (f *outboundRepositoryFake) Get(context.Context, string, string) (*media_model.Asset, error) {
	return f.asset, f.getErr
}
func (f *outboundRepositoryFake) GetMetadata(context.Context, string, string) (*media_model.Asset, error) {
	return f.asset, f.getErr
}
func (f *outboundRepositoryFake) AddReference(_ context.Context, reference media_model.AssetReference) error {
	if f.addErr == nil {
		f.added = append(f.added, reference)
	}
	return f.addErr
}
func (f *outboundRepositoryFake) RemoveReference(_ context.Context, instanceID, assetID string, ownerType media_model.ReferenceOwnerType, ownerID string) error {
	f.removed = append(f.removed, media_model.AssetReference{InstanceID: instanceID, MediaAssetID: assetID, OwnerType: ownerType, OwnerID: ownerID})
	return nil
}

type outboundStoreFake struct {
	bytes   string
	openErr error
}

func (f *outboundStoreFake) Put(context.Context, string, io.Reader, int64, string) error { return nil }
func (f *outboundStoreFake) Open(context.Context, string) (io.ReadCloser, error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	return io.NopCloser(strings.NewReader(f.bytes)), nil
}
func (f *outboundStoreFake) Delete(context.Context, string) error { return nil }
func (f *outboundStoreFake) Health(context.Context) error         { return nil }

type outboundSenderFake struct {
	err   error
	calls int
	data  *send_service.MediaStruct
}

func (f *outboundSenderFake) SendImageOnce(_ context.Context, data *send_service.MediaStruct, _ []byte, _ *instance_model.Instance) (*send_service.MessageSendStruct, error) {
	f.calls++
	f.data = data
	if f.err != nil {
		return nil, f.err
	}
	return &send_service.MessageSendStruct{Info: types.MessageInfo{ID: types.MessageID(data.Id)}}, nil
}

func readyOutboundAsset(instanceID, bytes string) *media_model.Asset {
	assetID := uuid.NewString()
	return &media_model.Asset{
		ID: assetID, InstanceID: instanceID, Status: media_model.AssetStatusReady,
		Canonical: &media_model.AssetVariant{
			MediaAssetID: assetID, InstanceID: instanceID, Kind: media_model.VariantCanonical,
			ObjectKey: "media-assets/" + instanceID + "/" + assetID + "/canonical", MIMEType: "image/png",
			SizeBytes: int64(len(bytes)), SHA256: sha256Bytes([]byte(bytes)), Width: 1, Height: 1,
		},
	}
}

func TestOutboundImageSendFencesAssetAndKeepsReferenceOnSuccess(t *testing.T) {
	instanceID := uuid.NewString()
	payload := "normalized-image"
	asset := readyOutboundAsset(instanceID, payload)
	repository := &outboundRepositoryFake{asset: asset}
	sender := &outboundSenderFake{}
	service := NewOutboundImageService(repository, &outboundStoreFake{bytes: payload}, sender, 1024, 24*time.Hour)
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	data := &send_service.MediaStruct{Number: "120363000001@g.us", Type: "image", MediaAssetID: asset.ID, Caption: "caption"}

	result, err := service.Send(context.Background(), data, &instance_model.Instance{Id: instanceID})
	if err != nil || result == nil || sender.calls != 1 || data.Id == "" {
		t.Fatalf("send result=%+v calls=%d id=%q err=%v", result, sender.calls, data.Id, err)
	}
	if result.AcknowledgementID != data.Id || result.AcknowledgedAt == nil || !result.AcknowledgedAt.Equal(now) {
		t.Fatalf("acknowledgement=%+v", result)
	}
	if len(repository.added) != 1 || len(repository.removed) != 0 || repository.added[0].OwnerID != data.Id ||
		repository.added[0].RetentionUntil == nil || !repository.added[0].RetentionUntil.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("references added=%+v removed=%+v", repository.added, repository.removed)
	}
}

func TestOutboundImageSendRejectsUnavailableLifecycleStates(t *testing.T) {
	instanceID := uuid.NewString()
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		status media_model.AssetStatus
		mutate func(*media_model.Asset)
		want   error
	}{
		{name: "pending", status: media_model.AssetStatusPending, want: ErrMediaAssetNotReady},
		{name: "failed", status: media_model.AssetStatusFailed, want: ErrMediaAssetFailed},
		{name: "expired", status: media_model.AssetStatusReady, mutate: func(asset *media_model.Asset) { expired := now.Add(-time.Second); asset.ExpiresAt = &expired }, want: ErrMediaAssetExpired},
		{name: "deleted", status: media_model.AssetStatusDeleted, mutate: func(asset *media_model.Asset) { deleted := now; asset.DeletedAt = &deleted }, want: ErrMediaAssetDeleted},
	} {
		t.Run(test.name, func(t *testing.T) {
			asset := readyOutboundAsset(instanceID, "content")
			asset.Status = test.status
			if test.mutate != nil {
				test.mutate(asset)
			}
			sender := &outboundSenderFake{}
			service := NewOutboundImageService(&outboundRepositoryFake{asset: asset}, &outboundStoreFake{bytes: "content"}, sender, 1024, time.Hour)
			service.now = func() time.Time { return now }
			_, err := service.Send(context.Background(), &send_service.MediaStruct{Number: "120363000001@g.us", Type: "image", MediaAssetID: asset.ID}, &instance_model.Instance{Id: instanceID})
			if !errors.Is(err, test.want) || sender.calls != 0 {
				t.Fatalf("err=%v calls=%d", err, sender.calls)
			}
		})
	}
}

func TestOutboundImageSendReleasesOnlyBeforeAdmission(t *testing.T) {
	for _, test := range []struct {
		name        string
		sendErr     error
		wantRemoved bool
	}{
		{name: "upload failure", sendErr: &send_service.ProviderMediaUploadError{Cause: errors.New("upload")}, wantRemoved: true},
		{name: "unknown outcome", sendErr: &send_service.ProviderSendError{Cause: errors.New("ack lost")}, wantRemoved: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			instanceID := uuid.NewString()
			payload := "normalized-image"
			asset := readyOutboundAsset(instanceID, payload)
			repository := &outboundRepositoryFake{asset: asset}
			service := NewOutboundImageService(repository, &outboundStoreFake{bytes: payload}, &outboundSenderFake{err: test.sendErr}, 1024, time.Hour)
			_, err := service.Send(context.Background(), &send_service.MediaStruct{
				Number: "120363000001@g.us", Type: "image", MediaAssetID: asset.ID,
			}, &instance_model.Instance{Id: instanceID})
			if !errors.Is(err, test.sendErr) || (len(repository.removed) == 1) != test.wantRemoved {
				t.Fatalf("err=%v removed=%+v", err, repository.removed)
			}
		})
	}
}

func TestOutboundImageSendRejectsIntegrityMismatchBeforeProvider(t *testing.T) {
	instanceID := uuid.NewString()
	asset := readyOutboundAsset(instanceID, "expected")
	repository := &outboundRepositoryFake{asset: asset}
	sender := &outboundSenderFake{}
	service := NewOutboundImageService(repository, &outboundStoreFake{bytes: "tampered"}, sender, 1024, time.Hour)
	_, err := service.Send(context.Background(), &send_service.MediaStruct{
		Number: "120363000001@g.us", Type: "image", MediaAssetID: asset.ID,
	}, &instance_model.Instance{Id: instanceID})
	if !errors.Is(err, ErrMediaAssetIntegrity) || sender.calls != 0 || len(repository.removed) != 1 {
		t.Fatalf("err=%v calls=%d removed=%+v", err, sender.calls, repository.removed)
	}
}
