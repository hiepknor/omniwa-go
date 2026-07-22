package projection_repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ContactAspect string

const (
	ContactAspectDetails      ContactAspect = "contact"
	ContactAspectPushName     ContactAspect = "push_name"
	ContactAspectBusinessName ContactAspect = "business_name"
	ContactAspectPicture      ContactAspect = "picture"
	ContactAspectAbout        ContactAspect = "about"
)

var contactAspects = []ContactAspect{
	ContactAspectDetails, ContactAspectPushName, ContactAspectBusinessName, ContactAspectPicture, ContactAspectAbout,
}

type ContactIdentityRef struct {
	Kind  projection_model.ContactIdentityKind
	Value string
}

type ContactCursor struct {
	SortKey   string
	ContactID string
}

type ContactPage struct {
	Items      []projection_model.Contact
	NextCursor *ContactCursor
}

// ContactPatch updates one independently ordered field group. A non-nil field
// is authoritative for that aspect, including pointers to empty values.
type ContactPatch struct {
	InstanceID               string
	Identities               []ContactIdentityRef
	Aspect                   ContactAspect
	OccurredAt               time.Time
	EventKey                 string
	PreferredJID             string
	PhoneJID                 *string
	LID                      *string
	Username                 *string
	Found                    *bool
	FirstName                *string
	FullName                 *string
	PushName                 *string
	BusinessName             *string
	RedactedPhone            *string
	SaveOnPrimaryAddressbook *bool
	PictureID                *string
	PictureAuthorJID         *string
	PictureRemoved           *bool
	PictureUpdatedAt         *time.Time
	About                    *string
	AboutUpdatedAt           *time.Time
	Deleted                  *bool
}

type ContactRepository interface {
	Apply(context.Context, ContactPatch) (*projection_model.Contact, bool, error)
	Get(context.Context, string, string) (*projection_model.Contact, error)
	GetByIdentity(context.Context, string, projection_model.ContactIdentityKind, string) (*projection_model.Contact, error)
	List(context.Context, string) ([]projection_model.Contact, error)
	Search(context.Context, string, string, int, *ContactCursor) (*ContactPage, error)
}

type contactRepository struct {
	db  *gorm.DB
	now func() time.Time
}

type contactFieldVersion struct {
	OccurredAt time.Time `json:"occurredAt"`
	EventKey   string    `json:"eventKey"`
}

type contactFieldVersions map[ContactAspect]contactFieldVersion

func NewContactRepository(db *gorm.DB) ContactRepository {
	return &contactRepository{db: db, now: time.Now}
}

func (r *contactRepository) Apply(ctx context.Context, patch ContactPatch) (*projection_model.Contact, bool, error) {
	identities, err := validateContactPatch(patch)
	if err != nil {
		return nil, false, err
	}
	patch.Identities = identities
	patch.OccurredAt = patch.OccurredAt.UTC()
	now := r.now().UTC()
	var stored projection_model.Contact
	applied := false
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockContactAliases(tx, patch.InstanceID, identities); err != nil {
			return err
		}
		contacts, err := resolveContacts(tx, patch.InstanceID, identities)
		if err != nil {
			return err
		}
		created := len(contacts) == 0
		if created {
			stored = projection_model.Contact{
				InstanceID: patch.InstanceID, ContactID: uuid.NewString(), PreferredJID: preferredJID(patch),
				SourceOccurredAt: patch.OccurredAt, SourceEventKey: patch.EventKey,
				FieldVersions: json.RawMessage(`{}`), LastSyncedAt: now,
			}
		} else {
			stored, err = mergeContacts(tx, contacts)
			if err != nil {
				return err
			}
			applied = len(contacts) > 1
		}

		versions, err := decodeContactVersions(stored.FieldVersions)
		if err != nil {
			return err
		}
		incoming := contactFieldVersion{OccurredAt: patch.OccurredAt, EventKey: patch.EventKey}
		if current, exists := versions[patch.Aspect]; !exists || contactVersionLess(current, incoming) {
			applyContactAspect(&stored, patch)
			versions[patch.Aspect] = incoming
			applied = true
		}
		if contactVersionLess(contactFieldVersion{OccurredAt: stored.SourceOccurredAt, EventKey: stored.SourceEventKey}, incoming) {
			stored.SourceOccurredAt = patch.OccurredAt
			stored.SourceEventKey = patch.EventKey
		}
		stored.FieldVersions, err = json.Marshal(versions)
		if err != nil {
			return fmt.Errorf("encode contact field versions: %w", err)
		}
		stored.LastSyncedAt = now
		if created {
			if err := tx.Create(&stored).Error; err != nil {
				return err
			}
		} else if err := tx.Save(&stored).Error; err != nil {
			return err
		}
		aliasesChanged, err := upsertContactAliases(tx, &stored, identities, incoming, now)
		if err != nil {
			return err
		}
		applied = applied || created || aliasesChanged
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("apply contact projection: %w", err)
	}
	return &stored, applied, nil
}

