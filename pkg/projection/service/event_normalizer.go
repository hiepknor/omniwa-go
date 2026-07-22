package projection_service

import projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"

func NormalizeProjectionEvent(instanceID string, rawEvent any) (*projection_model.Event, bool, error) {
	if event, relevant, err := NormalizeGroupEvent(instanceID, rawEvent); relevant || err != nil {
		return event, relevant, err
	}
	if event, relevant, err := NormalizeLabelEvent(instanceID, rawEvent); relevant || err != nil {
		return event, relevant, err
	}
	return NormalizeContactEvent(instanceID, rawEvent)
}
