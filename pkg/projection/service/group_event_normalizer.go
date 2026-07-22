package projection_service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

const groupResource = "groups"

type groupEventPayload struct {
	GroupID                    string                    `json:"groupId"`
	Owner                      string                    `json:"owner,omitempty"`
	OwnerPN                    string                    `json:"ownerPn,omitempty"`
	Notify                     string                    `json:"notify,omitempty"`
	Sender                     string                    `json:"sender,omitempty"`
	SenderPN                   string                    `json:"senderPn,omitempty"`
	Name                       *groupNamePayload         `json:"name,omitempty"`
	Topic                      *groupTopicPayload        `json:"topic,omitempty"`
	Locked                     *bool                     `json:"locked,omitempty"`
	Announce                   *groupAnnouncePayload     `json:"announce,omitempty"`
	Ephemeral                  *groupEphemeralPayload    `json:"ephemeral,omitempty"`
	JoinApprovalRequired       *bool                     `json:"joinApprovalRequired,omitempty"`
	Deleted                    *groupDeletePayload       `json:"deleted,omitempty"`
	Link                       *groupLinkPayload         `json:"link,omitempty"`
	Unlink                     *groupLinkPayload         `json:"unlink,omitempty"`
	NewInviteLink              *string                   `json:"newInviteLink,omitempty"`
	PreviousParticipantVersion string                    `json:"previousParticipantVersion,omitempty"`
	ParticipantVersion         string                    `json:"participantVersion,omitempty"`
	JoinReason                 string                    `json:"joinReason,omitempty"`
	JoinedParticipants         []string                  `json:"joinedParticipants,omitempty"`
	LeftParticipants           []string                  `json:"leftParticipants,omitempty"`
	PromotedParticipants       []string                  `json:"promotedParticipants,omitempty"`
	DemotedParticipants        []string                  `json:"demotedParticipants,omitempty"`
	Suspended                  bool                      `json:"suspended,omitempty"`
	Unsuspended                bool                      `json:"unsuspended,omitempty"`
	Joined                     *joinedGroupPayload       `json:"joined,omitempty"`
	CreatedAt                  time.Time                 `json:"createdAt,omitempty"`
	AddressingMode             string                    `json:"addressingMode,omitempty"`
	CreatorCountryCode         string                    `json:"creatorCountryCode,omitempty"`
	Participants               []groupParticipantPayload `json:"participants,omitempty"`
	ParticipantCount           int                       `json:"participantCount,omitempty"`
	IsParent                   bool                      `json:"isParent,omitempty"`
	DefaultMembershipApproval  string                    `json:"defaultMembershipApproval,omitempty"`
	LinkedParentID             string                    `json:"linkedParentId,omitempty"`
	IsDefaultSubgroup          bool                      `json:"isDefaultSubgroup,omitempty"`
	MemberAddMode              string                    `json:"memberAddMode,omitempty"`
}

type groupNamePayload struct {
	Name    string    `json:"name"`
	SetAt   time.Time `json:"setAt"`
	SetBy   string    `json:"setBy,omitempty"`
	SetByPN string    `json:"setByPn,omitempty"`
}

type groupTopicPayload struct {
	Topic   string    `json:"topic"`
	ID      string    `json:"id,omitempty"`
	SetAt   time.Time `json:"setAt"`
	SetBy   string    `json:"setBy,omitempty"`
	SetByPN string    `json:"setByPn,omitempty"`
	Deleted bool      `json:"deleted,omitempty"`
}

type groupAnnouncePayload struct {
	Enabled   bool   `json:"enabled"`
	VersionID string `json:"versionId,omitempty"`
}

type groupEphemeralPayload struct {
	Enabled bool   `json:"enabled"`
	Timer   uint32 `json:"timer"`
}

type groupDeletePayload struct {
	Deleted bool   `json:"deleted"`
	Reason  string `json:"reason,omitempty"`
}

type groupLinkPayload struct {
	Type              string `json:"type,omitempty"`
	UnlinkReason      string `json:"unlinkReason,omitempty"`
	GroupID           string `json:"groupId,omitempty"`
	GroupName         string `json:"groupName,omitempty"`
	IsDefaultSubgroup bool   `json:"isDefaultSubgroup,omitempty"`
}

