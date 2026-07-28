package group_service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type managementReadRepositoryStub struct {
	page               *projection_repository.GroupManagementPage
	record             *projection_repository.GroupManagementRecord
	recordErr          error
	filter             projection_repository.GroupManagementFilter
	cursor             *projection_repository.GroupCursor
	identity           string
	searchErr          error
	summary            *projection_repository.GroupManagementSummary
	summaryErr         error
	summaryInstanceID  string
	memberGroup        *projection_repository.GroupManagementRecord
	memberPage         *projection_repository.GroupMemberPage
	memberFilter       projection_repository.GroupMemberFilter
	memberCursor       *projection_repository.GroupMemberCursor
	memberGroupID      string
	memberErr          error
	commandMemberGroup *projection_repository.GroupManagementRecord
	commandMember      *projection_repository.GroupMemberRecord
	commandMemberErr   error
}

func (s *managementReadRepositoryStub) SearchManagement(_ context.Context, _, identity string, filter projection_repository.GroupManagementFilter, _ int, cursor *projection_repository.GroupCursor) (*projection_repository.GroupManagementPage, error) {
	s.identity, s.filter, s.cursor = identity, filter, cursor
	return s.page, s.searchErr
}

func (s *managementReadRepositoryStub) GetManagementSummary(_ context.Context, instanceID string) (*projection_repository.GroupManagementSummary, error) {
	s.summaryInstanceID = instanceID
	return s.summary, s.summaryErr
}

func (s *managementReadRepositoryStub) GetManagement(context.Context, string, string, string) (*projection_repository.GroupManagementRecord, error) {
	if s.recordErr != nil {
		return nil, s.recordErr
	}
	if s.record == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return s.record, nil
}

func (s *managementReadRepositoryStub) ListManagementMembers(_ context.Context, _, _, groupID string, filter projection_repository.GroupMemberFilter, _ int, cursor *projection_repository.GroupMemberCursor) (*projection_repository.GroupManagementRecord, *projection_repository.GroupMemberPage, error) {
	s.memberGroupID, s.memberFilter, s.memberCursor = groupID, filter, cursor
	return s.memberGroup, s.memberPage, s.memberErr
}

func (s *managementReadRepositoryStub) GetManagementMember(context.Context, string, string, string, string) (*projection_repository.GroupManagementRecord, *projection_repository.GroupMemberRecord, error) {
	if s.commandMemberErr != nil {
		return nil, nil, s.commandMemberErr
	}
	if s.commandMember == nil {
		return nil, nil, gorm.ErrRecordNotFound
	}
	return s.commandMemberGroup, s.commandMember, nil
}

type managementReadStateStub struct {
	state *projection_model.State
	err   error
}

func (s managementReadStateStub) GetServingState(string, string) (*projection_model.State, error) {
	return s.state, s.err
}

func readyManagementState() *projection_model.State {
	now := time.Unix(100, 0).UTC()
	return &projection_model.State{SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.GroupsProjectionSchemaVersion, LastReconciledAt: &now}
}

func TestManagementReaderReturnsNormalizedSummaryWithoutParticipants(t *testing.T) {
	name, topic := "Branches", "A description"
	suspended, announce, isParent := false, true, false
	participantCount := 42
	now := time.Unix(200, 0).UTC()
	repository := &managementReadRepositoryStub{page: &projection_repository.GroupManagementPage{Items: []projection_repository.GroupManagementRecord{{
		Group:            projection_model.Group{GroupID: "120363000001@g.us", Name: &name, Topic: &topic, Suspended: &suspended, Announce: &announce, IsParent: &isParent, ParticipantCount: &participantCount, UpdatedAt: now},
		ActorParticipant: &projection_model.GroupParticipant{ParticipantID: "actor@lid", Role: projection_model.ParticipantRoleAdmin},
	}}, NextCursor: &projection_repository.GroupCursor{GroupID: "120363000001@g.us"}}}
	reader := NewManagementReader(repository, managementReadStateStub{state: readyManagementState()})
	items, meta, err := reader.Search(context.Background(), &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"}, GroupManagementFilters{Type: "group", MyRole: "admin"}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if repository.identity != "actor@s.whatsapp.net" || repository.filter.Type != "group" || repository.filter.MyRole != "admin" {
		t.Fatalf("repository input = %#v identity=%s", repository.filter, repository.identity)
	}
	if len(items) != 1 || items[0].MyRole != "admin" || items[0].MembershipState != "joined" || items[0].State != "active" || items[0].SendMode != "admins_only" || items[0].MemberCount == nil || *items[0].MemberCount != 42 {
		t.Fatalf("summary = %#v", items)
	}
	if meta.NextCursor == "" {
		t.Fatal("next cursor was not encoded")
	}
}

