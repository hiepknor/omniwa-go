package projection_service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	"go.mau.fi/whatsmeow/types"
	"gorm.io/gorm"
)

const GroupsProjectionSchemaVersion int64 = 3

var (
	ErrGroupsProjectionNotReady = errors.New("groups projection is not ready")
	ErrInvalidGroupCursor       = errors.New("invalid group search cursor")
	ErrInvalidGroupSearch       = errors.New("invalid group search query")
)

const groupCursorVersion = 1

type ProjectionReadMeta struct {
	Source       string                      `json:"source"`
	SyncStatus   projection_model.SyncStatus `json:"syncStatus"`
	LastSyncedAt *time.Time                  `json:"lastSyncedAt,omitempty"`
	NextCursor   string                      `json:"nextCursor,omitempty"`
}

type groupReadRepository interface {
	Get(context.Context, string, string) (*projection_model.Group, []projection_model.GroupParticipant, error)
	List(context.Context, string) ([]projection_repository.GroupRecord, error)
	Search(context.Context, string, string, int, *projection_repository.GroupCursor) (*projection_repository.GroupPage, error)
	GetInviteLink(context.Context, string, string) (*string, error)
}

type groupCursorEnvelope struct {
	Version int    `json:"v"`
	Kind    string `json:"kind"`
	Scope   string `json:"scope"`
	GroupID string `json:"groupId"`
}

type groupReadState interface {
	Get(instanceID, resource string) (*projection_model.State, error)
}

type GroupReader struct {
	groups groupReadRepository
	state  groupReadState
}

func NewGroupReader(groups groupReadRepository, state groupReadState) *GroupReader {
	return &GroupReader{groups: groups, state: state}
}

func (r *GroupReader) List(ctx context.Context, instanceID string) ([]*types.GroupInfo, *ProjectionReadMeta, error) {
	meta, err := r.readMeta(instanceID)
	if err != nil {
		return nil, nil, err
	}
	records, err := r.groups.List(ctx, instanceID)
	if err != nil {
		return nil, nil, err
	}
	result := make([]*types.GroupInfo, len(records))
	for index := range records {
		info, err := groupInfoFromProjection(&records[index].Group, records[index].Participants)
		if err != nil {
			return nil, nil, err
		}
		result[index] = info
	}
	return result, meta, nil
}

func (r *GroupReader) Search(ctx context.Context, instanceID, term string, limit int, encodedCursor string) ([]*types.GroupInfo, *ProjectionReadMeta, error) {
	term = strings.ToLower(strings.TrimSpace(term))
	if len(term) > 128 || limit < 1 || limit > 200 {
		return nil, nil, ErrInvalidGroupSearch
	}
	meta, err := r.readMeta(instanceID)
	if err != nil {
		return nil, nil, err
	}
	scope := groupCursorScope(instanceID, term)
	cursor, err := decodeGroupCursor(encodedCursor, scope)
	if err != nil {
		return nil, nil, err
	}
	page, err := r.groups.Search(ctx, instanceID, term, limit, cursor)
	if err != nil {
		return nil, nil, err
	}
	if page == nil {
		return nil, nil, errors.New("group search repository returned no page")
	}
	result := make([]*types.GroupInfo, len(page.Items))
	for index := range page.Items {
		result[index], err = groupInfoFromProjection(&page.Items[index].Group, page.Items[index].Participants)
		if err != nil {
			return nil, nil, err
		}
	}
	if page.NextCursor != nil {
		meta.NextCursor, err = encodeGroupCursor(page.NextCursor, scope)
		if err != nil {
			return nil, nil, err
		}
	}
	return result, meta, nil
}

func groupCursorScope(instanceID, term string) string {
	sum := sha256.Sum256([]byte(instanceID + "\x00" + term))
	return hex.EncodeToString(sum[:])
}

