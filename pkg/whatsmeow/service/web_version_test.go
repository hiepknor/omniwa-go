package whatsmeow_service

import (
	"errors"
	"testing"

	"github.com/evolution-foundation/evolution-go/pkg/config"
	"go.mau.fi/whatsmeow/store"
	"google.golang.org/protobuf/proto"
)

func TestResolveWhatsAppWebVersionPrefersCompleteConfig(t *testing.T) {
	fetched := false
	version, source, err := resolveWhatsAppWebVersion(&config.Config{
		WhatsappVersionMajor: 2,
		WhatsappVersionMinor: 3000,
		WhatsappVersionPatch: 123456,
	}, func() (*clientVersion, error) {
		fetched = true
		return nil, errors.New("must not be called")
	}, clientVersion{Major: 2, Minor: 3000, Patch: 1})
	if err != nil {
		t.Fatalf("resolve configured version: %v", err)
	}
	if fetched {
		t.Fatal("web version fetch ran despite complete configured version")
	}
	if source != "config" || version != (clientVersion{Major: 2, Minor: 3000, Patch: 123456}) {
		t.Fatalf("unexpected configured result: source=%q version=%+v", source, version)
	}
}

func TestResolveWhatsAppWebVersionUsesFetchedVersion(t *testing.T) {
	want := clientVersion{Major: 2, Minor: 3000, Patch: 654321}
	version, source, err := resolveWhatsAppWebVersion(&config.Config{}, func() (*clientVersion, error) {
		return &want, nil
	}, clientVersion{Major: 2, Minor: 3000, Patch: 1})
	if err != nil {
		t.Fatalf("resolve fetched version: %v", err)
	}
	if source != "web" || version != want {
		t.Fatalf("unexpected fetched result: source=%q version=%+v", source, version)
	}
}

func TestResolveWhatsAppWebVersionFallsBackOnFetchFailure(t *testing.T) {
	want := clientVersion{Major: 2, Minor: 3000, Patch: 111111}
	fetchErr := errors.New("provider unavailable")
	version, source, err := resolveWhatsAppWebVersion(&config.Config{}, func() (*clientVersion, error) {
		return nil, fetchErr
	}, want)
	if !errors.Is(err, fetchErr) {
		t.Fatalf("expected fetch failure, got %v", err)
	}
	if source != "library_default" || version != want {
		t.Fatalf("unexpected fallback result: source=%q version=%+v", source, version)
	}
}

func TestApplyWhatsAppWebVersionSynchronizesHandshakePayload(t *testing.T) {
	previousVersion := store.GetWAVersion()
	previousDeviceVersion := [3]uint32{
		store.DeviceProps.GetVersion().GetPrimary(),
		store.DeviceProps.GetVersion().GetSecondary(),
		store.DeviceProps.GetVersion().GetTertiary(),
	}
	t.Cleanup(func() {
		store.SetWAVersion(previousVersion)
		store.DeviceProps.Version.Primary = proto.Uint32(previousDeviceVersion[0])
		store.DeviceProps.Version.Secondary = proto.Uint32(previousDeviceVersion[1])
		store.DeviceProps.Version.Tertiary = proto.Uint32(previousDeviceVersion[2])
	})

	want := clientVersion{Major: 2, Minor: 3000, Patch: 777777}
	applyWhatsAppWebVersion(want)

	if got := store.GetWAVersion(); got != (store.WAVersionContainer{2, 3000, 777777}) {
		t.Fatalf("unexpected whatsmeow version: %v", got)
	}
	appVersion := store.BaseClientPayload.GetUserAgent().GetAppVersion()
	if appVersion.GetPrimary() != 2 || appVersion.GetSecondary() != 3000 || appVersion.GetTertiary() != 777777 {
		t.Fatalf("handshake user-agent version was not synchronized: %s", store.GetWAVersion().String())
	}
	deviceVersion := store.DeviceProps.GetVersion()
	if deviceVersion.GetPrimary() != 2 || deviceVersion.GetSecondary() != 3000 || deviceVersion.GetTertiary() != 777777 {
		t.Fatalf("pairing device version was not synchronized: %d.%d.%d", deviceVersion.GetPrimary(), deviceVersion.GetSecondary(), deviceVersion.GetTertiary())
	}
}
