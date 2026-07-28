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
	"go.mau.fi/whatsmeow/types/events"
)

type contactLIDResolverFake struct {
	phone types.JID
	lid   types.JID
	err   error
}

func (f *contactLIDResolverFake) GetPNForLID(context.Context, types.JID) (types.JID, error) {
	return f.phone, f.err
}

func (f *contactLIDResolverFake) GetLIDForPN(context.Context, types.JID) (types.JID, error) {
	return f.lid, f.err
}

type contactIdentityBackfillRepositoryFake struct {
	batches []*projection_repository.ContactIdentityBackfillBatch
	commits []projection_repository.ContactIdentityBackfillCounts
	failed  string
}

func (f *contactIdentityBackfillRepositoryFake) ClaimBatch(context.Context, string, int, string, int, time.Time, time.Time) (*projection_repository.ContactIdentityBackfillBatch, error) {
	if len(f.batches) == 0 {
		return &projection_repository.ContactIdentityBackfillBatch{Complete: true, AlreadyComplete: true}, nil
	}
	batch := f.batches[0]
	f.batches = f.batches[1:]
	return batch, nil
}

func (f *contactIdentityBackfillRepositoryFake) CommitBatch(_ context.Context, _ string, _ int, _ string, _ *string, counts projection_repository.ContactIdentityBackfillCounts, _ bool, _ time.Time) error {
	f.commits = append(f.commits, counts)
	return nil
}

func (f *contactIdentityBackfillRepositoryFake) FailBatch(_ context.Context, _ string, _ int, _ string, code string, _ time.Time) error {
	f.failed = code
	return nil
}

func (f *contactIdentityBackfillRepositoryFake) GetState(context.Context, string) (*projection_model.ContactIdentityBackfill, error) {
	return nil, errors.New("unused")
}

func (f *contactIdentityBackfillRepositoryFake) Validate(context.Context, string) (projection_repository.ContactIdentityValidation, error) {
	return projection_repository.ContactIdentityValidation{}, nil
}

type contactIdentityWriterFake struct {
	patches []projection_repository.ContactPatch
}

func (f *contactIdentityWriterFake) Apply(_ context.Context, patch projection_repository.ContactPatch) (*projection_model.Contact, bool, error) {
	f.patches = append(f.patches, patch)
	return &projection_model.Contact{ContactID: "survivor"}, true, nil
}

func TestEnrichContactEventUsesLocalMappingInBothDirections(t *testing.T) {
	phone := types.NewJID("15550001", types.DefaultUserServer)
	lid := types.NewJID("9000001", types.HiddenUserServer)
	resolver := &contactLIDResolverFake{phone: phone, lid: lid}

	for _, primary := range []types.JID{phone, lid} {
		raw := eventsPushName(primary)
		event, relevant, err := NormalizeContactEvent("instance-a", &raw)
		if err != nil || !relevant {
			t.Fatalf("normalize %s = %#v, %v, %v", primary, event, relevant, err)
		}
		enriched, err := EnrichContactEventWithLIDMapping(context.Background(), event, resolver)
		if err != nil {
			t.Fatal(err)
		}
		var payload contactEventPayload
		if err = json.Unmarshal(enriched.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.PhoneJID == nil || *payload.PhoneJID != phone.String() || payload.LID == nil || *payload.LID != lid.String() ||
			payload.PreferredJID != phone.String() || len(payload.Identities) != 4 {
			t.Fatalf("enriched payload = %#v", payload)
		}
		replayed, err := EnrichContactEventWithLIDMapping(context.Background(), event, resolver)
		if err != nil || replayed.EventKey != enriched.EventKey {
			t.Fatalf("deterministic enrichment = %q/%q, %v", enriched.EventKey, replayed.EventKey, err)
		}
	}
}

func TestEnrichContactEventLeavesIncompleteMappingUnmerged(t *testing.T) {
	raw := eventsPushName(types.NewJID("15550001", types.DefaultUserServer))
	event, _, err := NormalizeContactEvent("instance-a", &raw)
	if err != nil {
		t.Fatal(err)
	}
	enriched, err := EnrichContactEventWithLIDMapping(context.Background(), event, &contactLIDResolverFake{})
	if err != nil || enriched != event {
		t.Fatalf("incomplete mapping changed event = %#v/%#v, %v", event, enriched, err)
	}
}

func TestContactIdentityReconcilerRunsBoundedAndRecordsMerge(t *testing.T) {
	phone := "15550001@s.whatsapp.net"
	repository := &contactIdentityBackfillRepositoryFake{batches: []*projection_repository.ContactIdentityBackfillBatch{{
		Items: []projection_repository.ContactIdentityCandidate{{ContactID: "absorbed", PreferredJID: phone, PhoneJID: &phone}}, Complete: true,
	}}}
	writer := &contactIdentityWriterFake{}
	reconciler := NewContactIdentityReconciler(repository, writer)
	reconciler.now = func() time.Time { return time.Unix(100, 0).UTC() }
	result, err := reconciler.RunBounded(context.Background(), "instance-a", &contactLIDResolverFake{
		phone: types.NewJID("15550001", types.DefaultUserServer), lid: types.NewJID("9000001", types.HiddenUserServer),
	}, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.Batches != 1 || result.Scanned != 1 || result.Mapped != 1 || result.Merged != 1 || len(repository.commits) != 1 {
		t.Fatalf("backfill result = %#v, commits=%#v", result, repository.commits)
	}
	if len(writer.patches) != 1 || writer.patches[0].Aspect != projection_repository.ContactAspectIdentity || len(writer.patches[0].Identities) != 4 {
		t.Fatalf("identity patch = %#v", writer.patches)
	}
}

func TestContactIdentityReconcilerRecordsSafeFailureAndRetriesLater(t *testing.T) {
	phone := "15550001@s.whatsapp.net"
	repository := &contactIdentityBackfillRepositoryFake{batches: []*projection_repository.ContactIdentityBackfillBatch{{
		Items: []projection_repository.ContactIdentityCandidate{{ContactID: "contact-a", PreferredJID: phone, PhoneJID: &phone}}, Complete: true,
	}}}
	reconciler := NewContactIdentityReconciler(repository, &contactIdentityWriterFake{})
	reconciler.now = func() time.Time { return time.Unix(100, 0).UTC() }
	_, err := reconciler.RunBounded(context.Background(), "instance-a", &contactLIDResolverFake{err: errors.New("database offline")}, 10, 1)
	if err == nil || repository.failed != "mapping_store_unavailable" || len(repository.commits) != 0 {
		t.Fatalf("mapping failure = %v, code=%q, commits=%#v", err, repository.failed, repository.commits)
	}
}

func eventsPushName(jid types.JID) events.PushName {
	return events.PushName{JID: jid, NewPushName: "Ada"}
}
