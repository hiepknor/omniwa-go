package projection_service

import (
	"context"
	"errors"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"gorm.io/gorm"
)

var ErrContactsProjectionNotReady = errors.New("contacts projection is not ready")

type contactReadRepository interface {
	List(context.Context, string) ([]projection_model.Contact, error)
	GetByIdentity(context.Context, string, projection_model.ContactIdentityKind, string) (*projection_model.Contact, error)
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

func (r *ContactReader) readMeta(instanceID string) (*ProjectionReadMeta, error) {
	if r == nil || r.contacts == nil || r.state == nil || instanceID == "" {
		return nil, errors.New("contact projection reader dependencies and instance identity are required")
	}
	state, err := r.state.Get(instanceID, contactResource)
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
