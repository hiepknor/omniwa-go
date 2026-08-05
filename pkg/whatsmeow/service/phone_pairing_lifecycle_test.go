package whatsmeow_service

import (
	"context"
	"errors"
	"testing"
	"time"

	instance_runtime "github.com/evolution-foundation/evolution-go/pkg/instance/runtime"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

func TestWaitPhonePairingReadyWaitsForRuntimeAndQRSignal(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	registry := instance_runtime.NewRegistry[*MyClient](parent)
	service := &whatsmeowService{runtimeRegistry: registry}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- service.WaitPhonePairingReady(ctx, "instance-a") }()

	state := &MyClient{pairReady: make(chan struct{})}
	if _, err := registry.Install("instance-a", new(whatsmeow.Client), state, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("wait returned before QR readiness: %v", err)
	case <-time.After(2 * phonePairingRuntimePollInterval):
	}

	state.signalPhonePairingReady()
	state.signalPhonePairingReady()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not observe QR readiness")
	}
}

func TestWaitPhonePairingReadyFollowsRuntimeReplacement(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	registry := instance_runtime.NewRegistry[*MyClient](parent)
	service := &whatsmeowService{runtimeRegistry: registry}
	first := &MyClient{pairReady: make(chan struct{})}
	if _, err := registry.Install("instance-a", new(whatsmeow.Client), first, nil); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- service.WaitPhonePairingReady(ctx, "instance-a") }()
	time.Sleep(2 * phonePairingRuntimePollInterval)

	second := &MyClient{pairReady: make(chan struct{})}
	if _, err := registry.Install("instance-a", new(whatsmeow.Client), second, nil); err != nil {
		t.Fatal(err)
	}
	first.signalPhonePairingReady()
	select {
	case err := <-done:
		t.Fatalf("stale generation released wait: %v", err)
	case <-time.After(2 * phonePairingRuntimePollInterval):
	}
	second.signalPhonePairingReady()
	if err := <-done; err != nil {
		t.Fatalf("replacement wait error = %v", err)
	}
}

func TestWaitPhonePairingReadyHonorsContext(t *testing.T) {
	service := &whatsmeowService{runtimeRegistry: instance_runtime.NewRegistry[*MyClient](context.Background())}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := service.WaitPhonePairingReady(ctx, "instance-a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestIsPairedClientRequiresDurableStoreIdentity(t *testing.T) {
	if isPairedClient(nil) || isPairedClient(new(whatsmeow.Client)) {
		t.Fatal("client without a durable identity was treated as paired")
	}
	jid := types.NewJID("15551234567", types.DefaultUserServer)
	client := &whatsmeow.Client{Store: &store.Device{ID: &jid}}
	if !isPairedClient(client) {
		t.Fatal("client with a durable identity was treated as unpaired")
	}
}

func TestPairingObservationTracksQRWithoutRaces(t *testing.T) {
	client := &MyClient{}
	client.markPairingStarted()
	client.markPairingQRSeen()

	elapsed, qrSeen := client.pairingObservation()
	if elapsed < 0 || !qrSeen {
		t.Fatalf("observation = (%s, %t), want non-negative elapsed and QR seen", elapsed, qrSeen)
	}

	client.markPairingStarted()
	_, qrSeen = client.pairingObservation()
	if qrSeen {
		t.Fatal("new pairing attempt retained the previous QR observation")
	}
}
