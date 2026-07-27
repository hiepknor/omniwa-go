package group_list_service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	group_list_model "github.com/evolution-foundation/evolution-go/pkg/groupList/model"
	group_list_repository "github.com/evolution-foundation/evolution-go/pkg/groupList/repository"
	"github.com/google/uuid"
)

var (
	ErrInvalidInput       = errors.New("invalid group list input")
	ErrEmpty              = errors.New("group list is empty")
	ErrInvalidGroup       = errors.New("group list contains an invalid group")
	ErrGroupUnavailable   = errors.New("group list contains an unavailable group")
	ErrProjectionNotReady = errors.New("groups projection is not ready")
	ErrInvalidCursor      = errors.New("invalid group list cursor")
)

const (
	maxGroupListEntries = 10_000
	cursorVersion       = 1
)

type AuthorizationInput struct {
	Source            string    `json:"source"`
	EvidenceReference string    `json:"evidenceReference"`
	AuthorizedAt      time.Time `json:"authorizedAt"`
}

type CreateInput struct {
	Name           string
	Description    string
	GroupJIDs      []string
	Authorization  AuthorizationInput
	ActorReference string
}

type UpdateInput struct {
	Name            string
	Description     string
	GroupJIDs       []string
	ExpectedVersion int64
	Authorization   AuthorizationInput
	ActorReference  string
}

type ListResult struct {
	Items      []group_list_repository.Summary `json:"items"`
	NextCursor string                          `json:"nextCursor,omitempty"`
}

type EntryView struct {
	GroupJID          string    `json:"groupJid"`
	SnapshotName      string    `json:"snapshotName"`
	CurrentName       string    `json:"currentName"`
	Eligibility       string    `json:"eligibility"`
	EligibilityReason *string   `json:"eligibilityReason"`
	CanSend           bool      `json:"canSend"`
	CheckedAt         time.Time `json:"checkedAt"`
}

type EntryList struct {
	Items      []EntryView `json:"items"`
	NextCursor string      `json:"nextCursor,omitempty"`
}

type AuditList struct {
	Items      []group_list_model.AuditEvent `json:"items"`
	NextCursor string                        `json:"nextCursor,omitempty"`
}

type eligibilityEvaluator interface {
	Evaluate(context.Context, string, string, []string) ([]EligibilityResult, error)
}

type ManagementService struct {
	repository  group_list_repository.Repository
	eligibility eligibilityEvaluator
	now         func() time.Time
}

func NewManagementService(repository group_list_repository.Repository, eligibility eligibilityEvaluator) *ManagementService {
	return &ManagementService{repository: repository, eligibility: eligibility, now: time.Now}
}

func (s *ManagementService) Create(ctx context.Context, instanceID, instanceJID string, input CreateInput) (*group_list_repository.Summary, error) {
	if err := s.validate(ctx, instanceID); err != nil {
		return nil, err
	}
	listID := uuid.NewString()
	prepared, err := s.prepareMutation(ctx, instanceID, instanceJID, listID, input.Name, input.Description, input.GroupJIDs, input.Authorization, input.ActorReference)
	if err != nil {
		return nil, err
	}
	list, err := s.repository.Create(ctx, group_list_repository.CreateInput{
		ID: listID, InstanceID: instanceID, Name: prepared.name, NormalizedName: prepared.normalizedName,
		Description: prepared.description, AuthorizationSource: prepared.authorizationSource,
		AuthorizationReferenceHash: prepared.authorizationHash, AuthorizedAt: prepared.authorizedAt,
		ActorReferenceHash: prepared.actorHash, Entries: prepared.entries, AuditMetadata: mutationMetadata(len(prepared.entries)),
	})
	if err != nil {
		return nil, err
	}
	return &group_list_repository.Summary{GroupList: *list, GroupCount: int64(len(prepared.entries))}, nil
}

func (s *ManagementService) Update(ctx context.Context, instanceID, instanceJID, groupListID string, input UpdateInput) (*group_list_repository.Summary, error) {
	if err := s.validate(ctx, instanceID); err != nil || uuid.Validate(groupListID) != nil || input.ExpectedVersion < 1 {
		return nil, ErrInvalidInput
	}
	prepared, err := s.prepareMutation(ctx, instanceID, instanceJID, groupListID, input.Name, input.Description, input.GroupJIDs, input.Authorization, input.ActorReference)
	if err != nil {
		return nil, err
	}
	list, err := s.repository.Update(ctx, instanceID, groupListID, group_list_repository.UpdateInput{
		Name: prepared.name, NormalizedName: prepared.normalizedName, Description: prepared.description,
		ExpectedVersion: input.ExpectedVersion, AuthorizationSource: prepared.authorizationSource,
		AuthorizationReferenceHash: prepared.authorizationHash, AuthorizedAt: prepared.authorizedAt,
		ActorReferenceHash: prepared.actorHash, Entries: prepared.entries, AuditMetadata: mutationMetadata(len(prepared.entries)),
	})
	if err != nil {
		return nil, err
	}
	return &group_list_repository.Summary{GroupList: *list, GroupCount: int64(len(prepared.entries))}, nil
}

