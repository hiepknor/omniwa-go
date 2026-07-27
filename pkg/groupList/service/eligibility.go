package group_list_service

import (
	"context"
	"errors"
	"strings"
	"time"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"go.mau.fi/whatsmeow/types"
	"gorm.io/gorm"
)

const (
	EligibilityEligible    = "eligible"
	EligibilityUnavailable = "unavailable"
	EligibilityUnknown     = "unknown"

	ReasonAccessLost         = "group_access_lost"
	ReasonDissolved          = "group_dissolved"
	ReasonPermissionDenied   = "send_permission_denied"
	ReasonSuspended          = "group_suspended"
	ReasonProjectionNotReady = "projection_not_ready"
)

type eligibilityGroupReader interface {
	GetForEligibility(context.Context, string, []string) ([]projection_repository.GroupRecord, error)
}

type eligibilityStateReader interface {
	GetServingState(instanceID, resource string) (*projection_model.State, error)
}

type EligibilityResult struct {
	GroupJID          string    `json:"groupJid"`
	CurrentName       string    `json:"currentName"`
	Eligibility       string    `json:"eligibility"`
	EligibilityReason *string   `json:"eligibilityReason"`
	CanSend           bool      `json:"canSend"`
	CheckedAt         time.Time `json:"checkedAt"`
}

type EligibilityService struct {
	groups eligibilityGroupReader
	state  eligibilityStateReader
	now    func() time.Time
}

func NewEligibilityService(groups eligibilityGroupReader, state eligibilityStateReader) *EligibilityService {
	return &EligibilityService{groups: groups, state: state, now: time.Now}
}

func (s *EligibilityService) Evaluate(ctx context.Context, instanceID, instanceJID string, groupJIDs []string) ([]EligibilityResult, error) {
	if s == nil || s.groups == nil || s.state == nil || s.now == nil || ctx == nil || instanceID == "" || len(groupJIDs) == 0 || len(groupJIDs) > 10_000 {
		return nil, errors.New("group eligibility dependencies and bounded identities are required")
	}
	canonicalGroups := make([]string, len(groupJIDs))
	seen := make(map[string]struct{}, len(groupJIDs))
	for index, value := range groupJIDs {
		canonical, err := CanonicalGroupJID(value)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[canonical]; exists {
			return nil, errors.New("duplicate group identity")
		}
		seen[canonical] = struct{}{}
		canonicalGroups[index] = canonical
	}
	checkedAt := s.now().UTC()
	state, err := s.state.GetServingState(instanceID, "groups")
	if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && !readyEligibilityState(state) {
		return unknownEligibility(canonicalGroups, checkedAt), nil
	}
	if err != nil {
		return nil, err
	}
	identity, err := canonicalIdentity(instanceJID)
	if err != nil {
		return unknownEligibility(canonicalGroups, checkedAt), nil
	}
	records, err := s.groups.GetForEligibility(ctx, instanceID, canonicalGroups)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]projection_repository.GroupRecord, len(records))
	for _, record := range records {
		byID[record.Group.GroupID] = record
	}
	results := make([]EligibilityResult, len(canonicalGroups))
	for index, groupJID := range canonicalGroups {
		result := EligibilityResult{GroupJID: groupJID, CheckedAt: checkedAt}
		record, exists := byID[groupJID]
		if !exists {
			setUnavailable(&result, ReasonAccessLost)
			results[index] = result
			continue
		}
		if record.Group.Name != nil {
			result.CurrentName = *record.Group.Name
		}
		if record.Group.TombstonedAt != nil {
			if record.Group.TombstoneCause != nil && *record.Group.TombstoneCause == projection_model.GroupTombstoneDissolved {
				setUnavailable(&result, ReasonDissolved)
			} else {
				setUnavailable(&result, ReasonAccessLost)
			}
			results[index] = result
			continue
		}
		if record.Group.Suspended == nil || record.Group.Announce == nil {
			setUnknown(&result)
			results[index] = result
			continue
		}
		participant, found := matchingParticipant(identity, record.Participants)
		if !found {
			setUnavailable(&result, ReasonAccessLost)
			results[index] = result
			continue
		}
		if *record.Group.Suspended {
			setUnavailable(&result, ReasonSuspended)
		} else if *record.Group.Announce && participant.Role != projection_model.ParticipantRoleAdmin && participant.Role != projection_model.ParticipantRoleSuperAdmin {
			setUnavailable(&result, ReasonPermissionDenied)
		} else {
			result.Eligibility = EligibilityEligible
			result.CanSend = true
		}
		results[index] = result
	}
	return results, nil
}

func CanonicalGroupJID(value string) (string, error) {
	jid, err := types.ParseJID(strings.TrimSpace(value))
	if err != nil || jid.User == "" || jid.Server != types.GroupServer {
		return "", errors.New("group identity must be a WhatsApp group JID")
	}
	canonical := jid.ToNonAD().String()
	if canonical == "" || len(canonical) > 255 {
		return "", errors.New("group identity is invalid")
	}
	return canonical, nil
}

func readyEligibilityState(state *projection_model.State) bool {
	return state != nil && state.SyncStatus == projection_model.SyncStatusReady && state.LastReconciledAt != nil && state.SchemaVersion >= projection_service.GroupsProjectionSchemaVersion
}

func canonicalIdentity(value string) (string, error) {
	jid, err := types.ParseJID(strings.TrimSpace(value))
	if err != nil || jid.User == "" {
		return "", errors.New("instance identity is invalid")
	}
	return jid.ToNonAD().String(), nil
}

func matchingParticipant(identity string, participants []projection_model.GroupParticipant) (projection_model.GroupParticipant, bool) {
	for _, participant := range participants {
		for _, value := range []string{participant.ParticipantID, stringValue(participant.PhoneNumberJID), stringValue(participant.LID)} {
			canonical, err := canonicalIdentity(value)
			if err == nil && canonical == identity {
				return participant, true
			}
		}
	}
	return projection_model.GroupParticipant{}, false
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func unknownEligibility(groupJIDs []string, checkedAt time.Time) []EligibilityResult {
	results := make([]EligibilityResult, len(groupJIDs))
	for index, groupJID := range groupJIDs {
		results[index] = EligibilityResult{GroupJID: groupJID, CheckedAt: checkedAt}
		setUnknown(&results[index])
	}
	return results
}

func setUnknown(result *EligibilityResult) {
	reason := ReasonProjectionNotReady
	result.Eligibility = EligibilityUnknown
	result.EligibilityReason = &reason
	result.CanSend = false
}

func setUnavailable(result *EligibilityResult, reason string) {
	result.Eligibility = EligibilityUnavailable
	result.EligibilityReason = &reason
	result.CanSend = false
}