func (r *contactRepository) Get(ctx context.Context, instanceID, contactID string) (*projection_model.Contact, error) {
	if instanceID == "" || contactID == "" {
		return nil, errors.New("contact projection identity is required")
	}
	var contact projection_model.Contact
	err := r.db.WithContext(ctx).Where("instance_id = ? AND contact_id = ? AND tombstoned_at IS NULL", instanceID, contactID).First(&contact).Error
	return &contact, err
}

func (r *contactRepository) GetByIdentity(ctx context.Context, instanceID string, kind projection_model.ContactIdentityKind, value string) (*projection_model.Contact, error) {
	if instanceID == "" || !validContactIdentityKind(kind) || value == "" {
		return nil, errors.New("contact projection alias is required")
	}
	var contact projection_model.Contact
	err := r.db.WithContext(ctx).Table("projected_contacts AS contacts").
		Select("contacts.*").
		Joins("JOIN projected_contact_identities AS identities ON identities.instance_id = contacts.instance_id AND identities.contact_id = contacts.contact_id").
		Where("identities.instance_id = ? AND identities.identity_kind = ? AND identities.identity_value = ?", instanceID, kind, value).
		Where("contacts.tombstoned_at IS NULL AND identities.tombstoned_at IS NULL").First(&contact).Error
	return &contact, err
}

func (r *contactRepository) List(ctx context.Context, instanceID string) ([]projection_model.Contact, error) {
	if instanceID == "" {
		return nil, errors.New("contact projection instance identity is required")
	}
	var contacts []projection_model.Contact
	err := r.db.WithContext(ctx).Where("instance_id = ? AND tombstoned_at IS NULL", instanceID).
		Order("COALESCE(NULLIF(full_name, ''), NULLIF(push_name, ''), preferred_jid) ASC, contact_id ASC").Find(&contacts).Error
	return contacts, err
}

const (
	maxContactSearchLimit = 200
	contactSearchSortSQL  = "LOWER(preferred_jid)"
)

type contactSearchRow struct {
	projection_model.Contact
	SearchSortKey string `gorm:"column:search_sort_key"`
}

func (r *contactRepository) Search(ctx context.Context, instanceID, term string, limit int, cursor *ContactCursor) (*ContactPage, error) {
	term = strings.TrimSpace(term)
	if r == nil || r.db == nil || ctx == nil || instanceID == "" || len(term) > 128 || limit < 1 || limit > maxContactSearchLimit ||
		(cursor != nil && (cursor.SortKey == "" || cursor.ContactID == "")) {
		return nil, errors.New("valid contact search parameters are required")
	}

	query := r.db.WithContext(ctx).Model(&projection_model.Contact{}).
		Select("projected_contacts.*, "+contactSearchSortSQL+" AS search_sort_key").
		Where("instance_id = ? AND tombstoned_at IS NULL", instanceID)
	if term != "" {
		pattern := escapeContactSearchPattern(strings.ToLower(term)) + "%"
		query = query.Where(`(LOWER(preferred_jid) LIKE ? OR
LOWER(COALESCE(first_name, '')) LIKE ? OR
LOWER(COALESCE(full_name, '')) LIKE ? OR
LOWER(COALESCE(push_name, '')) LIKE ? OR
LOWER(COALESCE(business_name, '')) LIKE ? OR
LOWER(COALESCE(username, '')) LIKE ? OR
LOWER(COALESCE(redacted_phone, '')) LIKE ?)`, pattern, pattern, pattern, pattern, pattern, pattern, pattern)
	}
	if cursor != nil {
		query = query.Where("("+contactSearchSortSQL+" > ? OR ("+contactSearchSortSQL+" = ? AND contact_id > ?))", cursor.SortKey, cursor.SortKey, cursor.ContactID)
	}

	var rows []contactSearchRow
	if err := query.Order(contactSearchSortSQL + " ASC, contact_id ASC").Limit(limit + 1).Scan(&rows).Error; err != nil {
		return nil, err
	}
	page := &ContactPage{Items: make([]projection_model.Contact, min(len(rows), limit))}
	for index := range page.Items {
		page.Items[index] = rows[index].Contact
	}
	if len(rows) > limit {
		last := rows[limit-1]
		page.NextCursor = &ContactCursor{SortKey: last.SearchSortKey, ContactID: last.ContactID}
	}
	return page, nil
}