func (s *ManagementService) Get(ctx context.Context, instanceID, groupListID string) (*group_list_repository.Summary, error) {
	if err := s.validate(ctx, instanceID); err != nil || uuid.Validate(groupListID) != nil {
		return nil, ErrInvalidInput
	}
	return s.repository.Get(ctx, instanceID, groupListID)
}

func (s *ManagementService) List(ctx context.Context, instanceID, search string, limit int, encodedCursor string) (*ListResult, error) {
	if err := s.validate(ctx, instanceID); err != nil || limit < 1 || limit > 100 || utf8.RuneCountInString(search) > 128 {
		return nil, ErrInvalidInput
	}
	search = normalizeName(search)
	scope := cursorScope("lists", instanceID, search)
	cursor, err := decodeCursor(encodedCursor, "lists", scope)
	if err != nil {
		return nil, err
	}
	var repositoryCursor *group_list_repository.ListCursor
	if cursor != nil {
		repositoryCursor = &group_list_repository.ListCursor{NormalizedName: cursor.Name, ID: cursor.ID}
	}
	page, err := s.repository.List(ctx, instanceID, search, limit, repositoryCursor)
	if err != nil {
		return nil, err
	}
	result := &ListResult{Items: page.Items}
	if page.NextCursor != nil {
		result.NextCursor, err = encodeCursor(cursorEnvelope{Version: cursorVersion, Kind: "lists", Scope: scope, Name: page.NextCursor.NormalizedName, ID: page.NextCursor.ID})
	}
	return result, err
}

func (s *ManagementService) Entries(ctx context.Context, instanceID, instanceJID, groupListID string, limit int, encodedCursor string) (*EntryList, error) {
	if err := s.validate(ctx, instanceID); err != nil || uuid.Validate(groupListID) != nil || limit < 1 || limit > 100 {
		return nil, ErrInvalidInput
	}
	scope := cursorScope("entries", instanceID, groupListID)
	cursor, err := decodeCursor(encodedCursor, "entries", scope)
	if err != nil {
		return nil, err
	}
	var repositoryCursor *group_list_repository.EntryCursor
	if cursor != nil {
		repositoryCursor = &group_list_repository.EntryCursor{GroupJID: cursor.GroupJID}
	}
	page, err := s.repository.ListEntries(ctx, instanceID, groupListID, limit, repositoryCursor)
	if err != nil {
		return nil, err
	}
	groupJIDs := make([]string, len(page.Items))
	for index := range page.Items {
		groupJIDs[index] = page.Items[index].GroupJID
	}
	views := make([]EntryView, len(page.Items))
	if len(page.Items) > 0 {
		results, evaluateErr := s.eligibility.Evaluate(ctx, instanceID, instanceJID, groupJIDs)
		if evaluateErr != nil {
			return nil, evaluateErr
		}
		for index := range page.Items {
			views[index] = EntryView{
				GroupJID: page.Items[index].GroupJID, SnapshotName: page.Items[index].GroupNameSnapshot,
				CurrentName: results[index].CurrentName, Eligibility: results[index].Eligibility,
				EligibilityReason: results[index].EligibilityReason, CanSend: results[index].CanSend, CheckedAt: results[index].CheckedAt,
			}
		}
	}
	result := &EntryList{Items: views}
	if page.NextCursor != nil {
		result.NextCursor, err = encodeCursor(cursorEnvelope{Version: cursorVersion, Kind: "entries", Scope: scope, GroupJID: page.NextCursor.GroupJID})
	}
	return result, err
}

func (s *ManagementService) Delete(ctx context.Context, instanceID, groupListID, actorReference string) error {
	if err := s.validate(ctx, instanceID); err != nil || uuid.Validate(groupListID) != nil || strings.TrimSpace(actorReference) == "" {
		return ErrInvalidInput
	}
	return s.repository.Delete(ctx, instanceID, groupListID, hashReference(groupListID, actorReference))
}

func (s *ManagementService) Audit(ctx context.Context, instanceID, groupListID string, limit int, encodedCursor string) (*AuditList, error) {
	if err := s.validate(ctx, instanceID); err != nil || uuid.Validate(groupListID) != nil || limit < 1 || limit > 100 {
		return nil, ErrInvalidInput
	}
	scope := cursorScope("audit", instanceID, groupListID)
	cursor, err := decodeCursor(encodedCursor, "audit", scope)
	if err != nil {
		return nil, err
	}
	var repositoryCursor *group_list_repository.AuditCursor
	if cursor != nil {
		repositoryCursor = &group_list_repository.AuditCursor{OccurredAt: cursor.At, ID: cursor.ID}
	}
	page, err := s.repository.ListAudit(ctx, instanceID, groupListID, limit, repositoryCursor)
	if err != nil {
		return nil, err
	}
	result := &AuditList{Items: page.Items}
	if page.NextCursor != nil {
		result.NextCursor, err = encodeCursor(cursorEnvelope{Version: cursorVersion, Kind: "audit", Scope: scope, At: page.NextCursor.OccurredAt, ID: page.NextCursor.ID})
	}
	return result, err
}

