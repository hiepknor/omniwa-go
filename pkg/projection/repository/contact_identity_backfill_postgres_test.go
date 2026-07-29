package projection_repository

import (
	"context"
	"errors"
	"os"
	"sort"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestContactIdentityBackfillCheckpointLeasesResumeAndComplete(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&instance_model.Instance{}); err != nil {
		t.Fatal(err)
	}
	if err = migrations.Run(db); err != nil {
		t.Fatal(err)
	}
	instance := instance_model.Instance{Name: "contact-backfill-" + uuid.NewString(), Token: "contact-backfill-token-" + uuid.NewString()}
	if err = db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Delete(&instance).Error })

	contacts := NewContactRepository(db)
	ids := make([]string, 0, 2)
	for index, jid := range []string{"15550001@s.whatsapp.net", "15550002@s.whatsapp.net"} {
		contact, _, applyErr := contacts.Apply(context.Background(), ContactPatch{
			InstanceID: instance.Id,
			Identities: []ContactIdentityRef{{Kind: projection_model.ContactIdentityKindPhoneJID, Value: jid}},
			Aspect:     ContactAspectDetails, OccurredAt: time.Unix(int64(10+index), 0).UTC(), EventKey: jid,
		})
		if applyErr != nil {
			t.Fatal(applyErr)
		}
		ids = append(ids, contact.ContactID)
	}
	sort.Strings(ids)

	backfill := NewContactIdentityBackfillRepository(db)
	ownerOne, ownerTwo := uuid.NewString(), uuid.NewString()
	first, err := backfill.ClaimBatch(context.Background(), instance.Id, 1, ownerOne, 1, time.Unix(100, 0).UTC(), time.Unix(200, 0).UTC())
	if err != nil || len(first.Items) != 1 || first.Items[0].ContactID != ids[0] || first.Items[0].PreferredJID == "" || first.Items[0].PhoneJID == nil || *first.Items[0].PhoneJID == "" || first.Complete {
		t.Fatalf("first claim = %#v, %v", first, err)
	}
	if restarted, restartErr := backfill.RestartCompleted(context.Background(), instance.Id, 1, time.Unix(101, 0).UTC()); restartErr != nil || restarted {
		t.Fatalf("active pass restart = %t, %v", restarted, restartErr)
	}
	if _, err = backfill.ClaimBatch(context.Background(), instance.Id, 1, ownerTwo, 1, time.Unix(150, 0).UTC(), time.Unix(250, 0).UTC()); !errors.Is(err, ErrContactIdentityBackfillLeaseHeld) {
		t.Fatalf("competing lease error = %v", err)
	}
	recovered, err := backfill.ClaimBatch(context.Background(), instance.Id, 1, ownerTwo, 1, time.Unix(201, 0).UTC(), time.Unix(301, 0).UTC())
	if err != nil || len(recovered.Items) != 1 || recovered.Items[0].ContactID != ids[0] {
		t.Fatalf("expired lease recovery = %#v, %v", recovered, err)
	}
	cursor := recovered.Items[0].ContactID
	if err = backfill.CommitBatch(context.Background(), instance.Id, 1, ownerOne, &cursor, ContactIdentityBackfillCounts{Scanned: 1}, false, time.Unix(202, 0).UTC()); !errors.Is(err, ErrContactIdentityBackfillLeaseLost) {
		t.Fatalf("stale owner commit error = %v", err)
	}
	if err = backfill.CommitBatch(context.Background(), instance.Id, 1, ownerTwo, &cursor, ContactIdentityBackfillCounts{Scanned: 1, Unchanged: 1}, false, time.Unix(203, 0).UTC()); err != nil {
		t.Fatal(err)
	}

	second, err := backfill.ClaimBatch(context.Background(), instance.Id, 1, ownerTwo, 1, time.Unix(204, 0).UTC(), time.Unix(304, 0).UTC())
	if err != nil || len(second.Items) != 1 || second.Items[0].ContactID != ids[1] {
		t.Fatalf("second claim = %#v, %v", second, err)
	}
	cursor = second.Items[0].ContactID
	if err = backfill.CommitBatch(context.Background(), instance.Id, 1, ownerTwo, &cursor, ContactIdentityBackfillCounts{Scanned: 1, Mapped: 1, Merged: 1}, false, time.Unix(205, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	empty, err := backfill.ClaimBatch(context.Background(), instance.Id, 1, ownerTwo, 1, time.Unix(206, 0).UTC(), time.Unix(306, 0).UTC())
	if err != nil || len(empty.Items) != 0 || !empty.Complete || empty.AlreadyComplete {
		t.Fatalf("terminal claim = %#v, %v", empty, err)
	}
	if err = backfill.CommitBatch(context.Background(), instance.Id, 1, ownerTwo, nil, ContactIdentityBackfillCounts{}, true, time.Unix(207, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	terminal, err := backfill.ClaimBatch(context.Background(), instance.Id, 1, uuid.NewString(), 1, time.Unix(208, 0).UTC(), time.Unix(308, 0).UTC())
	if err != nil || !terminal.AlreadyComplete {
		t.Fatalf("completed claim = %#v, %v", terminal, err)
	}
	state, err := backfill.GetState(context.Background(), instance.Id)
	if err != nil || state.Status != projection_model.ContactIdentityBackfillComplete || state.ScannedCount != 2 || state.MappedCount != 1 || state.MergedCount != 1 || state.UnchangedCount != 1 || state.CursorContactID == nil || *state.CursorContactID != ids[1] {
		t.Fatalf("checkpoint state = %#v, %v", state, err)
	}
	restarted, err := backfill.RestartCompleted(context.Background(), instance.Id, 1, time.Unix(209, 0).UTC())
	if err != nil || !restarted {
		t.Fatalf("completed pass restart = %t, %v", restarted, err)
	}
	refreshed, err := backfill.ClaimBatch(context.Background(), instance.Id, 1, uuid.NewString(), 1, time.Unix(210, 0).UTC(), time.Unix(310, 0).UTC())
	if err != nil || refreshed.AlreadyComplete || len(refreshed.Items) != 1 || refreshed.Items[0].ContactID != ids[0] {
		t.Fatalf("refreshed first claim = %#v, %v", refreshed, err)
	}
}