func TestManagementReaderReturnsAuthoritativeInstanceSummary(t *testing.T) {
	instanceID := uuid.NewString()
	repository := &managementReadRepositoryStub{summary: &projection_repository.GroupManagementSummary{
		Total: 12, Active: 8, Suspended: 1, Communities: 2, Subgroups: 4, AdminsOnlySend: 3,
	}}
	reader := NewManagementReader(repository, managementReadStateStub{state: readyManagementState()})
	summary, meta, err := reader.Summary(context.Background(), &instance_model.Instance{Id: instanceID})
	if err != nil {
		t.Fatal(err)
	}
	if repository.summaryInstanceID != instanceID || summary.Total != 12 || summary.Active != 8 || summary.Suspended != 1 || summary.Communities != 2 ||
		summary.Subgroups != 4 || summary.AdminsOnlySend != 3 || !summary.UpdatedAt.Equal(time.Unix(100, 0).UTC()) || meta.Source != "projection" {
		t.Fatalf("summary=%#v meta=%#v repository=%#v", summary, meta, repository)
	}
}

func TestManagementSummaryFailsClosedBeforeProjectionIsServing(t *testing.T) {
	repository := &managementReadRepositoryStub{summary: &projection_repository.GroupManagementSummary{Total: 99}}
	reader := NewManagementReader(repository, managementReadStateStub{err: gorm.ErrRecordNotFound})
	_, _, err := reader.Summary(context.Background(), &instance_model.Instance{Id: uuid.NewString()})
	if !errors.Is(err, projection_service.ErrGroupsProjectionNotReady) || repository.summaryInstanceID != "" {
		t.Fatalf("error=%v repository=%#v", err, repository)
	}
}

