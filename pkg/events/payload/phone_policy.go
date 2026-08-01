package payload

import (
	"encoding/json"
	"errors"
)

var phonePayloadFields = [...]string{
	"phoneNumber", "senderPhoneNumber", "recipientPhoneNumber", "participantPhoneNumber",
}

// PhonePayloadPolicy applies the startup kill switch at every transport boundary.
// Invalid payloads fail closed so uninspected personal data is never delivered.
type PhonePayloadObserver interface {
	ObservePhonePayloadPolicy(string)
}

type PhonePayloadPolicy struct {
	enabled  bool
	observer PhonePayloadObserver
}

func NewPhonePayloadPolicy(enabled bool, observers ...PhonePayloadObserver) PhonePayloadPolicy {
	var observer PhonePayloadObserver
	if len(observers) > 0 {
		observer = observers[0]
	}
	return PhonePayloadPolicy{enabled: enabled, observer: observer}
}

func (p PhonePayloadPolicy) Apply(payload []byte) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil || root == nil {
		if p.observer != nil {
			p.observer.ObservePhonePayloadPolicy("failed")
		}
		return nil, errors.New("external event payload is invalid")
	}
	data, ok := root["data"].(map[string]any)
	if !ok {
		if p.enabled {
			if p.observer != nil {
				p.observer.ObservePhonePayloadPolicy("preserved")
			}
			return payload, nil
		}
		return json.Marshal(root)
	}
	if !p.enabled {
		for _, field := range phonePayloadFields {
			delete(data, field)
		}
	}
	if p.observer != nil {
		if p.enabled {
			p.observer.ObservePhonePayloadPolicy("preserved")
		} else {
			p.observer.ObservePhonePayloadPolicy("redacted")
		}
	}
	return json.Marshal(root)
}
