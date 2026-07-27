package campaign_repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
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

	evaluator := func(_ context.Context, tx *gorm.DB, instanceID, instanceJID string, groupJIDs []string) ([]campaign_repository.GroupTargetSnapshot, error) {
		if tx == nil || instanceID != instance.Id || instanceJID != instance.Jid || len(groupJIDs) != 2 {
			return nil, errors.New("eligibility did not receive the locked instance snapshot")
		}
		return []campaign_repository.GroupTargetSnapshot{
			{GroupJID: groupJIDs[0], TargetLabel: "HCM Branch"},
			{GroupJID: groupJIDs[1], TargetLabel: "Hanoi Branch"},
		}, nil
	}
	repository := campaign_repository.NewCampaignRepository(db, campaign_repository.WithGroupEligibilityEvaluator(evaluator))
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
	if err := db.Model(&group_list_model.GroupList{}).Where("id = ?", list.ID).
		Updates(map[string]any{"name": "Renamed", "version": 5, "deleted_at": time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	loaded, loadedTargets, err := repository.Get(context.Background(), instance.Id, campaign.ID)
	if err != nil || loaded.GroupListNameSnapshot == nil || *loaded.GroupListNameSnapshot != "Northern branches" || loaded.GroupListVersion == nil || *loaded.GroupListVersion != 4 || len(loadedTargets) != 2 {
		t.Fatalf("immutable loaded snapshot = %#v targets=%#v err=%v", loaded, loadedTargets, err)
	}
	audit, err := repository.ListAudit(context.Background(), instance.Id, campaign.ID)
	if err != nil || len(audit) != 1 {
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
