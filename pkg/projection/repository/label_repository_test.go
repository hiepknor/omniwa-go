package projection_repository

import (
	"strings"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
)

func TestValidateLabelProjectionRecords(t *testing.T) {
	now := time.Now()
	if err := validateLabel(&projection_model.Label{InstanceID: "instance-a", LabelID: "label-a", SourceOccurredAt: now, SourceEventKey: "event-a"}); err != nil {
		t.Fatalf("valid label rejected: %v", err)
	}
	if err := validateChatAssociation(&projection_model.LabelChatAssociation{InstanceID: "instance-a", LabelID: "label-a", ChatID: "chat-a", SourceOccurredAt: now, SourceEventKey: "event-a"}); err != nil {
		t.Fatalf("valid chat association rejected: %v", err)
	}
	if err := validateMessageAssociation(&projection_model.LabelMessageAssociation{InstanceID: "instance-a", LabelID: "label-a", ChatID: "chat-a", MessageID: "message-a", SourceOccurredAt: now, SourceEventKey: "event-a"}); err != nil {
		t.Fatalf("valid message association rejected: %v", err)
	}

	if err := validateLabel(nil); err == nil {
		t.Fatal("nil label accepted")
	}
	if err := validateChatAssociation(&projection_model.LabelChatAssociation{}); err == nil {
		t.Fatal("empty chat association accepted")
	}
	if err := validateMessageAssociation(&projection_model.LabelMessageAssociation{}); err == nil {
		t.Fatal("empty message association accepted")
	}
	tooLong := strings.Repeat("x", 256)
	if err := validateLabel(&projection_model.Label{InstanceID: "instance-a", LabelID: tooLong, SourceOccurredAt: now, SourceEventKey: "event-a"}); err == nil {
		t.Fatal("oversized label identity accepted")
	}
}