func escapeContactSearchPattern(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

func validateContactPatch(patch ContactPatch) ([]ContactIdentityRef, error) {
	if patch.InstanceID == "" || patch.EventKey == "" || len(patch.EventKey) > 255 || patch.OccurredAt.IsZero() || !validContactAspect(patch.Aspect) {
		return nil, errors.New("contact projection identity, aspect, and source version are required")
	}
	seen := make(map[string]struct{}, len(patch.Identities))
	identities := make([]ContactIdentityRef, 0, len(patch.Identities))
	for _, identity := range patch.Identities {
		identity.Value = strings.TrimSpace(identity.Value)
		if !validContactIdentityKind(identity.Kind) || identity.Value == "" || len(identity.Value) > 255 {
			return nil, errors.New("contact projection alias is invalid")
		}
		key := string(identity.Kind) + "\x00" + identity.Value
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		identities = append(identities, identity)
	}
	preferred := preferredJID(patchWithIdentities(patch, identities))
	if len(identities) == 0 || preferred == "" || len(preferred) > 255 {
		return nil, errors.New("at least one contact alias and a valid preferred JID are required")
	}
	return identities, nil
}

func patchWithIdentities(patch ContactPatch, identities []ContactIdentityRef) ContactPatch {
	patch.Identities = identities
	return patch
}

func preferredJID(patch ContactPatch) string {
	if patch.PreferredJID != "" {
		return patch.PreferredJID
	}
	for _, identity := range patch.Identities {
		if identity.Kind == projection_model.ContactIdentityKindJID || identity.Kind == projection_model.ContactIdentityKindPhoneJID || identity.Kind == projection_model.ContactIdentityKindLID {
			return identity.Value
		}
	}
	return ""
}

func validContactAspect(aspect ContactAspect) bool {
	for _, candidate := range contactAspects {
		if candidate == aspect {
			return true
		}
	}
	return false
}

func validContactIdentityKind(kind projection_model.ContactIdentityKind) bool {
	switch kind {
	case projection_model.ContactIdentityKindJID, projection_model.ContactIdentityKindPhoneJID, projection_model.ContactIdentityKindLID, projection_model.ContactIdentityKindUsername:
		return true
	default:
		return false
	}
}

func lockContactAliases(tx *gorm.DB, instanceID string, identities []ContactIdentityRef) error {
	keys := make([]string, 0, len(identities))
	for _, identity := range identities {
		keys = append(keys, fmt.Sprintf("%d:%s%d:%s%d:%s", len(instanceID), instanceID, len(identity.Kind), identity.Kind, len(identity.Value), identity.Value))
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", key).Error; err != nil {
			return fmt.Errorf("lock contact alias: %w", err)
		}
	}
	return nil
}

