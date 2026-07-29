package projection_service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types"
)

const (
	ContactIdentityBackfillVersion = 1
	contactIdentityBackfillLease   = 2 * time.Minute
)

type ContactLIDResolver interface {
	GetPNForLID(context.Context, types.JID) (types.JID, error)
	GetLIDForPN(context.Context, types.JID) (types.JID, error)
}

type ContactIdentityBackfillResult struct {
	Batches   int
	Scanned   int64
	Mapped    int64
	Merged    int64
	Unchanged int64
	Complete  bool
	LeaseHeld bool
}

type ContactIdentityReconciler struct {
	backfill projection_repository.ContactIdentityBackfillRepository
	contacts contactProjectionWriter
	now      func() time.Time
}

func NewContactIdentityReconciler(
	backfill projection_repository.ContactIdentityBackfillRepository,
	contacts contactProjectionWriter,
) *ContactIdentityReconciler {
	return &ContactIdentityReconciler{backfill: backfill, contacts: contacts, now: time.Now}
}

func (r *ContactIdentityReconciler) RunBounded(
	ctx context.Context,
	instanceID string,
	resolver ContactLIDResolver,
	batchSize int,
	maxBatches int,
) (ContactIdentityBackfillResult, error) {
	if r == nil || r.backfill == nil || r.contacts == nil || r.now == nil || ctx == nil || instanceID == "" || resolver == nil || batchSize < 1 || maxBatches < 1 {
		return ContactIdentityBackfillResult{}, errors.New("contact identity reconciliation dependencies and bounds are required")
	}
	owner := uuid.NewString()
	result := ContactIdentityBackfillResult{}
	for result.Batches < maxBatches {
		now := r.now().UTC()
		batch, err := r.backfill.ClaimBatch(ctx, instanceID, ContactIdentityBackfillVersion, owner, batchSize, now, now.Add(contactIdentityBackfillLease))
		if errors.Is(err, projection_repository.ErrContactIdentityBackfillLeaseHeld) {
			result.LeaseHeld = true
			return result, nil
		}
		if err != nil {
			return result, err
		}
		if batch.AlreadyComplete {
			result.Complete = true
			return result, nil
		}
		result.Batches++
		counts := projection_repository.ContactIdentityBackfillCounts{}
		var cursor *string
		for _, candidate := range batch.Items {
			counts.Scanned++
			identities, mapped, resolveErr := resolveContactLIDAliases(ctx, resolver, candidate.PreferredJID, candidate.PhoneJID, candidate.LID)
			if resolveErr != nil {
				_ = r.backfill.FailBatch(ctx, instanceID, ContactIdentityBackfillVersion, owner, "mapping_store_unavailable", r.now().UTC())
				return result, fmt.Errorf("resolve local contact identity mapping: %w", resolveErr)
			}
			if !mapped {
				counts.Unchanged++
				value := candidate.ContactID
				cursor = &value
				continue
			}
			counts.Mapped++
			patch := projection_repository.ContactPatch{
				InstanceID: instanceID, Identities: identities, Aspect: projection_repository.ContactAspectIdentity,
				OccurredAt: time.Unix(0, 0).UTC(), EventKey: contactIdentityMappingEventKey(identities),
			}
			stored, applied, applyErr := r.contacts.Apply(ctx, patch)
			if applyErr != nil {
				_ = r.backfill.FailBatch(ctx, instanceID, ContactIdentityBackfillVersion, owner, "projection_write_failed", r.now().UTC())
				return result, fmt.Errorf("apply local contact identity mapping: %w", applyErr)
			}
			if stored == nil {
				_ = r.backfill.FailBatch(ctx, instanceID, ContactIdentityBackfillVersion, owner, "projection_write_failed", r.now().UTC())
				return result, errors.New("apply local contact identity mapping returned no contact")
			}
			if stored.ContactID != candidate.ContactID {
				counts.Merged++
			} else if !applied {
				counts.Unchanged++
			}
			value := candidate.ContactID
			cursor = &value
		}
		if batch.Complete {
			if _, err := r.backfill.Validate(ctx, instanceID); err != nil {
				_ = r.backfill.FailBatch(ctx, instanceID, ContactIdentityBackfillVersion, owner, "identity_validation_failed", r.now().UTC())
				return result, fmt.Errorf("validate canonical contact identity graph: %w", err)
			}
		}
		if err := r.backfill.CommitBatch(ctx, instanceID, ContactIdentityBackfillVersion, owner, cursor, counts, batch.Complete, r.now().UTC()); err != nil {
			return result, err
		}
		result.Scanned += counts.Scanned
		result.Mapped += counts.Mapped
		result.Merged += counts.Merged
		result.Unchanged += counts.Unchanged
		if batch.Complete {
			result.Complete = true
			return result, nil
		}
	}
	return result, nil
}

