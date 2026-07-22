package projection_service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrContactsProjectionNotReady = errors.New("contacts projection is not ready")
	ErrInvalidContactCursor       = errors.New("invalid contact search cursor")
	ErrInvalidContactSearch       = errors.New("invalid contact search query")
)

const (
	contactCursorVersion  = 1
	maxContactCursorKey   = 255
	maxContactSearchTerm  = 128
	maxContactSearchLimit = 200
)

type contactReadRepository interface {
	List(context.Context, string) ([]projection_model.Contact, error)
	GetByIdentity(context.Context, string, projection_model.ContactIdentityKind, string) (*projection_model.Contact, error)
	Search(context.Context, string, string, int, *projection_repository.ContactCursor) (*projection_repository.ContactPage, error)
}

type contactCursorEnvelope struct {
	Version   int    `json:"v"`
	Kind      string `json:"kind"`
	Scope     string `json:"scope"`
	SortKey   string `json:"sortKey"`
	ContactID string `json:"contactId"`
}

func (r *ContactReader) GetByJID(ctx context.Context, instanceID, jid string) (*projection_model.Contact, *ProjectionReadMeta, error) {
	if jid == "" {
		return nil, nil, errors.New("contact JID is required")
	}
	meta, err := r.readMeta(instanceID)
	if err != nil {
		return nil, nil, err
	}
	contact, err := r.contacts.GetByIdentity(ctx, instanceID, projection_model.ContactIdentityKindJID, jid)
	return contact, meta, err
}

type ContactReader struct {
	contacts contactReadRepository
	state    groupReadState
}

func NewContactReader(contacts contactReadRepository, state groupReadState) *ContactReader {
	return &ContactReader{contacts: contacts, state: state}
}

func (r *ContactReader) List(ctx context.Context, instanceID string) ([]projection_model.Contact, *ProjectionReadMeta, error) {
	meta, err := r.readMeta(instanceID)
	if err != nil {
		return nil, nil, err
	}
	contacts, err := r.contacts.List(ctx, instanceID)
	if contacts == nil && err == nil {
		contacts = make([]projection_model.Contact, 0)
	}
	return contacts, meta, err
}

func (r *ContactReader) Search(ctx context.Context, instanceID, term string, limit int, encodedCursor string) ([]projection_model.Contact, *ProjectionReadMeta, error) {
	term = strings.ToLower(strings.TrimSpace(term))
	if len(term) > maxContactSearchTerm || limit < 1 || limit > maxContactSearchLimit {
		return nil, nil, ErrInvalidContactSearch
	}
	meta, err := r.readMeta(instanceID)
	if err != nil {
		return nil, nil, err
	}
	scope := contactCursorScope(instanceID, term)
	cursor, err := decodeContactCursor(encodedCursor, scope)
	if err != nil {
		return nil, nil, err
	}
	page, err := r.contacts.Search(ctx, instanceID, term, limit, cursor)
	if err != nil {
		return nil, nil, err
	}
	if page == nil {
		return nil, nil, errors.New("contact search repository returned no page")
	}
	if page.Items == nil {
		page.Items = make([]projection_model.Contact, 0)
	}
	if page.NextCursor != nil {
		meta.NextCursor, err = encodeContactCursor(page.NextCursor, scope)
		if err != nil {
			return nil, nil, err
		}
	}
	return page.Items, meta, nil
}

func contactCursorScope(instanceID, term string) string {
	sum := sha256.Sum256([]byte(instanceID + "\x00" + term))
	return hex.EncodeToString(sum[:])
}

func decodeContactCursor(value, scope string) (*projection_repository.ContactCursor, error) {
	if value == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, ErrInvalidContactCursor
	}
	var envelope contactCursorEnvelope
	if err := json.Unmarshal(decoded, &envelope); err != nil || envelope.Version != contactCursorVersion || envelope.Kind != "contacts" ||
		envelope.Scope != scope || envelope.SortKey == "" || len(envelope.SortKey) > maxContactCursorKey || uuid.Validate(envelope.ContactID) != nil {
		return nil, ErrInvalidContactCursor
	}
	return &projection_repository.ContactCursor{SortKey: envelope.SortKey, ContactID: envelope.ContactID}, nil
}

func encodeContactCursor(cursor *projection_repository.ContactCursor, scope string) (string, error) {
	if cursor == nil || cursor.SortKey == "" || len(cursor.SortKey) > maxContactCursorKey || uuid.Validate(cursor.ContactID) != nil || scope == "" {
		return "", ErrInvalidContactCursor
	}
	payload, err := json.Marshal(contactCursorEnvelope{
		Version: contactCursorVersion, Kind: "contacts", Scope: scope, SortKey: cursor.SortKey, ContactID: cursor.ContactID,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func (r *ContactReader) readMeta(instanceID string) (*ProjectionReadMeta, error) {
	if r == nil || r.contacts == nil || r.state == nil || instanceID == "" {
		return nil, errors.New("contact projection reader dependencies and instance identity are required")
	}
	state, err := r.state.GetServingState(instanceID, contactResource)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrContactsProjectionNotReady
	}
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, ErrContactsProjectionNotReady
	}
	usableStatus := state.SyncStatus == projection_model.SyncStatusReady || state.SyncStatus == projection_model.SyncStatusStale || state.SyncStatus == projection_model.SyncStatusSyncing
	if !usableStatus || state.LastReconciledAt == nil || state.SchemaVersion < ContactsProjectionSchemaVersion {
		return nil, ErrContactsProjectionNotReady
	}
	lastSyncedAt := state.LastReconciledAt.UTC()
	return &ProjectionReadMeta{Source: "projection", SyncStatus: state.SyncStatus, LastSyncedAt: &lastSyncedAt}, nil
}
