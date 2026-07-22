package projection_service

import (
	"context"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
)

type captureLabelWrites struct {
	mutation           *projection_repository.LabelMutation
	chatAssociation    *projection_model.LabelChatAssociation
	messageAssociation *projection_model.LabelMessageAssociation
}

func (c *captureLabelWrites) ApplyLabelMutation(_ context.Context, mutation projection_repository.LabelMutation) (bool, error) {
	c.mutation = &mutation
	return true, nil
}

func (c *captureLabelWrites) ApplyChatAssociation(_ context.Context, association *projection_model.LabelChatAssociation) (bool, error) {
	c.chatAssociation = association
	return true, nil
}

func (c *captureLabelWrites) ApplyMessageAssociation(_ context.Context, association *projection_model.LabelMessageAssociation) (bool, error) {
	c.messageAssociation = association
	return true, nil
}

type captureLabelWriteState struct {
	recordedAt time.Time
	stale      bool
}

func (c *captureLabelWriteState) RecordEvent(_ string, _ string, _ int64, occurredAt time.Time) error {
	c.recordedAt = occurredAt
	return nil
}

func (c *captureLabelWriteState) MarkStale(string, string, int64) error {
	c.stale = true
	return nil
}

func TestLabelWriterWritesDefinitionAndAssociationTombstones(t *testing.T) {
	writes := &captureLabelWrites{}
	state := &captureLabelWriteState{}
	now := time.Unix(500, 0)
	writer := NewLabelWriter(writes, state)
	writer.now = func() time.Time { return now }

	if err := writer.WriteLabel(context.Background(), "instance-a", "label-1", "Priority", 4, false); err != nil {
		t.Fatal(err)
	}
	if writes.mutation == nil || writes.mutation.Name != "Priority" || writes.mutation.Color != 4 || writes.mutation.EventKey == "" || !writes.mutation.OccurredAt.Equal(now) {
		t.Fatalf("label mutation = %#v", writes.mutation)
	}
	if err := writer.WriteChatAssociation(context.Background(), "instance-a", "label-1", "chat@s.whatsapp.net", false); err != nil {
		t.Fatal(err)
	}
	if writes.chatAssociation == nil || writes.chatAssociation.TombstonedAt == nil || !writes.chatAssociation.TombstonedAt.Equal(now) {
		t.Fatalf("chat association = %#v", writes.chatAssociation)
	}
	if state.recordedAt.IsZero() {
		t.Fatal("write-through did not record projection state")
	}
}

func TestLabelWriterMarksProjectionStale(t *testing.T) {
	state := &captureLabelWriteState{}
	writer := NewLabelWriter(&captureLabelWrites{}, state)
	if err := writer.MarkStale("instance-a"); err != nil || !state.stale {
		t.Fatalf("MarkStale() = stale %v, error %v", state.stale, err)
	}
}