type preparedMutation struct {
	name                string
	normalizedName      string
	description         *string
	authorizationSource string
	authorizationHash   string
	authorizedAt        time.Time
	actorHash           string
	entries             []group_list_repository.EntryInput
}

func (s *ManagementService) prepareMutation(ctx context.Context, instanceID, instanceJID, groupListID, name, description string, groupJIDs []string, authorization AuthorizationInput, actorReference string) (*preparedMutation, error) {
	name = strings.TrimSpace(name)
	normalizedName := normalizeName(name)
	if name == "" || utf8.RuneCountInString(name) > 255 || normalizedName == "" || utf8.RuneCountInString(normalizedName) > 255 {
		return nil, ErrInvalidInput
	}
	description = strings.TrimSpace(description)
	if utf8.RuneCountInString(description) > 2_000 {
		return nil, ErrInvalidInput
	}
	var descriptionPointer *string
	if description != "" {
		descriptionPointer = &description
	}
	if len(groupJIDs) == 0 {
		return nil, ErrEmpty
	}
	if len(groupJIDs) > maxGroupListEntries {
		return nil, ErrInvalidInput
	}
	source := strings.TrimSpace(authorization.Source)
	evidence := strings.TrimSpace(authorization.EvidenceReference)
	now := s.now().UTC()
	if source == "" || len(source) > 64 || evidence == "" || len(evidence) > 4_096 || authorization.AuthorizedAt.IsZero() || authorization.AuthorizedAt.After(now) || strings.TrimSpace(actorReference) == "" {
		return nil, ErrInvalidInput
	}
	canonical := make([]string, len(groupJIDs))
	seen := make(map[string]struct{}, len(groupJIDs))
	for index, value := range groupJIDs {
		groupJID, err := CanonicalGroupJID(value)
		if err != nil {
			return nil, ErrInvalidGroup
		}
		if _, exists := seen[groupJID]; exists {
			return nil, ErrInvalidGroup
		}
		seen[groupJID] = struct{}{}
		canonical[index] = groupJID
	}
	eligibility, err := s.eligibility.Evaluate(ctx, instanceID, instanceJID, canonical)
	if err != nil {
		return nil, err
	}
	entries := make([]group_list_repository.EntryInput, len(eligibility))
	for index, result := range eligibility {
		switch result.Eligibility {
		case EligibilityUnknown:
			return nil, ErrProjectionNotReady
		case EligibilityUnavailable:
			return nil, ErrGroupUnavailable
		case EligibilityEligible:
			entries[index] = group_list_repository.EntryInput{GroupJID: result.GroupJID, GroupNameSnapshot: boundedName(result.CurrentName)}
		default:
			return nil, errors.New("eligibility returned an unsupported state")
		}
	}
	return &preparedMutation{
		name: name, normalizedName: normalizedName, description: descriptionPointer, authorizationSource: source,
		authorizationHash: hashReference(groupListID, evidence), authorizedAt: authorization.AuthorizedAt.UTC(),
		actorHash: hashReference(groupListID, actorReference), entries: entries,
	}, nil
}

func (s *ManagementService) validate(ctx context.Context, instanceID string) error {
	if s == nil || s.repository == nil || s.eligibility == nil || s.now == nil || ctx == nil || uuid.Validate(instanceID) != nil {
		return ErrInvalidInput
	}
	return nil
}

func normalizeName(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func boundedName(value string) string {
	runes := []rune(value)
	if len(runes) > 255 {
		runes = runes[:255]
	}
	return string(runes)
}

func hashReference(scope, value string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + value))
	return hex.EncodeToString(sum[:])
}

func mutationMetadata(entryCount int) json.RawMessage {
	encoded, _ := json.Marshal(map[string]int{"entryCount": entryCount})
	return encoded
}

type cursorEnvelope struct {
	Version  int       `json:"v"`
	Kind     string    `json:"kind"`
	Scope    string    `json:"scope"`
	Name     string    `json:"name,omitempty"`
	GroupJID string    `json:"groupJid,omitempty"`
	At       time.Time `json:"at,omitempty"`
	ID       string    `json:"id,omitempty"`
}

func cursorScope(kind string, values ...string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + strings.Join(values, "\x00")))
	return hex.EncodeToString(sum[:])
}

func decodeCursor(value, kind, scope string) (*cursorEnvelope, error) {
	if value == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	var cursor cursorEnvelope
	if json.Unmarshal(payload, &cursor) != nil || cursor.Version != cursorVersion || cursor.Kind != kind || cursor.Scope != scope {
		return nil, ErrInvalidCursor
	}
	switch kind {
	case "lists":
		if cursor.Name == "" || uuid.Validate(cursor.ID) != nil {
			return nil, ErrInvalidCursor
		}
	case "entries":
		if _, err := CanonicalGroupJID(cursor.GroupJID); err != nil {
			return nil, ErrInvalidCursor
		}
	case "audit":
		if cursor.At.IsZero() || uuid.Validate(cursor.ID) != nil {
			return nil, ErrInvalidCursor
		}
	default:
		return nil, ErrInvalidCursor
	}
	return &cursor, nil
}

func encodeCursor(cursor cursorEnvelope) (string, error) {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}
