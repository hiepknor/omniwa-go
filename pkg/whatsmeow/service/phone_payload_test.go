package whatsmeow_service

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/evolution-foundation/evolution-go/pkg/config"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type phoneCaptureProducer struct {
	mu       sync.Mutex
	payloads [][]byte
}

func (p *phoneCaptureProducer) Produce(_ string, payload []byte, _, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.payloads = append(p.payloads, append([]byte(nil), payload...))
	return nil
}
func (*phoneCaptureProducer) CreateGlobalQueues() error { return nil }

func mustPhoneTestJID(t *testing.T, value string) types.JID {
	t.Helper()
	jid, err := types.ParseJID(value)
	if err != nil {
		t.Fatal(err)
	}
	return jid
}

func TestEnrichCurrentPhoneMetadataUsesExplicitCurrentMessageRoles(t *testing.T) {
	event := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{
		Sender: mustPhoneTestJID(t, "12345@lid"), SenderAlt: mustPhoneTestJID(t, "15550001@s.whatsapp.net"),
		Chat: mustPhoneTestJID(t, "15550002:4@s.whatsapp.net"), IsFromMe: true,
	}}}
	result := enrichCurrentPhoneMetadata([]byte(`{"event":"Message","data":{"legacy":"kept"}}`), event, true)
	var root map[string]any
	if err := json.Unmarshal(result, &root); err != nil {
		t.Fatal(err)
	}
	data := root["data"].(map[string]any)
	if data["senderPhoneNumber"] != "15550001" || data["recipientPhoneNumber"] != "15550002" || data["legacy"] != "kept" {
		t.Fatalf("data=%#v", data)
	}
}

func TestEnrichCurrentPhoneMetadataDisabledIsByteCompatible(t *testing.T) {
	payload := []byte(`{"event":"Message","data":{"legacy":"kept"}}`)
	event := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Sender: mustPhoneTestJID(t, "15550001@s.whatsapp.net")}}}
	if result := enrichCurrentPhoneMetadata(payload, event, false); string(result) != string(payload) {
		t.Fatalf("result=%s", result)
	}
}

func TestEnrichCurrentPhoneMetadataReceiptUsesRecipientRole(t *testing.T) {
	event := &events.Receipt{MessageSource: types.MessageSource{Sender: mustPhoneTestJID(t, "15550003@s.whatsapp.net")}}
	result := enrichCurrentPhoneMetadata([]byte(`{"event":"Receipt","data":{}}`), event, true)
	var root map[string]any
	_ = json.Unmarshal(result, &root)
	data := root["data"].(map[string]any)
	if data["recipientPhoneNumber"] != "15550003" {
		t.Fatalf("data=%#v", data)
	}
}

func TestEnrichCurrentPhoneMetadataSupportsOutboundProviderAcknowledgement(t *testing.T) {
	info := types.MessageInfo{MessageSource: types.MessageSource{
		Sender: mustPhoneTestJID(t, "15550004@s.whatsapp.net"),
		Chat:   mustPhoneTestJID(t, "15550005:7@s.whatsapp.net"), IsFromMe: true,
	}}
	result := enrichCurrentPhoneMetadata([]byte(`{"event":"SendMessage","data":{"legacy":"kept"}}`), info, true)
	var root map[string]any
	if err := json.Unmarshal(result, &root); err != nil {
		t.Fatal(err)
	}
	data := root["data"].(map[string]any)
	if data["senderPhoneNumber"] != "15550004" || data["recipientPhoneNumber"] != "15550005" || data["legacy"] != "kept" {
		t.Fatalf("data=%#v", data)
	}
}

func TestEnrichCurrentPhoneMetadataDropsUnverifiedPhoneFields(t *testing.T) {
	event := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{
		Sender: mustPhoneTestJID(t, "12345@lid"),
	}}}
	result := enrichCurrentPhoneMetadata([]byte(`{"event":"Message","data":{"senderPhoneNumber":"15559999","recipientPhoneNumber":"15558888","legacy":"kept"}}`), event, true)
	var root map[string]any
	if err := json.Unmarshal(result, &root); err != nil {
		t.Fatal(err)
	}
	data := root["data"].(map[string]any)
	if _, exists := data["senderPhoneNumber"]; exists {
		t.Fatalf("unverified sender phone survived: %#v", data)
	}
	if _, exists := data["recipientPhoneNumber"]; exists {
		t.Fatalf("unverified recipient phone survived: %#v", data)
	}
	if data["legacy"] != "kept" {
		t.Fatalf("legacy payload changed: %#v", data)
	}
}

func TestRealtimeAndGlobalNATSTransportsApplyPhoneKillSwitch(t *testing.T) {
	nats, websocket := &phoneCaptureProducer{}, &phoneCaptureProducer{}
	service := &whatsmeowService{config: &config.Config{PhoneNumberExposureEnabled: false, NatsGlobalEvents: []string{"MESSAGE"}}, natsProducer: nats, websocketProducer: websocket}
	instance := &instance_model.Instance{Id: "instance-a", Events: "ALL", NatsEnable: "enabled", WebSocketEnable: "enabled"}
	payload := []byte(`{"event":"Message","data":{"senderPhoneNumber":"15550001","legacy":"kept"}}`)
	service.sendToRealtimeTransports(instance, "Message", "message", payload)
	service.sendToGlobalNATS("Message", payload, instance.Id)
	if len(nats.payloads) != 2 || len(websocket.payloads) != 1 {
		t.Fatalf("nats=%d websocket=%d", len(nats.payloads), len(websocket.payloads))
	}
	for _, delivered := range append(nats.payloads, websocket.payloads...) {
		var root map[string]any
		if err := json.Unmarshal(delivered, &root); err != nil {
			t.Fatal(err)
		}
		data := root["data"].(map[string]any)
		if _, exists := data["senderPhoneNumber"]; exists || data["legacy"] != "kept" {
			t.Fatalf("payload=%s", delivered)
		}
	}
}