func decodeGroupCursor(value, scope string) (*projection_repository.GroupCursor, error) {
	if value == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, ErrInvalidGroupCursor
	}
	var envelope groupCursorEnvelope
	if json.Unmarshal(payload, &envelope) != nil || envelope.Version != groupCursorVersion || envelope.Kind != "groups" || envelope.Scope != scope ||
		envelope.GroupID == "" || len(envelope.GroupID) > 255 {
		return nil, ErrInvalidGroupCursor
	}
	return &projection_repository.GroupCursor{GroupID: envelope.GroupID}, nil
}

func encodeGroupCursor(cursor *projection_repository.GroupCursor, scope string) (string, error) {
	if cursor == nil || cursor.GroupID == "" || len(cursor.GroupID) > 255 || scope == "" {
		return "", ErrInvalidGroupCursor
	}
	payload, err := json.Marshal(groupCursorEnvelope{Version: groupCursorVersion, Kind: "groups", Scope: scope, GroupID: cursor.GroupID})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func (r *GroupReader) Get(ctx context.Context, instanceID, groupID string) (*types.GroupInfo, *ProjectionReadMeta, error) {
	meta, err := r.readMeta(instanceID)
	if err != nil {
		return nil, nil, err
	}
	group, participants, err := r.groups.Get(ctx, instanceID, groupID)
	if err != nil {
		return nil, nil, err
	}
	info, err := groupInfoFromProjection(group, participants)
	return info, meta, err
}

func (r *GroupReader) InviteLink(ctx context.Context, instanceID, groupID string) (string, *ProjectionReadMeta, bool, error) {
	meta, err := r.readMeta(instanceID)
	if err != nil {
		return "", nil, false, err
	}
	inviteLink, err := r.groups.GetInviteLink(ctx, instanceID, groupID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", meta, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	if inviteLink == nil || *inviteLink == "" {
		return "", meta, false, nil
	}
	return *inviteLink, meta, true, nil
}

func (r *GroupReader) readMeta(instanceID string) (*ProjectionReadMeta, error) {
	if r == nil || r.groups == nil || r.state == nil || instanceID == "" {
		return nil, errors.New("group projection reader dependencies and instance identity are required")
	}
	state, err := r.state.Get(instanceID, groupResource)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrGroupsProjectionNotReady
	}
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, errors.New("group projection state is missing")
	}
	usableStatus := state.SyncStatus == projection_model.SyncStatusReady || state.SyncStatus == projection_model.SyncStatusStale || state.SyncStatus == projection_model.SyncStatusSyncing
	if !usableStatus || state.LastReconciledAt == nil || state.SchemaVersion < GroupsProjectionSchemaVersion {
		return nil, ErrGroupsProjectionNotReady
	}
	lastSyncedAt := state.LastReconciledAt.UTC()
	return &ProjectionReadMeta{Source: "projection", SyncStatus: state.SyncStatus, LastSyncedAt: &lastSyncedAt}, nil
}

