package projection_service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"go.mau.fi/whatsmeow/types"
)

type phoneEvidenceRepositoryStub struct {
	observed []projection_model.PhoneIdentityEvidence
	err      error
}

func (s *phoneEvidenceRepositoryStub) Observe(_ context.Context, evidence projection_model.PhoneIdentityEvidence) (bool, error) {
	s.observed = append(s.observed, evidence)
	return len(s.observed) == 1, s.err
}

func (s *phoneEvidenceRepositoryStub) Resolve(context.Context, string, []string) (map[string]string, error) {
	return nil, errors.New("not implemented")
}

func (s *phoneEvidenceRepositoryStub) List(context.Context, string) ([]projection_model.PhoneIdentityEvidence, error) {
	return nil, errors.New("not implemented")
}

type phoneEvidenceObserverStub struct{ outcomes []string }

func (s *phoneEvidenceObserverStub) ObservePhoneIdentityEvidence(outcome string) {
	s.outcomes = append(s.outcomes, outcome)
}

func TestPhoneIdentityEvidenceRecorderRequiresDirectInstanceEvidence(t *testing.T) {
	repository := &phoneEvidenceRepositoryStub{}
	recorder := NewPhoneIdentityEvidenceRecorder(repository, nil)
	at := time.Unix(100, 0)

	if err := recorder.ObserveJIDs(context.Background(), "instance-a", at,
		types.NewJID("900001", types.HiddenUserServer),
	); err != nil {
		t.Fatal(err)
	}
	if len(repository.observed) != 0 {
		t.Fatal("LID-only observation must not create phone evidence")
	}
	if err := recorder.ObserveJIDs(context.Background(), "instance-a", at,
		types.NewJID("900001", types.HiddenUserServer), types.NewJID("15550001", types.DefaultUserServer),
	); err != nil {
		t.Fatal(err)
	}
	if len(repository.observed) != 1 || repository.observed[0].LIDJID == nil ||
		*repository.observed[0].LIDJID != "900001@lid" || repository.observed[0].PhoneJID != "15550001@s.whatsapp.net" ||
		repository.observed[0].EvidenceKind != projection_model.PhoneIdentityEvidencePairedAlt {
		t.Fatalf("unexpected paired evidence: %#v", repository.observed)
	}
}

func TestPhoneIdentityEvidenceConflictIsObservedWithoutBlockingProjection(t *testing.T) {
	repository := &phoneEvidenceRepositoryStub{err: projection_repository.ErrPhoneIdentityEvidenceConflict}
	observer := &phoneEvidenceObserverStub{}
	recorder := NewPhoneIdentityEvidenceRecorder(repository, observer)
	if err := recorder.ObserveJIDs(context.Background(), "instance-a", time.Unix(100, 0),
		types.NewJID("15550001", types.DefaultUserServer), types.NewJID("900001", types.HiddenUserServer),
	); err != nil {
		t.Fatalf("conflict should be fail-closed without blocking projection: %v", err)
	}
	if len(observer.outcomes) != 1 || observer.outcomes[0] != "conflict" {
		t.Fatalf("outcomes = %v", observer.outcomes)
	}
}

func TestPhoneIdentityEvidenceMessageHandlerDoesNotCrossPairIncomingRecipient(t *testing.T) {
	repository := &phoneEvidenceRepositoryStub{}
	recorder := NewPhoneIdentityEvidenceRecorder(repository, nil)
	payload, err := json.Marshal(messageEventPayload{
		ChatID: "900001@lid", Direction: projection_model.MessageDirectionIncoming,
		SenderJID: stringPointer("900001@lid"), SenderAltJID: stringPointer("15550001@s.whatsapp.net"),
		RecipientAltJID: stringPointer("15559999@s.whatsapp.net"),
	})
	if err != nil {
		t.Fatal(err)
	}
	nextCalled := false
	handler := recorder.HandleMessage(func(context.Context, *projection_model.Event) error {
		nextCalled = true
		return nil
	})
	if err := handler(context.Background(), &projection_model.Event{
		InstanceID: "instance-a", Resource: messageResource, EventType: "message", EventKey: "event-a",
		OccurredAt: time.Unix(100, 0), Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	if !nextCalled {
		t.Fatal("wrapped projection handler was not called")
	}
	if len(repository.observed) != 2 {
		t.Fatalf("observed %d evidence rows, want paired sender and direct recipient", len(repository.observed))
	}
	if repository.observed[0].LIDJID == nil || *repository.observed[0].LIDJID != "900001@lid" {
		t.Fatalf("sender evidence = %#v", repository.observed[0])
	}
	if repository.observed[1].LIDJID != nil || repository.observed[1].PhoneJID != "15559999@s.whatsapp.net" {
		t.Fatalf("recipient evidence was cross-paired: %#v", repository.observed[1])
	}
}