type joinedGroupPayload struct {
	Reason    string `json:"reason,omitempty"`
	Type      string `json:"type,omitempty"`
	CreateKey string `json:"createKey,omitempty"`
}

type groupParticipantPayload struct {
	ID          string `json:"id"`
	PhoneNumber string `json:"phoneNumber,omitempty"`
	LID         string `json:"lid,omitempty"`
	Admin       bool   `json:"admin,omitempty"`
	SuperAdmin  bool   `json:"superAdmin,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	ErrorCode   int    `json:"errorCode,omitempty"`
}

// NormalizeGroupEvent converts projection-relevant whatsmeow group events into
// a bounded provider-independent envelope. Unknown events are intentionally
// ignored so new whatsmeow event types cannot enter the inbox accidentally.
func NormalizeGroupEvent(instanceID string, rawEvent any) (*projection_model.Event, bool, error) {
	eventType := ""
	var entityKey string
	var occurredAt time.Time
	var payload groupEventPayload
	switch event := rawEvent.(type) {
	case *events.GroupInfo:
		if event == nil || event.JID.IsEmpty() {
			return nil, true, errors.New("group projection event has no group identity")
		}
		eventType = "group_info"
		entityKey = event.JID.String()
		occurredAt = event.Timestamp.UTC()
		payload = normalizeGroupInfo(event)
	case *events.JoinedGroup:
		if event == nil || event.JID.IsEmpty() {
			return nil, true, errors.New("group projection event has no group identity")
		}
		eventType = "joined_group"
		entityKey = event.JID.String()
		occurredAt = event.GroupCreated.UTC()
		payload = normalizeJoinedGroup(event)
	default:
		return nil, false, nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, true, err
	}
	if occurredAt.IsZero() {
		occurredAt = time.Unix(0, 0).UTC()
	}
	keyMaterial := eventType + "\x00" + entityKey + "\x00" + occurredAt.Format(time.RFC3339Nano) + "\x00" + string(encoded)
	sum := sha256.Sum256([]byte(keyMaterial))
	return &projection_model.Event{
		InstanceID: instanceID, Resource: groupResource, EventKey: hex.EncodeToString(sum[:]),
		EntityKey: entityKey, EventType: eventType, OccurredAt: occurredAt, Payload: encoded,
	}, true, nil
}

func normalizeJoinedGroup(event *events.JoinedGroup) groupEventPayload {
	info := &event.GroupInfo
	locked := info.IsLocked
	joinApproval := info.IsJoinApprovalRequired
	payload := groupEventPayload{
		GroupID: info.JID.String(), Owner: info.OwnerJID.String(), OwnerPN: info.OwnerPN.String(),
		Name:   &groupNamePayload{Name: info.Name, SetAt: info.NameSetAt.UTC(), SetBy: info.NameSetBy.String(), SetByPN: info.NameSetByPN.String()},
		Topic:  &groupTopicPayload{Topic: info.Topic, ID: info.TopicID, SetAt: info.TopicSetAt.UTC(), SetBy: info.TopicSetBy.String(), SetByPN: info.TopicSetByPN.String(), Deleted: info.TopicDeleted},
		Locked: &locked, Announce: &groupAnnouncePayload{Enabled: info.IsAnnounce, VersionID: info.AnnounceVersionID},
		Ephemeral: &groupEphemeralPayload{Enabled: info.IsEphemeral, Timer: info.DisappearingTimer}, JoinApprovalRequired: &joinApproval,
		ParticipantVersion: info.ParticipantVersionID, Suspended: info.Suspended,
		Joined:    &joinedGroupPayload{Reason: event.Reason, Type: event.Type, CreateKey: event.CreateKey},
		CreatedAt: info.GroupCreated.UTC(), AddressingMode: string(info.AddressingMode), CreatorCountryCode: info.CreatorCountryCode,
		Participants: normalizeParticipants(info.Participants), ParticipantCount: info.ParticipantCount,
		IsParent: info.IsParent, DefaultMembershipApproval: info.DefaultMembershipApprovalMode,
		LinkedParentID: info.LinkedParentJID.String(), IsDefaultSubgroup: info.IsDefaultSubGroup, MemberAddMode: string(info.MemberAddMode),
	}
	return payload
}

func normalizeGroupInfo(info *events.GroupInfo) groupEventPayload {
	payload := groupEventPayload{
		GroupID: info.JID.String(), Notify: info.Notify, Sender: jidString(info.Sender), SenderPN: jidString(info.SenderPN),
		NewInviteLink: info.NewInviteLink, PreviousParticipantVersion: info.PrevParticipantVersionID,
		ParticipantVersion: info.ParticipantVersionID, JoinReason: info.JoinReason,
		JoinedParticipants: jidsToStrings(info.Join), LeftParticipants: jidsToStrings(info.Leave),
		PromotedParticipants: jidsToStrings(info.Promote), DemotedParticipants: jidsToStrings(info.Demote),
		Suspended: info.Suspended, Unsuspended: info.Unsuspended,
	}
	if info.Name != nil {
		payload.Name = &groupNamePayload{Name: info.Name.Name, SetAt: info.Name.NameSetAt.UTC(), SetBy: info.Name.NameSetBy.String(), SetByPN: info.Name.NameSetByPN.String()}
	}
	if info.Topic != nil {
		payload.Topic = &groupTopicPayload{Topic: info.Topic.Topic, ID: info.Topic.TopicID, SetAt: info.Topic.TopicSetAt.UTC(), SetBy: info.Topic.TopicSetBy.String(), SetByPN: info.Topic.TopicSetByPN.String(), Deleted: info.Topic.TopicDeleted}
	}
	if info.Locked != nil {
		value := info.Locked.IsLocked
		payload.Locked = &value
	}
	if info.Announce != nil {
		payload.Announce = &groupAnnouncePayload{Enabled: info.Announce.IsAnnounce, VersionID: info.Announce.AnnounceVersionID}
	}
	if info.Ephemeral != nil {
		payload.Ephemeral = &groupEphemeralPayload{Enabled: info.Ephemeral.IsEphemeral, Timer: info.Ephemeral.DisappearingTimer}
	}
	if info.MembershipApprovalMode != nil {
		value := info.MembershipApprovalMode.IsJoinApprovalRequired
		payload.JoinApprovalRequired = &value
	}
	if info.Delete != nil {
		payload.Deleted = &groupDeletePayload{Deleted: info.Delete.Deleted, Reason: info.Delete.DeleteReason}
	}
	payload.Link = normalizeGroupLink(info.Link)
	payload.Unlink = normalizeGroupLink(info.Unlink)
	return payload
}

func normalizeGroupLink(change *types.GroupLinkChange) *groupLinkPayload {
	if change == nil {
		return nil
	}
	return &groupLinkPayload{
		Type: string(change.Type), UnlinkReason: string(change.UnlinkReason), GroupID: change.Group.JID.String(),
		GroupName: change.Group.Name, IsDefaultSubgroup: change.Group.IsDefaultSubGroup,
	}
}

func jidString(jid *types.JID) string {
	if jid == nil {
		return ""
	}
	return jid.String()
}

func jidsToStrings(jids []types.JID) []string {
	if len(jids) == 0 {
		return nil
	}
	result := make([]string, len(jids))
	for index := range jids {
		result[index] = jids[index].String()
	}
	sort.Strings(result)
	return result
}

func normalizeParticipants(participants []types.GroupParticipant) []groupParticipantPayload {
	if len(participants) == 0 {
		return nil
	}
	result := make([]groupParticipantPayload, len(participants))
	for index := range participants {
		participant := participants[index]
		result[index] = groupParticipantPayload{
			ID: participant.JID.String(), PhoneNumber: participant.PhoneNumber.String(), LID: participant.LID.String(),
			Admin: participant.IsAdmin, SuperAdmin: participant.IsSuperAdmin, DisplayName: participant.DisplayName, ErrorCode: participant.Error,
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}
