package campaign_repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	campaign_model "github.com/evolution-foundation/evolution-go/pkg/campaign/model"
	campaign_repository "github.com/evolution-foundation/evolution-go/pkg/campaign/repository"
	group_list_model "github.com/evolution-foundation/evolution-go/pkg/groupList/model"
	group_list_repository "github.com/evolution-foundation/evolution-go/pkg/groupList/repository"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestGroupDraftSnapshotIsAtomicScopedAndImmutable(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&instance_model.Instance{}); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Run(db); err != nil {
		t.Fatal(err)
	}
	suffix := uuid.NewString()
	instance := instance_model.Instance{Name: "group-campaign-" + suffix, Token: "group-campaign-token-" + suffix, Jid: "15550001@s.whatsapp.net"}
	if err := db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	defer db.Delete(&instance)

	now := time.Now().UTC().Add(-time.Hour)
	list := group_list_model.GroupList{
		ID: uuid.NewString(), InstanceID: instance.Id, Name: "Northern branches", NormalizedName: "northern branches", Version: 4,
		AuthorizationSource: "operator_attestation", AuthorizationReferenceHash: strings.Repeat("a", 64), AuthorizedAt: now,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&list).Error; err != nil {
		t.Fatal(err)
	}
	entries := []group_list_model.Entry{
		{GroupListID: list.ID, InstanceID: instance.Id, GroupJID: "120363000001@g.us", GroupNameSnapshot: "Branch 01", CreatedAt: now},
		{GroupListID: list.ID, InstanceID: instance.Id, GroupJID: "120363000002@g.us", GroupNameSnapshot: "Branch 02", CreatedAt: now},
	}
	if err := db.Create(&entries).Error; err != nil {
		t.Fatal(err)
	}

	eligibilityOverrides := map[string]campaign_repository.GroupEligibilityResult{}
	evaluator := func(_ context.Context, tx *gorm.DB, instanceID, instanceJID string, groupJIDs []string) ([]campaign_repository.GroupEligibilityResult, error) {
		if tx == nil || instanceID != instance.Id || instanceJID != instance.Jid || len(groupJIDs) != 2 {
			return nil, errors.New("eligibility did not receive the locked instance snapshot")
		}
		results := make([]campaign_repository.GroupEligibilityResult, len(groupJIDs))
		for index, groupJID := range groupJIDs {
			result := campaign_repository.GroupEligibilityResult{GroupJID: groupJID, TargetLabel: "Current " + groupJID, Eligibility: "eligible"}
			if override, exists := eligibilityOverrides[groupJID]; exists {
				result = override
				result.GroupJID = groupJID
			}
			results[index] = result
		}
		return results, nil
	}
	repository := campaign_repository.NewCampaignRepository(db, campaign_repository.WithGroupEligibilityEvaluator(evaluator), campaign_repository.WithGroupSafety(campaign_repository.GroupSafetySettings{
		Enabled: true, Cooldown: time.Hour, CircuitDuration: time.Minute, RatePauseThreshold: 2, FailurePauseThreshold: 2,
	}))
	input := campaign_repository.GroupDraftInput{
		Name: "July campaign", TextBody: "Campaign content", GroupListID: list.ID, GroupListVersion: list.Version,
		InstanceJID: instance.Jid, Actor: campaign_repository.Actor{Type: "system"},
	}
	staleInput := input
	staleInput.GroupListVersion = 3
	if _, _, err := repository.CreateGroupDraft(context.Background(), instance.Id, staleInput); !errors.Is(err, group_list_repository.ErrVersionConflict) {
		t.Fatalf("stale group list error = %v", err)
	}
	campaign, targets, err := repository.CreateGroupDraft(context.Background(), instance.Id, input)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.TargetType != campaign_model.CampaignTargetGroupList || campaign.GroupListID == nil || *campaign.GroupListID != list.ID ||
		campaign.GroupListNameSnapshot == nil || *campaign.GroupListNameSnapshot != list.Name || campaign.GroupListVersion == nil || *campaign.GroupListVersion != 4 || len(targets) != 2 {
		t.Fatalf("campaign snapshot = %#v targets=%#v", campaign, targets)
	}
	for _, target := range targets {
		if target.TargetType != campaign_model.RecipientTargetGroup || target.TargetLabel == nil || target.OptInReferenceHash != list.AuthorizationReferenceHash {
			t.Fatalf("group target snapshot = %#v", target)
		}
	}
	secondCampaign, _, err := repository.CreateGroupDraft(context.Background(), instance.Id, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []*campaign_model.Campaign{campaign, secondCampaign} {
		startsAt := time.Now().UTC()
		if _, err := repository.Transition(context.Background(), instance.Id, candidate.ID, campaign_model.CampaignStatusScheduled, &startsAt, campaign_repository.Actor{Type: "system"}); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.ActivateGroupCampaign(context.Background(), instance.Id, candidate.ID, instance.Jid, campaign_repository.Actor{Type: "system"}); err != nil {
			t.Fatal(err)
		}
	}
	claimBatches := make(chan []campaign_model.Recipient, 2)
	claimErrors := make(chan error, 2)
	var claimWorkers sync.WaitGroup
	for range 2 {
		claimWorkers.Add(1)
		go func() {
			defer claimWorkers.Done()
			claimed, claimErr := repository.ClaimReadyForInstance(context.Background(), instance.Id, 4, time.Minute)
			claimBatches <- claimed
			claimErrors <- claimErr
		}()
	}
	claimWorkers.Wait()
	close(claimBatches)
	close(claimErrors)
	for claimErr := range claimErrors {
		if claimErr != nil {
			t.Fatal(claimErr)
		}
	}
	claimedGroups := make([]campaign_model.Recipient, 0, 2)
	claimedJIDs := map[string]bool{}
	for batch := range claimBatches {
		claimedGroups = append(claimedGroups, batch...)
		for _, claimed := range batch {
			if claimedJIDs[claimed.RecipientJID] {
				t.Fatalf("group claimed concurrently by two campaigns: %s", claimed.RecipientJID)
			}
			claimedJIDs[claimed.RecipientJID] = true
		}
	}
	if len(claimedGroups) != 2 {
		t.Fatalf("concurrent guarded claims = %d, want one per group", len(claimedGroups))
	}
	for index := range claimedGroups {
		if err := repository.MarkSent(context.Background(), &claimedGroups[index], "provider-group-"+uuid.NewString()); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.MarkSent(context.Background(), &claimedGroups[0], "stale-provider"); !errors.Is(err, campaign_repository.ErrRecipientClaimLost) {
		t.Fatalf("stale group claim completion = %v", err)
	}
	cooldownClaims, err := repository.ClaimReadyForInstance(context.Background(), instance.Id, 4, time.Minute)
	if err != nil || len(cooldownClaims) != 0 {
		t.Fatalf("cooldown claims = %#v, %v", cooldownClaims, err)
	}
	eligibilityOverrides = map[string]campaign_repository.GroupEligibilityResult{}
	partialCampaign, _, err := repository.CreateGroupDraft(context.Background(), instance.Id, input)
	if err != nil {
		t.Fatal(err)
	}
	startsAt := time.Now().UTC()
	if _, err := repository.Transition(context.Background(), instance.Id, partialCampaign.ID, campaign_model.CampaignStatusScheduled, &startsAt, campaign_repository.Actor{Type: "system"}); err != nil {
		t.Fatal(err)
	}
	eligibilityOverrides[entries[0].GroupJID] = campaign_repository.GroupEligibilityResult{Eligibility: "unavailable", Reason: "group_access_lost"}
	activated, err := repository.ActivateGroupCampaign(context.Background(), instance.Id, partialCampaign.ID, instance.Jid, campaign_repository.Actor{Type: "system"})
	if err != nil || activated.Status != campaign_model.CampaignStatusRunning {
		t.Fatalf("partial activation = %#v, %v", activated, err)
	}
	partialCounts, err := repository.RecipientCounts(context.Background(), instance.Id, partialCampaign.ID)
	if err != nil || partialCounts[campaign_model.RecipientStatusSkipped] != 1 || partialCounts[campaign_model.RecipientStatusPending] != 1 {
		t.Fatalf("partial activation counts = %#v, %v", partialCounts, err)
	}

	eligibilityOverrides = map[string]campaign_repository.GroupEligibilityResult{}
	unknownCampaign, _, err := repository.CreateGroupDraft(context.Background(), instance.Id, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Transition(context.Background(), instance.Id, unknownCampaign.ID, campaign_model.CampaignStatusScheduled, &startsAt, campaign_repository.Actor{Type: "system"}); err != nil {
		t.Fatal(err)
	}
	eligibilityOverrides[entries[0].GroupJID] = campaign_repository.GroupEligibilityResult{Eligibility: "unknown", Reason: "projection_not_ready"}
	if _, err := repository.ActivateGroupCampaign(context.Background(), instance.Id, unknownCampaign.ID, instance.Jid, campaign_repository.Actor{Type: "system"}); !errors.Is(err, campaign_repository.ErrGroupProjectionNotReady) {
		t.Fatalf("unknown activation error = %v", err)
	}
	storedUnknown, err := repository.GetCampaign(context.Background(), instance.Id, unknownCampaign.ID)
	if err != nil || storedUnknown.Status != campaign_model.CampaignStatusScheduled {
		t.Fatalf("unknown activation mutated campaign = %#v, %v", storedUnknown, err)
	}

	eligibilityOverrides = map[string]campaign_repository.GroupEligibilityResult{}
	noEligibleCampaign, _, err := repository.CreateGroupDraft(context.Background(), instance.Id, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Transition(context.Background(), instance.Id, noEligibleCampaign.ID, campaign_model.CampaignStatusScheduled, &startsAt, campaign_repository.Actor{Type: "system"}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		eligibilityOverrides[entry.GroupJID] = campaign_repository.GroupEligibilityResult{Eligibility: "unavailable", Reason: "group_access_lost"}
	}
	failedCampaign, err := repository.ActivateGroupCampaign(context.Background(), instance.Id, noEligibleCampaign.ID, instance.Jid, campaign_repository.Actor{Type: "system"})
	if !errors.Is(err, campaign_repository.ErrNoEligibleTargets) || failedCampaign.Status != campaign_model.CampaignStatusFailed {
		t.Fatalf("no-eligible activation = %#v, %v", failedCampaign, err)
	}
	if err := db.Exec(`UPDATE campaign_group_delivery_guards SET last_acknowledged_at = NULL WHERE instance_id = ?`, instance.Id).Error; err != nil {
		t.Fatal(err)
	}
	rateLimitedClaim, err := repository.ClaimReadyForInstance(context.Background(), instance.Id, 1, time.Minute)
	if err != nil || len(rateLimitedClaim) != 1 {
		t.Fatalf("rate-limit claim = %#v, %v", rateLimitedClaim, err)
	}
	retryAt := time.Now().UTC().Add(2 * time.Minute)
	if err := repository.MarkRateLimited(context.Background(), &rateLimitedClaim[0], "provider_rate_limited", retryAt); err != nil {
		t.Fatal(err)
	}
	directCampaign, _, err := repository.CreateDraft(context.Background(), instance.Id, campaign_repository.DraftInput{
		Name: "Circuit-scoped direct campaign", TextBody: "Legacy direct work", Actor: campaign_repository.Actor{Type: "system"},
		Recipients: []campaign_repository.RecipientConsent{{
			JID: "15550009999@s.whatsapp.net", OptInSource: "integration_test", EvidenceReference: "circuit-consent",
			OptedInAt: now,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Transition(context.Background(), instance.Id, directCampaign.ID, campaign_model.CampaignStatusScheduled, &startsAt, campaign_repository.Actor{Type: "system"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Transition(context.Background(), instance.Id, directCampaign.ID, campaign_model.CampaignStatusRunning, nil, campaign_repository.Actor{Type: "system"}); err != nil {
		t.Fatal(err)
	}
	blockedByCircuit, err := repository.ClaimReadyForInstance(context.Background(), instance.Id, 4, time.Minute)
	if err != nil || len(blockedByCircuit) != 0 {
		t.Fatalf("open circuit allowed instance campaign claims = %#v, %v", blockedByCircuit, err)
	}
	directSnapshots, err := repository.ProgressSnapshots(context.Background(), instance.Id, []string{directCampaign.ID})
	if err != nil || directSnapshots[directCampaign.ID].RetryAt == nil || directSnapshots[directCampaign.ID].RetryAt.Before(retryAt.Add(-time.Millisecond)) {
		t.Fatalf("direct campaign circuit progress retry = %#v, %v", directSnapshots, err)
	}
	rollbackRepository := campaign_repository.NewCampaignRepository(db)
	rollbackClaims, err := rollbackRepository.ClaimReadyForInstance(context.Background(), instance.Id, 1, time.Minute)
	if err != nil || len(rollbackClaims) != 1 || rollbackClaims[0].CampaignID != directCampaign.ID || rollbackClaims[0].TargetType != campaign_model.RecipientTargetDirect {
		t.Fatalf("disabled group safety did not preserve direct claims = %#v, %v", rollbackClaims, err)
	}
	if err := rollbackRepository.MarkDeferred(context.Background(), &rollbackClaims[0], "campaign_paused", retryAt); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Transition(context.Background(), instance.Id, directCampaign.ID, campaign_model.CampaignStatusPaused, nil, campaign_repository.Actor{Type: "system"}); err != nil {
		t.Fatal(err)
	}
	snapshots, err := repository.ProgressSnapshots(context.Background(), instance.Id, []string{rateLimitedClaim[0].CampaignID})
	if err != nil || snapshots[rateLimitedClaim[0].CampaignID].RetryAt == nil || snapshots[rateLimitedClaim[0].CampaignID].RetryAt.Before(retryAt.Add(-time.Millisecond)) {
		t.Fatalf("circuit progress retry = %#v, %v", snapshots, err)
	}
	if err := db.Exec(`DELETE FROM campaign_instance_circuits WHERE instance_id = ?`, instance.Id).Error; err != nil {
		t.Fatal(err)
	}
	unknownClaim, err := repository.ClaimReadyForInstance(context.Background(), instance.Id, 1, time.Minute)
	if err != nil || len(unknownClaim) != 1 {
		t.Fatalf("unknown-outcome claim = %#v, %v", unknownClaim, err)
	}
	if err := repository.MarkUnknownOutcome(context.Background(), &unknownClaim[0]); err != nil {
		t.Fatal(err)
	}
	attentionCampaign, err := repository.GetCampaign(context.Background(), instance.Id, unknownClaim[0].CampaignID)
	if err != nil || attentionCampaign.Status != campaign_model.CampaignStatusPaused || !attentionCampaign.NeedsAttention || attentionCampaign.PauseReason == nil || *attentionCampaign.PauseReason != "unknown_send_outcome" {
		t.Fatalf("unknown-outcome campaign = %#v, %v", attentionCampaign, err)
	}
	if err := repository.MarkUnknownOutcome(context.Background(), &unknownClaim[0]); !errors.Is(err, campaign_repository.ErrRecipientClaimLost) {
		t.Fatalf("stale unknown outcome = %v", err)
	}
	eligibilityOverrides = map[string]campaign_repository.GroupEligibilityResult{}
	failureList := group_list_model.GroupList{
		ID: uuid.NewString(), InstanceID: instance.Id, Name: "Failure threshold groups", NormalizedName: "failure threshold groups", Version: 1,
		AuthorizationSource: "operator_attestation", AuthorizationReferenceHash: strings.Repeat("b", 64), AuthorizedAt: now,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(&failureList).Error; err != nil {
		t.Fatal(err)
	}
	failureEntries := []group_list_model.Entry{
		{GroupListID: failureList.ID, InstanceID: instance.Id, GroupJID: "120363000011@g.us", GroupNameSnapshot: "Failure 01", CreatedAt: now},
		{GroupListID: failureList.ID, InstanceID: instance.Id, GroupJID: "120363000012@g.us", GroupNameSnapshot: "Failure 02", CreatedAt: now},
	}
	if err := db.Create(&failureEntries).Error; err != nil {
		t.Fatal(err)
	}
	failureInput := input
	failureInput.GroupListID = failureList.ID
	failureInput.GroupListVersion = 1
	failureCampaign, _, err := repository.CreateGroupDraft(context.Background(), instance.Id, failureInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Transition(context.Background(), instance.Id, failureCampaign.ID, campaign_model.CampaignStatusScheduled, &startsAt, campaign_repository.Actor{Type: "system"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ActivateGroupCampaign(context.Background(), instance.Id, failureCampaign.ID, instance.Jid, campaign_repository.Actor{Type: "system"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE campaigns SET status = 'paused' WHERE instance_id = ? AND id <> ? AND status = 'running'`, instance.Id, failureCampaign.ID).Error; err != nil {
		t.Fatal(err)
	}
	failureClaims, err := repository.ClaimReadyForInstance(context.Background(), instance.Id, 2, time.Minute)
	if err != nil || len(failureClaims) != 2 {
		t.Fatalf("failure threshold claims = %#v, %v", failureClaims, err)
	}
	for index := range failureClaims {
		if failureClaims[index].CampaignID != failureCampaign.ID {
			t.Fatalf("unexpected failure campaign claim = %#v", failureClaims[index])
		}
		if err := repository.MarkFailed(context.Background(), &failureClaims[index], "send_permission_denied"); err != nil {
			t.Fatal(err)
		}
	}
	pausedFailureCampaign, err := repository.GetCampaign(context.Background(), instance.Id, failureCampaign.ID)
	if err != nil || pausedFailureCampaign.Status != campaign_model.CampaignStatusPaused || pausedFailureCampaign.FailureSignalCount != 2 || pausedFailureCampaign.PauseReason == nil || *pausedFailureCampaign.PauseReason != "failure_threshold_exceeded" {
		t.Fatalf("failure threshold campaign = %#v, %v", pausedFailureCampaign, err)
	}
	if err := db.Model(&group_list_model.GroupList{}).Where("id = ?", list.ID).
		Updates(map[string]any{"name": "Renamed", "version": 5, "deleted_at": time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	loaded, loadedTargets, err := repository.Get(context.Background(), instance.Id, campaign.ID)
	if err != nil || loaded.GroupListNameSnapshot == nil || *loaded.GroupListNameSnapshot != "Northern branches" || loaded.GroupListVersion == nil || *loaded.GroupListVersion != 4 || len(loadedTargets) != 2 {
		t.Fatalf("immutable loaded snapshot = %#v targets=%#v err=%v", loaded, loadedTargets, err)
	}
	audit, err := repository.ListAudit(context.Background(), instance.Id, campaign.ID)
	if err != nil || len(audit) < 1 {
		t.Fatalf("audit = %#v, %v", audit, err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(audit[0].Metadata, &metadata); err != nil || metadata["targetType"] != "group_list" || metadata["targetCount"] != float64(2) || strings.Contains(string(audit[0].Metadata), list.AuthorizationReferenceHash) {
		t.Fatalf("safe audit metadata = %s, %v", audit[0].Metadata, err)
	}

	input.GroupListVersion = 4
	if _, _, err := repository.CreateGroupDraft(context.Background(), instance.Id, input); !errors.Is(err, group_list_repository.ErrNotFound) {
		t.Fatalf("deleted list error = %v", err)
	}
	if _, _, err := repository.CreateGroupDraft(context.Background(), uuid.NewString(), input); !errors.Is(err, group_list_repository.ErrNotFound) {
		t.Fatalf("cross-instance list error = %v", err)
	}
}