func resolveContacts(tx *gorm.DB, instanceID string, identities []ContactIdentityRef) ([]projection_model.Contact, error) {
	condition, args := contactAliasCondition(identities)
	var aliases []projection_model.ContactIdentity
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id = ? AND ("+condition+")", append([]any{instanceID}, args...)...).Find(&aliases).Error; err != nil {
		return nil, err
	}
	contactIDs := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		contactIDs[alias.ContactID] = struct{}{}
	}
	values := make([]string, 0, len(identities))
	for _, identity := range identities {
		values = append(values, identity.Value)
	}
	var fallback []projection_model.Contact
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("instance_id = ? AND (preferred_jid IN ? OR phone_jid IN ? OR lid IN ? OR username IN ?)", instanceID, values, values, values, values).
		Find(&fallback).Error; err != nil {
		return nil, err
	}
	for _, contact := range fallback {
		contactIDs[contact.ContactID] = struct{}{}
	}
	if len(contactIDs) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(contactIDs))
	for id := range contactIDs {
		ids = append(ids, id)
	}
	var contacts []projection_model.Contact
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id = ? AND contact_id IN ?", instanceID, ids).Find(&contacts).Error; err != nil {
		return nil, err
	}
	return contacts, nil
}

func contactAliasCondition(identities []ContactIdentityRef) (string, []any) {
	parts := make([]string, 0, len(identities))
	args := make([]any, 0, len(identities)*2)
	for _, identity := range identities {
		parts = append(parts, "(identity_kind = ? AND identity_value = ?)")
		args = append(args, identity.Kind, identity.Value)
	}
	return strings.Join(parts, " OR "), args
}

func mergeContacts(tx *gorm.DB, contacts []projection_model.Contact) (projection_model.Contact, error) {
	sort.Slice(contacts, func(i, j int) bool { return contacts[i].ContactID < contacts[j].ContactID })
	target := contacts[0]
	targetVersions, err := decodeContactVersions(target.FieldVersions)
	if err != nil {
		return target, err
	}
	losingIDs := make([]string, 0, len(contacts)-1)
	for index := 1; index < len(contacts); index++ {
		candidate := contacts[index]
		candidateVersions, decodeErr := decodeContactVersions(candidate.FieldVersions)
		if decodeErr != nil {
			return target, decodeErr
		}
		for _, aspect := range contactAspects {
			candidateVersion := contactVersionFor(candidate, candidateVersions, aspect)
			if contactVersionLess(contactVersionFor(target, targetVersions, aspect), candidateVersion) {
				copyContactAspect(&target, &candidate, aspect)
				targetVersions[aspect] = candidateVersion
			}
		}
		candidateSource := contactFieldVersion{OccurredAt: candidate.SourceOccurredAt, EventKey: candidate.SourceEventKey}
		if contactVersionLess(contactFieldVersion{OccurredAt: target.SourceOccurredAt, EventKey: target.SourceEventKey}, candidateSource) {
			target.SourceOccurredAt, target.SourceEventKey = candidate.SourceOccurredAt, candidate.SourceEventKey
		}
		if candidate.LastSyncedAt.After(target.LastSyncedAt) {
			target.LastSyncedAt = candidate.LastSyncedAt
		}
		losingIDs = append(losingIDs, candidate.ContactID)
	}
	if len(losingIDs) > 0 {
		if err := tx.Model(&projection_model.ContactIdentity{}).Where("instance_id = ? AND contact_id IN ?", target.InstanceID, losingIDs).Update("contact_id", target.ContactID).Error; err != nil {
			return target, err
		}
		if err := tx.Where("instance_id = ? AND contact_id IN ?", target.InstanceID, losingIDs).Delete(&projection_model.Contact{}).Error; err != nil {
			return target, err
		}
	}
	target.FieldVersions, err = json.Marshal(targetVersions)
	return target, err
}

func upsertContactAliases(tx *gorm.DB, contact *projection_model.Contact, identities []ContactIdentityRef, version contactFieldVersion, now time.Time) (bool, error) {
	changed := false
	for _, identity := range identities {
		alias := projection_model.ContactIdentity{
			InstanceID: contact.InstanceID, Kind: identity.Kind, Value: identity.Value, ContactID: contact.ContactID,
			SourceOccurredAt: version.OccurredAt, SourceEventKey: version.EventKey, LastSyncedAt: now,
		}
		result := tx.Clauses(orderedUpsert(
			[]clause.Column{{Name: "instance_id"}, {Name: "identity_kind"}, {Name: "identity_value"}},
			[]string{"contact_id", "source_occurred_at", "source_event_key", "last_synced_at", "tombstoned_at", "updated_at"},
			"projected_contact_identities",
		)).Create(&alias)
		if result.Error != nil {
			return false, result.Error
		}
		changed = changed || result.RowsAffected > 0
	}
	return changed, nil
}