func TestManagementCursorIsBoundToInstanceAndFilters(t *testing.T) {
	repository := &managementReadRepositoryStub{page: &projection_repository.GroupManagementPage{Items: []projection_repository.GroupManagementRecord{}, NextCursor: &projection_repository.GroupCursor{GroupID: "120363000001@g.us"}}}
	reader := NewManagementReader(repository, managementReadStateStub{state: readyManagementState()})
	instance := &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"}
	_, meta, err := reader.Search(context.Background(), instance, GroupManagementFilters{State: "active"}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reader.Search(context.Background(), instance, GroupManagementFilters{State: "suspended"}, 50, meta.NextCursor); !errors.Is(err, ErrInvalidManagementCursor) {
		t.Fatalf("filter-scoped cursor error = %v", err)
	}
	other := &instance_model.Instance{Id: "instance-b", Jid: "actor@s.whatsapp.net"}
	if _, _, err := reader.Search(context.Background(), other, GroupManagementFilters{State: "active"}, 50, meta.NextCursor); !errors.Is(err, ErrInvalidManagementCursor) {
		t.Fatalf("instance-scoped cursor error = %v", err)
	}
}

func TestManagementDetailUsesTriStateDecisions(t *testing.T) {
	announce := false
	mediaAssetID := uuid.NewString()
	now := time.Unix(300, 0).UTC()
	repository := &managementReadRepositoryStub{record: &projection_repository.GroupManagementRecord{Group: projection_model.Group{
		GroupID: "120363000001@g.us", Announce: &announce, PictureMediaAssetID: &mediaAssetID, UpdatedAt: now,
	}}}
	reader := NewManagementReader(repository, managementReadStateStub{state: readyManagementState()})
	reader.now = func() time.Time { return now }
	detail, _, err := reader.Get(context.Background(), &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"}, "120363000001@g.us")
	if err != nil {
		t.Fatal(err)
	}
	if detail.MyRole != "unknown" || detail.MembershipState != "unknown" || detail.State != "unknown" {
		t.Fatalf("unknown facts were fabricated: %#v", detail.GroupSummary)
	}
	if detail.Actions.SendMessage.State != "unknown" || detail.Actions.SendMessage.Reason == nil || *detail.Actions.SendMessage.Reason != "permission_unknown" || detail.Actions.EditName.State != "unknown" {
		t.Fatalf("actions = %#v", detail.Actions)
	}
	if detail.Photo == nil || detail.Photo.MediaAssetID == nil || *detail.Photo.MediaAssetID != mediaAssetID {
		t.Fatalf("photo = %#v", detail.Photo)
	}
}

func TestManagementDetailSeparatesInvitePermissionFromCacheAvailability(t *testing.T) {
	suspended, announce, isParent := false, false, false
	membership := projection_model.GroupActorMembershipJoined
	now := time.Unix(300, 0).UTC()
	repository := &managementReadRepositoryStub{record: &projection_repository.GroupManagementRecord{
		Group: projection_model.Group{
			GroupID: "120363000001@g.us", Suspended: &suspended, Announce: &announce, IsParent: &isParent,
			MembershipState: &membership, UpdatedAt: now,
		},
		ActorParticipant: &projection_model.GroupParticipant{ParticipantID: "actor@s.whatsapp.net", Role: projection_model.ParticipantRoleAdmin},
	}}
	reader := NewManagementReader(repository, managementReadStateStub{state: readyManagementState()})
	reader.now = func() time.Time { return now }
	detail, _, err := reader.Get(context.Background(), &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"}, "120363000001@g.us")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Actions.ReadInviteLink.State != "allowed" || detail.Actions.ResetInviteLink.State != "allowed" || detail.InviteLink.Available || detail.InviteLink.UpdatedAt != nil {
		t.Fatalf("detail = %#v", detail)
	}
	link, available, decision, meta, err := reader.InviteLink(context.Background(), &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"}, "120363000001@g.us")
	if err != nil || link != "" || available || decision.State != "allowed" || meta == nil {
		t.Fatalf("invite link = %q available=%v decision=%#v meta=%#v error=%v", link, available, decision, meta, err)
	}

	cachedAt := now.Add(time.Minute)
	cachedLink := "https://chat.whatsapp.com/cached"
	repository.record.Group.InviteLink = &cachedLink
	repository.record.Group.InviteLinkUpdatedAt = &cachedAt
	link, available, decision, _, err = reader.InviteLink(context.Background(), &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"}, "120363000001@g.us")
	if err != nil || link != cachedLink || !available || decision.State != "allowed" {
		t.Fatalf("cached invite link = %q available=%v decision=%#v error=%v", link, available, decision, err)
	}
}

func TestManagementReaderFailsClosedForUnreadyProjection(t *testing.T) {
	reader := NewManagementReader(&managementReadRepositoryStub{}, managementReadStateStub{err: gorm.ErrRecordNotFound})
	_, _, err := reader.Search(context.Background(), &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"}, GroupManagementFilters{}, 50, "")
	if !errors.Is(err, projection_service.ErrGroupsProjectionNotReady) {
		t.Fatalf("error = %v", err)
	}
}

func TestManagementReaderRejectsInvalidAndOversizedFilters(t *testing.T) {
	reader := NewManagementReader(&managementReadRepositoryStub{}, managementReadStateStub{state: readyManagementState()})
	instance := &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"}
	for _, filters := range []GroupManagementFilters{{Type: "broadcast"}, {MyRole: "root"}, {Query: strings.Repeat("界", 129)}} {
		if _, _, err := reader.Search(context.Background(), instance, filters, 50, ""); !errors.Is(err, ErrInvalidManagementFilter) {
			t.Fatalf("filters=%#v error=%v", filters, err)
		}
	}
}

func TestManagementReaderProjectionServingStates(t *testing.T) {
	reconciledAt := time.Unix(100, 0).UTC()
	cases := []struct {
		name  string
		state projection_model.State
		ready bool
	}{
		{name: "ready", state: projection_model.State{SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt}, ready: true},
		{name: "stale with prior reconciliation", state: projection_model.State{SyncStatus: projection_model.SyncStatusStale, SchemaVersion: projection_service.GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt}, ready: true},
		{name: "not started", state: projection_model.State{SyncStatus: projection_model.SyncStatusNotStarted, SchemaVersion: projection_service.GroupsProjectionSchemaVersion}},
		{name: "failed", state: projection_model.State{SyncStatus: projection_model.SyncStatusFailed, SchemaVersion: projection_service.GroupsProjectionSchemaVersion, LastReconciledAt: &reconciledAt}},
		{name: "schema old", state: projection_model.State{SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.GroupsProjectionSchemaVersion - 1, LastReconciledAt: &reconciledAt}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			repository := &managementReadRepositoryStub{page: &projection_repository.GroupManagementPage{Items: []projection_repository.GroupManagementRecord{}}}
			reader := NewManagementReader(repository, managementReadStateStub{state: &test.state})
			_, _, err := reader.Search(context.Background(), &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"}, GroupManagementFilters{}, 50, "")
			if test.ready && err != nil {
				t.Fatalf("serving state rejected: %v", err)
			}
			if !test.ready && !errors.Is(err, projection_service.ErrGroupsProjectionNotReady) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestManagementActionDecisionsRespectRoleSendModeAndState(t *testing.T) {
	checkedAt := time.Unix(400, 0).UTC()
	cases := []struct {
		name       string
		summary    GroupSummary
		sendState  string
		adminState string
		reason     string
	}{
		{name: "member may send in all-members group", summary: GroupSummary{State: "active", MembershipState: "joined", MyRole: "member", SendMode: "all_members"}, sendState: "allowed", adminState: "denied", reason: "admin_required"},
		{name: "member may not send in admins-only group", summary: GroupSummary{State: "active", MembershipState: "joined", MyRole: "member", SendMode: "admins_only"}, sendState: "denied", adminState: "denied", reason: "admin_required"},
		{name: "admin may manage active group", summary: GroupSummary{State: "active", MembershipState: "joined", MyRole: "admin", SendMode: "admins_only"}, sendState: "allowed", adminState: "allowed"},
		{name: "community send support is not fabricated", summary: GroupSummary{Type: "community", State: "active", MembershipState: "joined", MyRole: "admin", SendMode: "admins_only"}, sendState: "unknown", adminState: "allowed", reason: "unsupported"},
		{name: "unknown group type keeps permissions unknown", summary: GroupSummary{Type: "unknown", State: "active", MembershipState: "joined", MyRole: "admin", SendMode: "all_members"}, sendState: "unknown", adminState: "unknown", reason: "permission_unknown"},
		{name: "suspension denies management", summary: GroupSummary{State: "suspended", MembershipState: "joined", MyRole: "admin", SendMode: "all_members"}, sendState: "denied", adminState: "denied", reason: "group_suspended"},
		{name: "unavailable group denies management", summary: GroupSummary{State: "unavailable", MembershipState: "joined", MyRole: "admin", SendMode: "all_members"}, sendState: "denied", adminState: "denied", reason: "group_unavailable"},
		{name: "dissolved group denies management", summary: GroupSummary{State: "dissolved", MembershipState: "joined", MyRole: "admin", SendMode: "all_members"}, sendState: "denied", adminState: "denied", reason: "group_unavailable"},
		{name: "left actor is not a member", summary: GroupSummary{State: "active", MembershipState: "left", MyRole: "not_member", SendMode: "all_members"}, sendState: "denied", adminState: "denied", reason: "not_a_member"},
		{name: "removed actor is not a member", summary: GroupSummary{State: "active", MembershipState: "removed", MyRole: "not_member", SendMode: "all_members"}, sendState: "denied", adminState: "denied", reason: "not_a_member"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			actions := managementActions(test.summary, checkedAt)
			if actions.SendMessage.State != test.sendState || actions.EditName.State != test.adminState ||
				actions.ReadInviteLink.State != test.adminState || actions.ResetInviteLink.State != test.adminState {
				t.Fatalf("actions = %#v", actions)
			}
			if test.reason != "" {
				var reason *string
				if actions.SendMessage.State != "allowed" {
					reason = actions.SendMessage.Reason
				} else {
					reason = actions.EditName.Reason
				}
				if reason == nil || *reason != test.reason {
					t.Fatalf("reason = %#v, actions=%#v", reason, actions)
				}
			}
		})
	}
}

func TestManagementActorUsesAllProjectedAliasesForOwner(t *testing.T) {
	owner := "owner@s.whatsapp.net"
	phone := "owner@s.whatsapp.net"
	membership, role := managementActor(projection_repository.GroupManagementRecord{
		Group:            projection_model.Group{OwnerJID: &owner},
		ActorParticipant: &projection_model.GroupParticipant{ParticipantID: "owner@lid", PhoneNumberJID: &phone, Role: projection_model.ParticipantRoleAdmin},
	})
	if membership != "joined" || role != "owner" {
		t.Fatalf("membership=%s role=%s", membership, role)
	}
}

func TestManagementActorDoesNotReuseStaleRoleAfterAccessLoss(t *testing.T) {
	owner := "owner@s.whatsapp.net"
	tombstonedAt := time.Unix(500, 0).UTC()
	membership, role := managementActor(projection_repository.GroupManagementRecord{
		Group: projection_model.Group{OwnerJID: &owner, TombstonedAt: &tombstonedAt}, ActorIsOwner: true,
		ActorParticipant: &projection_model.GroupParticipant{ParticipantID: owner, Role: projection_model.ParticipantRoleSuperAdmin},
	})
	if membership != "unknown" || role != "unknown" {
		t.Fatalf("membership=%s role=%s", membership, role)
	}
}

func TestManagementMembersReturnsOpaqueReferencesAndTargetDecisions(t *testing.T) {
	suspended, announce, isParent := false, false, false
	membership := projection_model.GroupActorMembershipJoined
	memberID, nextID := uuid.NewString(), uuid.NewString()
	displayName := "Alice"
	repository := &managementReadRepositoryStub{
		memberGroup: &projection_repository.GroupManagementRecord{
			Group:        projection_model.Group{GroupID: "123@g.us", Suspended: &suspended, Announce: &announce, IsParent: &isParent, MembershipState: &membership, UpdatedAt: time.Unix(1, 0)},
			ActorIsOwner: true,
		},
		memberPage: &projection_repository.GroupMemberPage{
			Items: []projection_repository.GroupMemberRecord{{
				Participant: projection_model.GroupParticipant{PublicID: memberID, DisplayName: &displayName, Role: projection_model.ParticipantRoleMember}, Role: "member",
			}},
			NextCursor: &projection_repository.GroupMemberCursor{SortKey: "alice", PublicID: nextID},
		},
	}
	reader := NewManagementReader(repository, managementReadStateStub{state: readyManagementState()})
	items, meta, err := reader.Members(context.Background(), &instance_model.Instance{Id: "instance-a", Jid: "owner@s.whatsapp.net"}, "123@g.us", GroupMemberFilters{Query: "Ali", Role: "member"}, 50, "")
	if err != nil {
		t.Fatal(err)
	}
	if repository.memberGroupID != "123@g.us" || repository.memberFilter.Term != "ali" || repository.memberFilter.Role != "member" || len(items) != 1 || items[0].MemberID != memberID || items[0].DisplayName == nil || *items[0].DisplayName != displayName || items[0].Role != "member" || items[0].MembershipState != "active" {
		t.Fatalf("repository=%#v items=%#v", repository, items)
	}
	if items[0].Actions.Promote.State != "allowed" || items[0].Actions.Remove.State != "allowed" || items[0].Actions.Demote.State != "denied" || items[0].Actions.Demote.Reason == nil || *items[0].Actions.Demote.Reason != "not_an_admin" || meta.NextCursor == "" {
		t.Fatalf("actions=%#v meta=%#v", items[0].Actions, meta)
	}
	if _, _, err := reader.Members(context.Background(), &instance_model.Instance{Id: "instance-a", Jid: "owner@s.whatsapp.net"}, "123@g.us", GroupMemberFilters{Role: "admin"}, 50, meta.NextCursor); !errors.Is(err, ErrInvalidManagementCursor) {
		t.Fatalf("filter-scoped cursor error = %v", err)
	}
	if _, _, err := reader.Members(context.Background(), &instance_model.Instance{Id: "instance-a", Jid: "owner@s.whatsapp.net"}, "456@g.us", GroupMemberFilters{Query: "Ali", Role: "member"}, 50, meta.NextCursor); !errors.Is(err, ErrInvalidManagementCursor) {
		t.Fatalf("group-scoped cursor error = %v", err)
	}
}

func TestManagementMemberDecisionsProtectSelfAndPrivilegedTargets(t *testing.T) {
	checkedAt := time.Unix(600, 0).UTC()
	summary := GroupSummary{Type: "group", State: "active", MembershipState: "joined", MyRole: "admin", SendMode: "all_members"}
	self := memberActions(summary, "admin", true, checkedAt)
	if self.Remove.State != "denied" || self.Remove.Reason == nil || *self.Remove.Reason != "self_action_not_allowed" {
		t.Fatalf("self actions = %#v", self)
	}
	protected := memberActions(summary, "superadmin", false, checkedAt)
	if protected.Demote.State != "denied" || protected.Remove.State != "denied" || protected.Remove.Reason == nil || *protected.Remove.Reason != "protected_member" {
		t.Fatalf("protected actions = %#v", protected)
	}
	peer := memberActions(summary, "admin", false, checkedAt)
	if peer.Demote.State != "unknown" || peer.Remove.State != "unknown" {
		t.Fatalf("peer admin actions = %#v", peer)
	}
}

func TestManagementMembersRejectsUnavailableGroup(t *testing.T) {
	tombstonedAt := time.Now().UTC()
	repository := &managementReadRepositoryStub{
		memberGroup: &projection_repository.GroupManagementRecord{Group: projection_model.Group{GroupID: "123@g.us", TombstonedAt: &tombstonedAt}},
		memberPage:  &projection_repository.GroupMemberPage{Items: []projection_repository.GroupMemberRecord{}},
	}
	reader := NewManagementReader(repository, managementReadStateStub{state: readyManagementState()})
	_, _, err := reader.Members(context.Background(), &instance_model.Instance{Id: "instance-a", Jid: "actor@s.whatsapp.net"}, "123@g.us", GroupMemberFilters{}, 50, "")
	if !errors.Is(err, ErrManagementGroupUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestManagementMemberCursorSupportsMissingDisplayName(t *testing.T) {
	scope := memberCursorScope("instance-a", "123@g.us", GroupMemberFilters{})
	publicID := uuid.NewString()
	encoded, err := encodeMemberCursor(&projection_repository.GroupMemberCursor{SortKey: "", PublicID: publicID}, scope)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeMemberCursor(encoded, scope)
	if err != nil || decoded.SortKey != "" || decoded.PublicID != publicID {
		t.Fatalf("decoded=%#v error=%v", decoded, err)
	}
}