// RefreshBounded reopens a completed verification pass before scanning. This
// is used only after Whatsmeow has persisted new authoritative PN/LID mappings.
// A pending or running pass is left intact and remains protected by its lease.
func (r *ContactIdentityReconciler) RefreshBounded(
	ctx context.Context,
	instanceID string,
	resolver ContactLIDResolver,
	batchSize int,
	maxBatches int,
) (ContactIdentityBackfillResult, error) {
	if r == nil || r.backfill == nil || r.now == nil || ctx == nil || instanceID == "" {
		return ContactIdentityBackfillResult{}, errors.New("contact identity refresh dependencies are required")
	}
	if _, err := r.backfill.RestartCompleted(ctx, instanceID, ContactIdentityBackfillVersion, r.now().UTC()); err != nil {
		return ContactIdentityBackfillResult{}, fmt.Errorf("restart contact identity verification: %w", err)
	}
	return r.RunBounded(ctx, instanceID, resolver, batchSize, maxBatches)
}

// EnrichContactEventWithLIDMapping adds authoritative aliases from the local
// whatsmeow mapping store. It never performs a provider network request.
func EnrichContactEventWithLIDMapping(ctx context.Context, event *projection_model.Event, resolver ContactLIDResolver) (*projection_model.Event, error) {
	if event == nil || event.Resource != contactResource || event.EventType == "contact_sync_complete" {
		return event, nil
	}
	var payload contactEventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return nil, err
	}
	identities, mapped, err := resolveContactLIDAliases(ctx, resolver, payload.PreferredJID, payload.PhoneJID, payload.LID)
	if err != nil || !mapped {
		return event, err
	}
	for _, identity := range identities {
		payload.Identities = append(payload.Identities, contactEventIdentity{Kind: identity.Kind, Value: identity.Value})
		switch identity.Kind {
		case projection_model.ContactIdentityKindPhoneJID:
			value := identity.Value
			payload.PhoneJID, payload.PreferredJID = &value, value
		case projection_model.ContactIdentityKindLID:
			value := identity.Value
			payload.LID = &value
		}
	}
	payload.Identities = deduplicateContactEventIdentities(payload.Identities)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	copyOfEvent := *event
	copyOfEvent.EntityKey = payload.PreferredJID
	copyOfEvent.Payload = encoded
	copyOfEvent.EventKey = contactEventKey(event.EventType, payload.PreferredJID, event.OccurredAt, encoded)
	return &copyOfEvent, nil
}

func resolveContactLIDAliases(
	ctx context.Context,
	resolver ContactLIDResolver,
	preferredJID string,
	phoneValue *string,
	lidValue *string,
) ([]projection_repository.ContactIdentityRef, bool, error) {
	if resolver == nil {
		return nil, false, errors.New("local LID resolver is required")
	}
	var phone, lid types.JID
	for _, candidate := range []string{contactOptionalString(phoneValue), contactOptionalString(lidValue), preferredJID} {
		if candidate == "" {
			continue
		}
		jid, err := types.ParseJID(candidate)
		if err != nil || jid.IsEmpty() {
			return nil, false, errors.New("projected contact contains an invalid JID")
		}
		jid = jid.ToNonAD()
		switch jid.Server {
		case types.DefaultUserServer:
			phone = jid
		case types.HiddenUserServer:
			lid = jid
		}
	}
	var err error
	if phone.IsEmpty() && !lid.IsEmpty() {
		phone, err = resolver.GetPNForLID(ctx, lid)
	} else if lid.IsEmpty() && !phone.IsEmpty() {
		lid, err = resolver.GetLIDForPN(ctx, phone)
	}
	if err != nil {
		return nil, false, err
	}
	if phone.IsEmpty() || lid.IsEmpty() {
		return nil, false, nil
	}
	phone, lid = phone.ToNonAD(), lid.ToNonAD()
	if phone.Server != types.DefaultUserServer || lid.Server != types.HiddenUserServer {
		return nil, false, errors.New("local LID mapping returned an invalid alias pair")
	}
	identities := []projection_repository.ContactIdentityRef{
		{Kind: projection_model.ContactIdentityKindJID, Value: phone.String()},
		{Kind: projection_model.ContactIdentityKindPhoneJID, Value: phone.String()},
		{Kind: projection_model.ContactIdentityKindJID, Value: lid.String()},
		{Kind: projection_model.ContactIdentityKindLID, Value: lid.String()},
	}
	return identities, true, nil
}

func contactIdentityMappingEventKey(identities []projection_repository.ContactIdentityRef) string {
	encoded, _ := json.Marshal(identities)
	sum := sha256.Sum256(encoded)
	return "lid-map:" + hex.EncodeToString(sum[:])
}

func contactOptionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
