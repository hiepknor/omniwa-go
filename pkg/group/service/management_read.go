package group_service

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

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/evolution-foundation/evolution-go/pkg/utils"
	"github.com/google/uuid"
	"go.mau.fi/whatsmeow/types"
	"gorm.io/gorm"
)

var (
	ErrInvalidManagementFilter    = errors.New("invalid group management filter")
	ErrInvalidManagementCursor    = errors.New("invalid group management cursor")
	ErrManagementGroupUnavailable = errors.New("group management projection is unavailable")
)

const managementCursorVersion = 1

type GroupManagementFilters struct {
	Query           string
	Type            string
	MyRole          string
	SendMode        string
	State           string
	MembershipState string
}

type GroupMemberFilters struct {
	Query string
	Role  string
}

type GroupDirectorySummary struct {
	Total          int64     `json:"total"`
	Active         int64     `json:"active"`
	Suspended      int64     `json:"suspended"`
	Communities    int64     `json:"communities"`
	Subgroups      int64     `json:"subgroups"`
	AdminsOnlySend int64     `json:"adminsOnlySend"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type GroupSummary struct {
	GroupJID           string     `json:"groupJid"`
	Name               *string    `json:"name,omitempty"`
	DescriptionPreview *string    `json:"descriptionPreview,omitempty"`
	Type               string     `json:"type" enums:"group,community,subgroup,unknown"`
	State              string     `json:"state" enums:"active,suspended,dissolved,unavailable,unknown"`
	MembershipState    string     `json:"membershipState" enums:"joined,left,removed,unknown"`
	MyRole             string     `json:"myRole" enums:"owner,superadmin,admin,member,not_member,unknown"`
	SendMode           string     `json:"sendMode" enums:"all_members,admins_only,unknown"`
	MemberCount        *int       `json:"memberCount,omitempty"`
	ParentGroupJID     *string    `json:"parentGroupJid,omitempty"`
	IsDefaultSubgroup  *bool      `json:"isDefaultSubgroup,omitempty"`
	CreatedAt          *time.Time `json:"createdAt,omitempty"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

type SafeMemberReference struct {
	MemberID string `json:"memberId"`
}

type GroupPhotoMetadata struct {
	Available    *bool      `json:"available,omitempty"`
	MediaAssetID *string    `json:"mediaAssetId,omitempty" format:"uuid"`
	UpdatedAt    *time.Time `json:"updatedAt,omitempty"`
}

// GroupInviteLinkMetadata describes projection-cache availability only. It
// does not assert that the provider has no invite link when Available is false.
type GroupInviteLinkMetadata struct {
	Available bool       `json:"available"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

type ActionDecision struct {
	State     string    `json:"state" enums:"allowed,denied,unknown"`
	Reason    *string   `json:"reason" enums:"admin_required,owner_required,not_a_member,group_suspended,group_unavailable,protected_member,self_action_not_allowed,already_admin,not_an_admin,permission_unknown,projection_not_ready,provider_disconnected,unsupported"`
	CheckedAt time.Time `json:"checkedAt"`
}

type GroupActions struct {
	SendMessage     ActionDecision `json:"sendMessage"`
	EditName        ActionDecision `json:"editName"`
	EditDescription ActionDecision `json:"editDescription"`
	EditSettings    ActionDecision `json:"editSettings"`
	AddMembers      ActionDecision `json:"addMembers"`
	RemoveMembers   ActionDecision `json:"removeMembers"`
	PromoteMembers  ActionDecision `json:"promoteMembers"`
	DemoteMembers   ActionDecision `json:"demoteMembers"`
	ReadInviteLink  ActionDecision `json:"readInviteLink"`
	ResetInviteLink ActionDecision `json:"resetInviteLink"`
	SetPhoto        ActionDecision `json:"setPhoto"`
	LeaveGroup      ActionDecision `json:"leaveGroup"`
}

type GroupDetail struct {
	GroupSummary
	Description           *string                 `json:"description,omitempty"`
	AdminCount            *int64                  `json:"adminCount,omitempty"`
	Owner                 *SafeMemberReference    `json:"owner,omitempty"`
	Announce              *bool                   `json:"announce,omitempty"`
	Locked                *bool                   `json:"locked,omitempty"`
	JoinApproval          *bool                   `json:"joinApproval,omitempty"`
	MemberAddMode         string                  `json:"memberAddMode" enums:"all_members,admins_only,unknown"`
	EphemeralEnabled      *bool                   `json:"ephemeralEnabled,omitempty"`
	EphemeralTimerSeconds *int64                  `json:"ephemeralTimerSeconds,omitempty"`
	Photo                 *GroupPhotoMetadata     `json:"photo,omitempty"`
	InviteLink            GroupInviteLinkMetadata `json:"inviteLink"`
	Actions               GroupActions            `json:"actions"`
}

type GroupMemberActions struct {
	Promote ActionDecision `json:"promote"`
	Demote  ActionDecision `json:"demote"`
	Remove  ActionDecision `json:"remove"`
}

type GroupMember struct {
	MemberID        string             `json:"memberId" format:"uuid"`
	DisplayName     *string            `json:"displayName,omitempty"`
	Role            string             `json:"role" enums:"owner,superadmin,admin,member"`
	MembershipState string             `json:"membershipState" enums:"active,pending,removed,unknown"`
	Actions         GroupMemberActions `json:"actions"`
}

type managementReadRepository interface {
	SearchManagement(context.Context, string, string, projection_repository.GroupManagementFilter, int, *projection_repository.GroupCursor) (*projection_repository.GroupManagementPage, error)
	GetManagementSummary(context.Context, string) (*projection_repository.GroupManagementSummary, error)
	GetManagement(context.Context, string, string, string) (*projection_repository.GroupManagementRecord, error)
	ListManagementMembers(context.Context, string, string, string, projection_repository.GroupMemberFilter, int, *projection_repository.GroupMemberCursor) (*projection_repository.GroupManagementRecord, *projection_repository.GroupMemberPage, error)
	GetManagementMember(context.Context, string, string, string, string) (*projection_repository.GroupManagementRecord, *projection_repository.GroupMemberRecord, error)
}

type managementReadState interface {
	GetServingState(instanceID, resource string) (*projection_model.State, error)
}

type ManagementReader struct {
	groups managementReadRepository
	state  managementReadState
	now    func() time.Time
}

func NewManagementReader(groups managementReadRepository, state managementReadState) *ManagementReader {
	return &ManagementReader{groups: groups, state: state, now: time.Now}
}

type managementCursorEnvelope struct {
	Version int    `json:"v"`
	Kind    string `json:"kind"`
	Scope   string `json:"scope"`
	GroupID string `json:"groupId"`
}

type memberCursorEnvelope struct {
	Version  int    `json:"v"`
	Kind     string `json:"kind"`
	Scope    string `json:"scope"`
	SortKey  string `json:"sortKey"`
	PublicID string `json:"memberId"`
}

func (r *ManagementReader) Search(ctx context.Context, instance *instance_model.Instance, filters GroupManagementFilters, limit int, encodedCursor string) ([]GroupSummary, *projection_service.ProjectionReadMeta, error) {
	if r == nil || r.groups == nil || instance == nil || instance.Id == "" || instance.Jid == "" || limit < 1 || limit > 200 || !validManagementFilters(filters) {
		return nil, nil, ErrInvalidManagementFilter
	}
	meta, err := r.readMeta(instance.Id)
	if err != nil {
		return nil, nil, err
	}
	filters.Query = strings.ToLower(strings.TrimSpace(filters.Query))
	scope := managementCursorScope(instance.Id, filters)
	cursor, err := decodeManagementCursor(encodedCursor, scope)
	if err != nil {
		return nil, nil, err
	}
	page, err := r.groups.SearchManagement(ctx, instance.Id, normalizedActorIdentity(instance.Jid), projection_repository.GroupManagementFilter{
		Term: filters.Query, Type: filters.Type, MyRole: filters.MyRole, SendMode: filters.SendMode, State: filters.State, MembershipState: filters.MembershipState,
	}, limit, cursor)
	if err != nil {
		return nil, nil, err
	}
	if page == nil {
		return nil, nil, errors.New("group management repository returned no page")
	}
	items := make([]GroupSummary, len(page.Items))
	for index := range page.Items {
		items[index] = managementSummary(page.Items[index])
	}
	if page.NextCursor != nil {
		meta.NextCursor, err = encodeManagementCursor(page.NextCursor, scope)
		if err != nil {
			return nil, nil, err
		}
	}
	return items, meta, nil
}

func (r *ManagementReader) Summary(ctx context.Context, instance *instance_model.Instance) (*GroupDirectorySummary, *projection_service.ProjectionReadMeta, error) {
	if r == nil || r.groups == nil || ctx == nil || instance == nil || uuid.Validate(instance.Id) != nil {
		return nil, nil, ErrInvalidManagementFilter
	}
	meta, err := r.readMeta(instance.Id)
	if err != nil {
		return nil, nil, err
	}
	aggregate, err := r.groups.GetManagementSummary(ctx, instance.Id)
	if err != nil {
		return nil, nil, err
	}
	if aggregate == nil || meta.LastSyncedAt == nil {
		return nil, nil, errors.New("group summary repository returned no aggregate")
	}
	return &GroupDirectorySummary{
		Total: aggregate.Total, Active: aggregate.Active, Suspended: aggregate.Suspended,
		Communities: aggregate.Communities, Subgroups: aggregate.Subgroups,
		AdminsOnlySend: aggregate.AdminsOnlySend, UpdatedAt: meta.LastSyncedAt.UTC(),
	}, meta, nil
}

func (r *ManagementReader) Get(ctx context.Context, instance *instance_model.Instance, groupJID string) (*GroupDetail, *projection_service.ProjectionReadMeta, error) {
	record, meta, err := r.getRecord(ctx, instance, groupJID)
	if err != nil {
		return nil, meta, err
	}
	detail := managementDetail(*record, r.now().UTC())
	return &detail, meta, nil
}

func (r *ManagementReader) InviteLink(ctx context.Context, instance *instance_model.Instance, groupJID string) (string, bool, ActionDecision, *projection_service.ProjectionReadMeta, error) {
	record, meta, err := r.getRecord(ctx, instance, groupJID)
	if err != nil {
		return "", false, ActionDecision{}, meta, err
	}
	detail := managementDetail(*record, r.now().UTC())
	if record.Group.InviteLink == nil || strings.TrimSpace(*record.Group.InviteLink) == "" {
		return "", false, detail.Actions.ReadInviteLink, meta, nil
	}
	return *record.Group.InviteLink, true, detail.Actions.ReadInviteLink, meta, nil
}

func (r *ManagementReader) getRecord(ctx context.Context, instance *instance_model.Instance, groupJID string) (*projection_repository.GroupManagementRecord, *projection_service.ProjectionReadMeta, error) {
	if r == nil || r.groups == nil || instance == nil || instance.Id == "" || instance.Jid == "" {
		return nil, nil, errors.New("group management reader and instance are required")
	}
	jid, ok := utils.ParseJID(groupJID)
	if !ok || jid.Server != types.GroupServer {
		return nil, nil, ErrInvalidManagementFilter
	}
	meta, err := r.readMeta(instance.Id)
	if err != nil {
		return nil, nil, err
	}
	record, err := r.groups.GetManagement(ctx, instance.Id, normalizedActorIdentity(instance.Jid), jid.String())
	if err != nil {
		return nil, meta, err
	}
	if record == nil {
		return nil, meta, errors.New("group management repository returned no record")
	}
	return record, meta, nil
}

func (r *ManagementReader) Members(ctx context.Context, instance *instance_model.Instance, groupJID string, filters GroupMemberFilters, limit int, encodedCursor string) ([]GroupMember, *projection_service.ProjectionReadMeta, error) {
	if r == nil || r.groups == nil || instance == nil || instance.Id == "" || instance.Jid == "" || limit < 1 || limit > 200 ||
		utf8.RuneCountInString(strings.TrimSpace(filters.Query)) > 128 || !oneOf(filters.Role, "", "owner", "superadmin", "admin", "member") {
		return nil, nil, ErrInvalidManagementFilter
	}
	jid, ok := utils.ParseJID(groupJID)
	if !ok || jid.Server != types.GroupServer {
		return nil, nil, ErrInvalidManagementFilter
	}
	meta, err := r.readMeta(instance.Id)
	if err != nil {
		return nil, nil, err
	}
	filters.Query = strings.ToLower(strings.TrimSpace(filters.Query))
	scope := memberCursorScope(instance.Id, jid.String(), filters)
	cursor, err := decodeMemberCursor(encodedCursor, scope)
	if err != nil {
		return nil, nil, err
	}
	record, page, err := r.groups.ListManagementMembers(ctx, instance.Id, normalizedActorIdentity(instance.Jid), jid.String(), projection_repository.GroupMemberFilter{Term: filters.Query, Role: filters.Role}, limit, cursor)
	if err != nil {
		return nil, nil, err
	}
	if record == nil || page == nil {
		return nil, nil, errors.New("group member repository returned no result")
	}
	summary := managementSummary(*record)
	if summary.State == "unavailable" || summary.State == "dissolved" {
		return nil, nil, ErrManagementGroupUnavailable
	}
	checkedAt := r.now().UTC()
	items := make([]GroupMember, len(page.Items))
	for index := range page.Items {
		item := page.Items[index]
		if uuid.Validate(item.Participant.PublicID) != nil {
			return nil, nil, errors.New("projected group member has invalid public identity")
		}
		items[index] = GroupMember{
			MemberID: item.Participant.PublicID, DisplayName: item.Participant.DisplayName, Role: item.Role,
			MembershipState: "active", Actions: memberActions(summary, item.Role, item.IsActor, checkedAt),
		}
	}
	if page.NextCursor != nil {
		meta.NextCursor, err = encodeMemberCursor(page.NextCursor, scope)
		if err != nil {
			return nil, nil, err
		}
	}
	return items, meta, nil
}

func (r *ManagementReader) readMeta(instanceID string) (*projection_service.ProjectionReadMeta, error) {
	if r.state == nil {
		return nil, errors.New("group projection state is required")
	}
	state, err := r.state.GetServingState(instanceID, "groups")
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, projection_service.ErrGroupsProjectionNotReady
	}
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, projection_service.ErrGroupsProjectionNotReady
	}
	usable := state.SyncStatus == projection_model.SyncStatusReady || state.SyncStatus == projection_model.SyncStatusStale || state.SyncStatus == projection_model.SyncStatusSyncing
	if !usable || state.LastReconciledAt == nil || state.SchemaVersion < projection_service.GroupsProjectionSchemaVersion {
		return nil, projection_service.ErrGroupsProjectionNotReady
	}
	lastSyncedAt := state.LastReconciledAt.UTC()
	return &projection_service.ProjectionReadMeta{Source: "projection", SyncStatus: state.SyncStatus, LastSyncedAt: &lastSyncedAt}, nil
}

func validManagementFilters(filters GroupManagementFilters) bool {
	return utf8.RuneCountInString(strings.TrimSpace(filters.Query)) <= 128 &&
		oneOf(filters.Type, "", "group", "community", "subgroup", "unknown") &&
		oneOf(filters.MyRole, "", "owner", "superadmin", "admin", "member", "not_member", "unknown") &&
		oneOf(filters.SendMode, "", "all_members", "admins_only", "unknown") &&
		oneOf(filters.State, "", "active", "suspended", "dissolved", "unavailable", "unknown") &&
		oneOf(filters.MembershipState, "", "joined", "left", "removed", "unknown")
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func normalizedActorIdentity(value string) string {
	jid, err := types.ParseJID(value)
	if err != nil || jid.IsEmpty() {
		return value
	}
	return jid.ToNonAD().String()
}

func managementCursorScope(instanceID string, filters GroupManagementFilters) string {
	payload, _ := json.Marshal(struct {
		InstanceID string                 `json:"instanceId"`
		Filters    GroupManagementFilters `json:"filters"`
	}{instanceID, filters})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func decodeManagementCursor(value, scope string) (*projection_repository.GroupCursor, error) {
	if value == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, ErrInvalidManagementCursor
	}
	var envelope managementCursorEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Version != managementCursorVersion || envelope.Kind != "group_management" || envelope.Scope != scope || envelope.GroupID == "" || len(envelope.GroupID) > 255 {
		return nil, ErrInvalidManagementCursor
	}
	return &projection_repository.GroupCursor{GroupID: envelope.GroupID}, nil
}

func encodeManagementCursor(cursor *projection_repository.GroupCursor, scope string) (string, error) {
	if cursor == nil || cursor.GroupID == "" || len(cursor.GroupID) > 255 || scope == "" {
		return "", ErrInvalidManagementCursor
	}
	payload, err := json.Marshal(managementCursorEnvelope{Version: managementCursorVersion, Kind: "group_management", Scope: scope, GroupID: cursor.GroupID})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func memberCursorScope(instanceID, groupJID string, filters GroupMemberFilters) string {
	payload, _ := json.Marshal(struct {
		InstanceID string             `json:"instanceId"`
		GroupJID   string             `json:"groupJid"`
		Filters    GroupMemberFilters `json:"filters"`
	}{instanceID, groupJID, filters})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func decodeMemberCursor(value, scope string) (*projection_repository.GroupMemberCursor, error) {
	if value == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, ErrInvalidManagementCursor
	}
	var envelope memberCursorEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.Version != managementCursorVersion || envelope.Kind != "group_members" || envelope.Scope != scope ||
		len(envelope.SortKey) > 512 || uuid.Validate(envelope.PublicID) != nil {
		return nil, ErrInvalidManagementCursor
	}
	return &projection_repository.GroupMemberCursor{SortKey: envelope.SortKey, PublicID: envelope.PublicID}, nil
}

func encodeMemberCursor(cursor *projection_repository.GroupMemberCursor, scope string) (string, error) {
	if cursor == nil || len(cursor.SortKey) > 512 || uuid.Validate(cursor.PublicID) != nil || scope == "" {
		return "", ErrInvalidManagementCursor
	}
	payload, err := json.Marshal(memberCursorEnvelope{Version: managementCursorVersion, Kind: "group_members", Scope: scope, SortKey: cursor.SortKey, PublicID: cursor.PublicID})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func managementSummary(record projection_repository.GroupManagementRecord) GroupSummary {
	groupType := managementGroupType(record.Group)
	state := managementGroupState(record.Group)
	membership, role := managementActor(record)
	sendMode := "unknown"
	if record.Group.Announce != nil {
		if *record.Group.Announce {
			sendMode = "admins_only"
		} else {
			sendMode = "all_members"
		}
	}
	var preview *string
	if record.Group.Topic != nil && (record.Group.TopicDeleted == nil || !*record.Group.TopicDeleted) {
		value := truncateRunes(*record.Group.Topic, 160)
		preview = &value
	}
	return GroupSummary{
		GroupJID: record.Group.GroupID, Name: record.Group.Name, DescriptionPreview: preview, Type: groupType, State: state,
		MembershipState: membership, MyRole: role, SendMode: sendMode, MemberCount: record.Group.ParticipantCount,
		ParentGroupJID: record.Group.ParentGroupID, IsDefaultSubgroup: record.Group.IsDefaultSubgroup,
		CreatedAt: utcTime(record.Group.ProviderCreatedAt), UpdatedAt: record.Group.UpdatedAt.UTC(),
	}
}

func managementDetail(record projection_repository.GroupManagementRecord, checkedAt time.Time) GroupDetail {
	summary := managementSummary(record)
	var description *string
	if record.Group.Topic != nil && (record.Group.TopicDeleted == nil || !*record.Group.TopicDeleted) {
		description = record.Group.Topic
	}
	detail := GroupDetail{
		GroupSummary: summary, Description: description, AdminCount: record.AdminCount, Announce: record.Group.Announce,
		Locked: record.Group.Locked, JoinApproval: record.Group.JoinApprovalRequired, MemberAddMode: managementMemberAddMode(record.Group.MemberAddMode),
		EphemeralEnabled: record.Group.EphemeralEnabled, EphemeralTimerSeconds: record.Group.EphemeralTimer,
		InviteLink: GroupInviteLinkMetadata{
			Available: record.Group.InviteLink != nil && strings.TrimSpace(*record.Group.InviteLink) != "",
			UpdatedAt: utcTime(record.Group.InviteLinkUpdatedAt),
		},
	}
	if record.OwnerPublicID != nil {
		detail.Owner = &SafeMemberReference{MemberID: *record.OwnerPublicID}
	}
	if record.Group.PictureID != nil || record.Group.PictureMediaAssetID != nil || record.Group.PictureRemoved != nil || record.Group.PictureUpdatedAt != nil {
		var available *bool
		if record.Group.PictureRemoved != nil {
			value := !*record.Group.PictureRemoved
			available = &value
		} else if record.Group.PictureID != nil {
			value := true
			available = &value
		}
		detail.Photo = &GroupPhotoMetadata{Available: available, MediaAssetID: record.Group.PictureMediaAssetID, UpdatedAt: utcTime(record.Group.PictureUpdatedAt)}
	}
	detail.Actions = managementActions(summary, checkedAt)
	return detail
}

func managementMemberAddMode(value *string) string {
	if value == nil {
		return "unknown"
	}
	switch *value {
	case "all_member_add", "all_members":
		return "all_members"
	case "admin_add", "admins_only":
		return "admins_only"
	default:
		return "unknown"
	}
}

func managementGroupType(group projection_model.Group) string {
	if group.IsParent != nil && *group.IsParent {
		return "community"
	}
	if group.ParentGroupID != nil && *group.ParentGroupID != "" {
		return "subgroup"
	}
	if group.IsParent != nil && !*group.IsParent {
		return "group"
	}
	return "unknown"
}

func managementGroupState(group projection_model.Group) string {
	if group.TombstonedAt != nil {
		if group.TombstoneCause != nil && *group.TombstoneCause == projection_model.GroupTombstoneDissolved {
			return "dissolved"
		}
		return "unavailable"
	}
	if group.Suspended == nil {
		return "unknown"
	}
	if *group.Suspended {
		return "suspended"
	}
	return "active"
}

func managementActor(record projection_repository.GroupManagementRecord) (string, string) {
	if record.Group.MembershipState != nil {
		switch *record.Group.MembershipState {
		case projection_model.GroupActorMembershipLeft:
			return "left", "not_member"
		case projection_model.GroupActorMembershipRemoved:
			return "removed", "not_member"
		}
	}
	if record.Group.TombstonedAt != nil {
		return "unknown", "unknown"
	}
	if record.ActorIsOwner {
		return "joined", "owner"
	}
	if record.ActorParticipant == nil {
		if record.Group.MembershipState != nil && *record.Group.MembershipState == projection_model.GroupActorMembershipJoined {
			return "joined", "unknown"
		}
		return "unknown", "unknown"
	}
	if actorIsOwner(record.Group, *record.ActorParticipant) {
		return "joined", "owner"
	}
	switch record.ActorParticipant.Role {
	case projection_model.ParticipantRoleSuperAdmin:
		return "joined", "superadmin"
	case projection_model.ParticipantRoleAdmin:
		return "joined", "admin"
	case projection_model.ParticipantRoleMember:
		return "joined", "member"
	default:
		return "joined", "unknown"
	}
}

func actorIsOwner(group projection_model.Group, actor projection_model.GroupParticipant) bool {
	return stringMatches(group.OwnerJID, actor.ParticipantID, actor.PhoneNumberJID, actor.LID) ||
		stringMatches(group.OwnerPhoneJID, actor.ParticipantID, actor.PhoneNumberJID, actor.LID)
}

func stringMatches(expected *string, values ...any) bool {
	if expected == nil || *expected == "" {
		return false
	}
	for _, value := range values {
		switch typed := value.(type) {
		case string:
			if typed == *expected {
				return true
			}
		case *string:
			if typed != nil && *typed == *expected {
				return true
			}
		}
	}
	return false
}

func managementActions(summary GroupSummary, checkedAt time.Time) GroupActions {
	admin := adminDecision(summary, checkedAt)
	send := sendDecision(summary, checkedAt)
	leave := leaveDecision(summary, checkedAt)
	return GroupActions{
		SendMessage: send, EditName: admin, EditDescription: admin, EditSettings: admin, AddMembers: admin,
		RemoveMembers: admin, PromoteMembers: admin, DemoteMembers: admin, ReadInviteLink: admin,
		ResetInviteLink: admin, SetPhoto: admin, LeaveGroup: leave,
	}
}

func memberActions(summary GroupSummary, targetRole string, isActor bool, checkedAt time.Time) GroupMemberActions {
	base := adminDecision(summary, checkedAt)
	if base.State != "allowed" {
		return GroupMemberActions{Promote: base, Demote: base, Remove: base}
	}
	if isActor {
		denied := decision("denied", "self_action_not_allowed", checkedAt)
		return GroupMemberActions{Promote: denied, Demote: denied, Remove: denied}
	}
	protected := targetRole == "owner" || targetRole == "superadmin"
	var promote, demote, remove ActionDecision
	switch targetRole {
	case "member":
		promote = decision("allowed", "", checkedAt)
		demote = decision("denied", "not_an_admin", checkedAt)
		remove = decision("allowed", "", checkedAt)
	case "admin":
		promote = decision("denied", "already_admin", checkedAt)
		if summary.MyRole == "owner" || summary.MyRole == "superadmin" {
			demote = decision("allowed", "", checkedAt)
			remove = decision("allowed", "", checkedAt)
		} else {
			demote = decision("unknown", "permission_unknown", checkedAt)
			remove = decision("unknown", "permission_unknown", checkedAt)
		}
	case "owner", "superadmin":
		promote = decision("denied", "already_admin", checkedAt)
		demote = decision("denied", "protected_member", checkedAt)
		remove = decision("denied", "protected_member", checkedAt)
	default:
		unknown := decision("unknown", "permission_unknown", checkedAt)
		return GroupMemberActions{Promote: unknown, Demote: unknown, Remove: unknown}
	}
	if protected {
		demote = decision("denied", "protected_member", checkedAt)
		remove = decision("denied", "protected_member", checkedAt)
	}
	return GroupMemberActions{Promote: promote, Demote: demote, Remove: remove}
}

func adminDecision(summary GroupSummary, checkedAt time.Time) ActionDecision {
	if decision := operationalDecision(summary, checkedAt); decision.State != "allowed" {
		return decision
	}
	switch summary.MyRole {
	case "owner", "superadmin", "admin":
		return decision("allowed", "", checkedAt)
	case "member":
		return decision("denied", "admin_required", checkedAt)
	default:
		return decision("unknown", "permission_unknown", checkedAt)
	}
}

func sendDecision(summary GroupSummary, checkedAt time.Time) ActionDecision {
	if base := operationalDecision(summary, checkedAt); base.State != "allowed" {
		return base
	}
	if summary.Type == "community" {
		return decision("unknown", "unsupported", checkedAt)
	}
	if summary.SendMode == "all_members" {
		return decision("allowed", "", checkedAt)
	}
	if summary.SendMode == "admins_only" {
		return adminDecision(summary, checkedAt)
	}
	return decision("unknown", "permission_unknown", checkedAt)
}

func leaveDecision(summary GroupSummary, checkedAt time.Time) ActionDecision {
	if summary.MembershipState == "left" || summary.MembershipState == "removed" {
		return decision("denied", "not_a_member", checkedAt)
	}
	if summary.MembershipState == "unknown" {
		return decision("unknown", "permission_unknown", checkedAt)
	}
	if summary.State == "unavailable" || summary.State == "dissolved" {
		return decision("denied", "group_unavailable", checkedAt)
	}
	return decision("allowed", "", checkedAt)
}

func operationalDecision(summary GroupSummary, checkedAt time.Time) ActionDecision {
	if summary.MembershipState == "left" || summary.MembershipState == "removed" {
		return decision("denied", "not_a_member", checkedAt)
	}
	if summary.MembershipState == "unknown" {
		return decision("unknown", "permission_unknown", checkedAt)
	}
	if summary.Type == "unknown" {
		return decision("unknown", "permission_unknown", checkedAt)
	}
	switch summary.State {
	case "active":
		return decision("allowed", "", checkedAt)
	case "suspended":
		return decision("denied", "group_suspended", checkedAt)
	case "unavailable", "dissolved":
		return decision("denied", "group_unavailable", checkedAt)
	default:
		return decision("unknown", "permission_unknown", checkedAt)
	}
}

func decision(state, reason string, checkedAt time.Time) ActionDecision {
	var pointer *string
	if reason != "" {
		pointer = &reason
	}
	return ActionDecision{State: state, Reason: pointer, CheckedAt: checkedAt}
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func utcTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := value.UTC()
	return &result
}
