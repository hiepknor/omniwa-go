package payload

import "go.mau.fi/whatsmeow/types"

// ConfirmedPhoneMetadata carries phone identity evidence that was confirmed
// during the current provider operation but is absent from its acknowledgement.
// It is internal emission context and must never be serialized directly.
type ConfirmedPhoneMetadata struct {
	Recipient types.JID
}
