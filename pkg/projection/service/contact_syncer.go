package projection_service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types"
	"gorm.io/gorm"
)

type ContactSnapshotFetcher func(context.Context) (map[types.JID]types.ContactInfo, error)

type contactSyncState interface {
	Get(instanceID, resource string) (*projection_model.State, error)
	MarkSyncing(instanceID, resource string, schemaVersion int64) error
	MarkStale(instanceID, resource string, schemaVersion int64) error
	MarkFailed(instanceID, resource string, schemaVersion int64) error
}

type contactSyncEvents interface {
	Ingest(context.Context, *projection_model.Event) (bool, error)
}

type ContactSyncer struct {
	contacts contactProjectionWriter
	state    contactSyncState
	events   contactSyncEvents
	now      func() time.Time
	locks    sync.Map
}

func NewContactSyncer(contacts contactProjectionWriter, state contactSyncState, events contactSyncEvents) *ContactSyncer {
	return &ContactSyncer{contacts: contacts, state: state, events: events, now: time.Now}
}

func (s *ContactSyncer) Sync(ctx context.Context, instanceID string, fetch ContactSnapshotFetcher) error {
	if s == nil || s.contacts == nil || s.state == nil || s.events == nil || s.now == nil || instanceID == "" || fetch == nil {
		return errors.New("contact sync dependencies and instance identity are required")
	}
	lockValue, _ := s.locks.LoadOrStore(instanceID, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	state, err := s.state.Get(instanceID, contactResource)
	if err == nil && state != nil && state.SyncStatus == projection_model.SyncStatusReady && state.SchemaVersion >= ContactsProjectionSchemaVersion {
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err := s.state.MarkSyncing(instanceID, contactResource, ContactsProjectionSchemaVersion); err != nil {
		return err
	}
	snapshotAt := s.now().UTC()
	contacts, err := fetch(ctx)
	if err != nil {
		return s.fail(instanceID, state, err)
	}
	snapshotID := uuid.NewString()
	type snapshotEntry struct {
		jid    types.JID
		info   types.ContactInfo
		source string
	}
	entryByJID := make(map[string]snapshotEntry, len(contacts))
	for jid, info := range contacts {
		if isContactJID(jid) {
			normalized := jid.ToNonAD()
			key := normalized.String()
			candidate := snapshotEntry{jid: normalized, info: info, source: jid.String()}
			if current, exists := entryByJID[key]; !exists || candidate.source < current.source {
				entryByJID[key] = candidate
			}
		}
	}
	entries := make([]snapshotEntry, 0, len(entryByJID))
	for _, entry := range entryByJID {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].jid.String() < entries[j].jid.String() })
	for _, entry := range entries {
		if err := s.applySnapshotContact(ctx, instanceID, snapshotID, snapshotAt, entry.jid, entry.info); err != nil {
			return s.fail(instanceID, state, err)
		}
	}
	completedAt := s.now().UTC()
	payload, err := json.Marshal(contactEventPayload{PreferredJID: "contacts-sync", CompletedAt: &completedAt})
	if err != nil {
		return s.fail(instanceID, state, err)
	}
	event := &projection_model.Event{
		InstanceID: instanceID, Resource: contactResource, EventKey: "contact-sync:" + snapshotID,
		EntityKey: "contacts-sync", EventType: "contact_sync_complete",
		OccurredAt: time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), Payload: payload,
	}
	if _, err := s.events.Ingest(ctx, event); err != nil {
		return s.fail(instanceID, state, err)
	}
	return nil
}

func (s *ContactSyncer) applySnapshotContact(ctx context.Context, instanceID, snapshotID string, snapshotAt time.Time, jid types.JID, info types.ContactInfo) error {
	payload := newContactPayload(jid)
	identities := make([]projection_repository.ContactIdentityRef, 0, len(payload.Identities))
	for _, identity := range payload.Identities {
		identities = append(identities, projection_repository.ContactIdentityRef{Kind: identity.Kind, Value: identity.Value})
	}
	firstName, fullName, redactedPhone := info.FirstName, info.FullName, info.RedactedPhone
	found := info.Found
	base := projection_repository.ContactPatch{
		InstanceID: instanceID, Identities: identities, OccurredAt: snapshotAt, PreferredJID: payload.PreferredJID,
		PhoneJID: payload.PhoneJID, LID: payload.LID,
	}
	details := base
	details.Aspect, details.EventKey = projection_repository.ContactAspectDetails, contactSnapshotEventKey(snapshotID, "details", payload.PreferredJID)
	details.Found, details.FirstName, details.FullName, details.RedactedPhone = &found, &firstName, &fullName, &redactedPhone
	if _, _, err := s.contacts.Apply(ctx, details); err != nil {
		return err
	}
	pushName := info.PushName
	push := base
	push.Aspect, push.EventKey, push.PushName = projection_repository.ContactAspectPushName, contactSnapshotEventKey(snapshotID, "push", payload.PreferredJID), &pushName
	if _, _, err := s.contacts.Apply(ctx, push); err != nil {
		return err
	}
	businessName := info.BusinessName
	business := base
	business.Aspect, business.EventKey, business.BusinessName = projection_repository.ContactAspectBusinessName, contactSnapshotEventKey(snapshotID, "business", payload.PreferredJID), &businessName
	_, _, err := s.contacts.Apply(ctx, business)
	return err
}

func contactSnapshotEventKey(snapshotID, aspect, preferredJID string) string {
	sum := sha256.Sum256([]byte(snapshotID + "\x00" + aspect + "\x00" + preferredJID))
	return "snapshot:" + hex.EncodeToString(sum[:])
}

func (s *ContactSyncer) fail(instanceID string, previous *projection_model.State, syncErr error) error {
	var stateErr error
	if previous != nil && previous.LastReconciledAt != nil {
		stateErr = s.state.MarkStale(instanceID, contactResource, ContactsProjectionSchemaVersion)
	} else {
		stateErr = s.state.MarkFailed(instanceID, contactResource, ContactsProjectionSchemaVersion)
	}
	return errors.Join(syncErr, stateErr)
}
