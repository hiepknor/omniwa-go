package send_service

import (
	"errors"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestConfirmedOutboundPhoneMetadataRequiresPNToLIDAcknowledgement(t *testing.T) {
	tests := []struct {
		name          string
		requested     types.JID
		validationErr error
		acknowledged  types.JID
		want          bool
	}{
		{
			name: "provider resolved PN to LID", requested: types.NewJID("15550001", types.DefaultUserServer),
			acknowledged: types.NewJID("12345", types.HiddenUserServer), want: true,
		},
		{
			name: "device PN is normalized", requested: types.JID{User: "15550002", Device: 7, Server: types.DefaultUserServer},
			acknowledged: types.NewJID("12346", types.HostedLIDServer), want: true,
		},
		{
			name: "validation failed", requested: types.NewJID("15550003", types.DefaultUserServer),
			validationErr: errors.New("invalid"), acknowledged: types.NewJID("12347", types.HiddenUserServer),
		},
		{
			name: "acknowledgement remained PN", requested: types.NewJID("15550004", types.DefaultUserServer),
			acknowledged: types.NewJID("15550004", types.DefaultUserServer),
		},
		{
			name: "requested target was already LID", requested: types.NewJID("12348", types.HiddenUserServer),
			acknowledged: types.NewJID("12348", types.HiddenUserServer),
		},
		{
			name: "requested PN is not digits only", requested: types.NewJID("1555abc", types.DefaultUserServer),
			acknowledged: types.NewJID("12349", types.HiddenUserServer),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadata := confirmedOutboundPhoneMetadata(test.requested, test.validationErr, test.acknowledged)
			if got := len(metadata) == 1; got != test.want {
				t.Fatalf("metadata present=%v want=%v", got, test.want)
			}
			if test.want && metadata[0].Recipient.ToNonAD() != test.requested.ToNonAD() {
				t.Fatal("confirmed recipient was not preserved")
			}
		})
	}
}