func applyContactAspect(contact *projection_model.Contact, patch ContactPatch) {
	switch patch.Aspect {
	case ContactAspectDetails:
		if patch.PreferredJID != "" {
			contact.PreferredJID = patch.PreferredJID
		}
		contact.PhoneJID = assignString(contact.PhoneJID, patch.PhoneJID)
		contact.LID = assignString(contact.LID, patch.LID)
		contact.Username = assignString(contact.Username, patch.Username)
		contact.FirstName = assignString(contact.FirstName, patch.FirstName)
		contact.FullName = assignString(contact.FullName, patch.FullName)
		contact.RedactedPhone = assignString(contact.RedactedPhone, patch.RedactedPhone)
		if patch.Found != nil {
			contact.Found = *patch.Found
		}
		if patch.SaveOnPrimaryAddressbook != nil {
			contact.SaveOnPrimaryAddressbook = patch.SaveOnPrimaryAddressbook
		}
		if patch.Deleted != nil {
			if *patch.Deleted {
				tombstone := patch.OccurredAt
				contact.TombstonedAt = &tombstone
			} else {
				contact.TombstonedAt = nil
			}
		}
	case ContactAspectPushName:
		contact.PushName = assignString(contact.PushName, patch.PushName)
	case ContactAspectBusinessName:
		contact.BusinessName = assignString(contact.BusinessName, patch.BusinessName)
	case ContactAspectPicture:
		contact.PictureID = assignString(contact.PictureID, patch.PictureID)
		contact.PictureAuthorJID = assignString(contact.PictureAuthorJID, patch.PictureAuthorJID)
		if patch.PictureRemoved != nil {
			contact.PictureRemoved = patch.PictureRemoved
		}
		if patch.PictureUpdatedAt != nil {
			value := patch.PictureUpdatedAt.UTC()
			contact.PictureUpdatedAt = &value
		}
	case ContactAspectAbout:
		contact.About = assignString(contact.About, patch.About)
		if patch.AboutUpdatedAt != nil {
			value := patch.AboutUpdatedAt.UTC()
			contact.AboutUpdatedAt = &value
		}
	}
}

func copyContactAspect(target, source *projection_model.Contact, aspect ContactAspect) {
	switch aspect {
	case ContactAspectDetails:
		target.PreferredJID, target.PhoneJID, target.LID, target.Username = source.PreferredJID, source.PhoneJID, source.LID, source.Username
		target.Found, target.FirstName, target.FullName, target.RedactedPhone = source.Found, source.FirstName, source.FullName, source.RedactedPhone
		target.SaveOnPrimaryAddressbook, target.TombstonedAt = source.SaveOnPrimaryAddressbook, source.TombstonedAt
	case ContactAspectPushName:
		target.PushName = source.PushName
	case ContactAspectBusinessName:
		target.BusinessName = source.BusinessName
	case ContactAspectPicture:
		target.PictureID, target.PictureAuthorJID, target.PictureRemoved, target.PictureUpdatedAt = source.PictureID, source.PictureAuthorJID, source.PictureRemoved, source.PictureUpdatedAt
	case ContactAspectAbout:
		target.About, target.AboutUpdatedAt = source.About, source.AboutUpdatedAt
	}
}

func assignString(current, incoming *string) *string {
	if incoming == nil {
		return current
	}
	value := *incoming
	return &value
}

func decodeContactVersions(raw json.RawMessage) (contactFieldVersions, error) {
	versions := make(contactFieldVersions)
	if len(raw) == 0 {
		return versions, nil
	}
	if err := json.Unmarshal(raw, &versions); err != nil {
		return nil, fmt.Errorf("decode contact field versions: %w", err)
	}
	return versions, nil
}

func contactVersionFor(contact projection_model.Contact, versions contactFieldVersions, aspect ContactAspect) contactFieldVersion {
	if version, exists := versions[aspect]; exists {
		return version
	}
	return contactFieldVersion{}
}

func contactVersionLess(left, right contactFieldVersion) bool {
	return left.OccurredAt.Before(right.OccurredAt) || (left.OccurredAt.Equal(right.OccurredAt) && left.EventKey < right.EventKey)
}