func groupInfoFromProjection(group *projection_model.Group, participants []projection_model.GroupParticipant) (*types.GroupInfo, error) {
	if group == nil {
		return nil, errors.New("projected group is required")
	}
	jid, err := types.ParseJID(group.GroupID)
	if err != nil {
		return nil, fmt.Errorf("invalid projected group identity: %w", err)
	}
	owner, err := optionalJID(group.OwnerJID)
	if err != nil {
		return nil, err
	}
	ownerPhone, err := optionalJID(group.OwnerPhoneJID)
	if err != nil {
		return nil, err
	}
	nameSetBy, err := optionalJID(group.NameSetBy)
	if err != nil {
		return nil, err
	}
	nameSetByPhone, err := optionalJID(group.NameSetByPhone)
	if err != nil {
		return nil, err
	}
	topicSetBy, err := optionalJID(group.TopicSetBy)
	if err != nil {
		return nil, err
	}
	topicSetByPhone, err := optionalJID(group.TopicSetByPhone)
	if err != nil {
		return nil, err
	}
	parent, err := optionalJID(group.ParentGroupID)
	if err != nil {
		return nil, err
	}
	projectedParticipants := make([]types.GroupParticipant, len(participants))
	for index := range participants {
		participant, err := participantFromProjection(&participants[index])
		if err != nil {
			return nil, err
		}
		projectedParticipants[index] = participant
	}
	timer := int64Value(group.EphemeralTimer)
	if timer < 0 || timer > math.MaxUint32 {
		return nil, errors.New("projected group disappearing timer is invalid")
	}
	participantCount := len(projectedParticipants)
	if group.ParticipantCount != nil {
		participantCount = *group.ParticipantCount
	}
	return &types.GroupInfo{
		JID: jid, OwnerJID: owner, OwnerPN: ownerPhone,
		GroupName:                   types.GroupName{Name: stringValue(group.Name), NameSetAt: timeValue(group.NameSetAt), NameSetBy: nameSetBy, NameSetByPN: nameSetByPhone},
		GroupTopic:                  types.GroupTopic{Topic: stringValue(group.Topic), TopicID: stringValue(group.TopicID), TopicSetAt: timeValue(group.TopicSetAt), TopicSetBy: topicSetBy, TopicSetByPN: topicSetByPhone, TopicDeleted: boolValue(group.TopicDeleted)},
		GroupLocked:                 types.GroupLocked{IsLocked: boolValue(group.Locked)},
		GroupAnnounce:               types.GroupAnnounce{IsAnnounce: boolValue(group.Announce), AnnounceVersionID: stringValue(group.AnnounceVersion)},
		GroupEphemeral:              types.GroupEphemeral{IsEphemeral: boolValue(group.EphemeralEnabled), DisappearingTimer: uint32(timer)},
		GroupIncognito:              types.GroupIncognito{IsIncognito: boolValue(group.Incognito)},
		GroupParent:                 types.GroupParent{IsParent: boolValue(group.IsParent), DefaultMembershipApprovalMode: stringValue(group.DefaultApprovalMode)},
		GroupLinkedParent:           types.GroupLinkedParent{LinkedParentJID: parent},
		GroupIsDefaultSub:           types.GroupIsDefaultSub{IsDefaultSubGroup: boolValue(group.IsDefaultSubgroup)},
		GroupMembershipApprovalMode: types.GroupMembershipApprovalMode{IsJoinApprovalRequired: boolValue(group.JoinApprovalRequired)},
		AddressingMode:              types.AddressingMode(stringValue(group.AddressingMode)), GroupCreated: timeValue(group.ProviderCreatedAt),
		CreatorCountryCode: stringValue(group.CreatorCountryCode), ParticipantVersionID: stringValue(group.ParticipantVersion),
		Participants: projectedParticipants, ParticipantCount: participantCount, MemberAddMode: types.GroupMemberAddMode(stringValue(group.MemberAddMode)),
		Suspended: boolValue(group.Suspended),
	}, nil
}

func participantFromProjection(participant *projection_model.GroupParticipant) (types.GroupParticipant, error) {
	jid, err := types.ParseJID(participant.ParticipantID)
	if err != nil {
		return types.GroupParticipant{}, fmt.Errorf("invalid projected participant identity: %w", err)
	}
	phone, err := optionalJID(participant.PhoneNumberJID)
	if err != nil {
		return types.GroupParticipant{}, err
	}
	lid, err := optionalJID(participant.LID)
	if err != nil {
		return types.GroupParticipant{}, err
	}
	return types.GroupParticipant{
		JID: jid, PhoneNumber: phone, LID: lid, DisplayName: stringValue(participant.DisplayName),
		IsAdmin: participant.Role == projection_model.ParticipantRoleAdmin, IsSuperAdmin: participant.Role == projection_model.ParticipantRoleSuperAdmin,
	}, nil
}

func optionalJID(value *string) (types.JID, error) {
	if value == nil || *value == "" {
		return types.JID{}, nil
	}
	jid, err := types.ParseJID(*value)
	if err != nil {
		return types.JID{}, fmt.Errorf("invalid projected JID: %w", err)
	}
	return jid, nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func boolValue(value *bool) bool { return value != nil && *value }
func int64Value(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
func timeValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}
