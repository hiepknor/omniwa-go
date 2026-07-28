package group_list_repository

import (
	"errors"
	"time"
)

const MaxPublicEligibilityIssues = 100

type EligibilityMutationResult struct {
	GroupJID          string
	CurrentName       string
	SnapshotName      string
	Eligibility       string
	EligibilityReason *string
	CanSend           bool
	CheckedAt         time.Time
}

type EligibilityIssue struct {
	GroupJID          string    `json:"groupJid"`
	CurrentName       string    `json:"currentName"`
	Eligibility       string    `json:"eligibility"`
	EligibilityReason *string   `json:"eligibilityReason"`
	CanSend           bool      `json:"canSend"`
	CheckedAt         time.Time `json:"checkedAt"`
}

type EligibilityIssueDetails struct {
	IssueCount int                `json:"issueCount"`
	Truncated  bool               `json:"truncated"`
	Issues     []EligibilityIssue `json:"issues"`
}

type EligibilityIssuesError struct {
	Cause   error
	Details EligibilityIssueDetails
}

func (e *EligibilityIssuesError) Error() string {
	if e == nil || e.Cause == nil {
		return "group eligibility rejected"
	}
	return e.Cause.Error()
}

func (e *EligibilityIssuesError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// MutationEntries turns one authoritative eligibility result set into entry
// snapshots or one bounded public-safe issue error. It classifies only the
// three stable service states; the eligibility decision itself remains owned
// by the Group List eligibility service.
func MutationEntries(results []EligibilityMutationResult, unavailableCause, unknownCause error) ([]EntryInput, error) {
	if unavailableCause == nil || unknownCause == nil {
		return nil, errors.New("eligibility rejection causes are required")
	}
	issueCapacity := len(results)
	if issueCapacity > MaxPublicEligibilityIssues {
		issueCapacity = MaxPublicEligibilityIssues
	}
	issues := make([]EligibilityIssue, 0, issueCapacity)
	entries := make([]EntryInput, len(results))
	issueCount := 0
	hasUnknown := false
	for index, result := range results {
		switch result.Eligibility {
		case "eligible":
			entries[index] = EntryInput{GroupJID: result.GroupJID, GroupNameSnapshot: result.SnapshotName}
			continue
		case "unknown":
			hasUnknown = true
		case "unavailable":
		default:
			return nil, errors.New("eligibility returned an unsupported state")
		}
		issueCount++
		if len(issues) < MaxPublicEligibilityIssues {
			issues = append(issues, EligibilityIssue{
				GroupJID: result.GroupJID, CurrentName: result.CurrentName, Eligibility: result.Eligibility,
				EligibilityReason: result.EligibilityReason, CanSend: result.CanSend, CheckedAt: result.CheckedAt,
			})
		}
	}
	if issueCount == 0 {
		return entries, nil
	}
	cause := unavailableCause
	if hasUnknown {
		cause = unknownCause
	}
	return nil, &EligibilityIssuesError{Cause: cause, Details: EligibilityIssueDetails{
		IssueCount: issueCount, Truncated: issueCount > MaxPublicEligibilityIssues, Issues: issues,
	}}
}
