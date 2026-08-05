package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRepositoryPostgresLifecycleFencingAndAtomicity(t *testing.T) {
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

	instance := instance_model.Instance{
		Name: "outbox-test-" + uuid.NewString(), Token: "outbox-token-" + uuid.NewString(),
		Webhook: "https://instance.example/events", RabbitmqEnable: "enabled",
	}
	if err := db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Delete(&instance_model.Instance{}, "id = ?", instance.Id).Error })

	ctx := context.Background()
	now := time.Unix(1_000, 0).UTC()
	repository := &repository{db: db, now: func() time.Time { return now }}
	event := testDurableEvent(instance.Id, now)
	delivery := testDelivery(TransportWebhook, DestinationInstance, "instance.message")
	delivery.Payload = json.RawMessage(`{"instanceId":"safe","instanceToken":"must-not-persist"}`)
	if err := repository.Record(ctx, event, []Delivery{delivery}); err != nil {
		t.Fatal(err)
	}
	var stored Delivery
	if err := db.First(&stored, "id = ?", delivery.ID).Error; err != nil {
		t.Fatal(err)
	}
	if string(stored.Payload) != `{"instanceId": "safe"}` {
		t.Fatalf("stored payload was not sanitized: %s", stored.Payload)
	}

	claimed, err := repository.ClaimReady(ctx, 1, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].ClaimToken == nil || claimed[0].AttemptCount != 1 {
		t.Fatalf("initial claim = %#v, %v", claimed, err)
	}
	stale := claimed[0]
	wrongToken := uuid.NewString()
	stale.ClaimToken = &wrongToken
	if err := repository.MarkDelivered(ctx, &stale); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("stale claim mutation error = %v", err)
	}
	if err := repository.MarkRetry(ctx, &claimed[0], "temporary_unavailable", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if work, err := repository.ClaimReady(ctx, 1, time.Minute); err != nil || len(work) != 0 {
		t.Fatalf("delivery retried too early = %#v, %v", work, err)
	}
	now = now.Add(2 * time.Minute)
	claimed, err = repository.ClaimReady(ctx, 1, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].AttemptCount != 2 {
		t.Fatalf("due retry claim = %#v, %v", claimed, err)
	}
	if err := repository.MarkDelivered(ctx, &claimed[0]); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&stored, "id = ?", delivery.ID).Error; err != nil || stored.Status != StatusDelivered || stored.DeliveredAt == nil || string(stored.Payload) != `{}` {
		t.Fatalf("delivered row = %#v, %v", stored, err)
	}

	expiringEvent := testDurableEvent(instance.Id, now)
	expiringDelivery := testDelivery(TransportRabbitMQ, DestinationGlobal, "message")
	expiringDelivery.MaxAttempts = 1
	if err := repository.Record(ctx, expiringEvent, []Delivery{expiringDelivery}); err != nil {
		t.Fatal(err)
	}
	firstClaim, err := repository.ClaimReady(ctx, 1, time.Minute)
	if err != nil || len(firstClaim) != 1 {
		t.Fatalf("lease claim = %#v, %v", firstClaim, err)
	}
	now = now.Add(2 * time.Minute)
	reclaimed, err := repository.ClaimReady(ctx, 1, time.Minute)
	if err != nil || len(reclaimed) != 0 {
		t.Fatalf("expired lease recovery = %#v, %v", reclaimed, err)
	}
	if err := repository.MarkDelivered(ctx, &firstClaim[0]); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("superseded claim error = %v", err)
	}
	stored = Delivery{}
	if err := db.First(&stored, "id = ?", expiringDelivery.ID).Error; err != nil || stored.Status != StatusDeadLetter ||
		stored.DeadLetteredAt == nil || stored.LastErrorCode == nil || *stored.LastErrorCode != "attempt_budget_exhausted" {
		t.Fatalf("dead-letter row = %#v, %v", stored, err)
	}
	page, err := repository.ListDeadLetters(ctx, instance.Id, TransportRabbitMQ, 10, nil)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != expiringDelivery.ID {
		t.Fatalf("dead-letter page = %#v, %v", page, err)
	}
	operation := ReplayOperation{
		DeliveryID: expiringDelivery.ID, Reason: "operator confirmed transport recovery",
		ActorReferenceHash: strings.Repeat("a", 64), RequestID: "request-replay-0001", OccurredAt: now,
	}
	if err := repository.ReplayDeadLetter(ctx, operation); err != nil {
		t.Fatal(err)
	}
	stored = Delivery{}
	if err := db.First(&stored, "id = ?", expiringDelivery.ID).Error; err != nil || stored.Status != StatusPending || stored.AttemptCount != 0 ||
		stored.DeadLetteredAt != nil || string(stored.Payload) == `{}` {
		t.Fatalf("replayed row = %#v, %v", stored, err)
	}
	var auditCount int64
	if err := db.Model(&ReplayAudit{}).Where("delivery_id = ?", expiringDelivery.ID).Count(&auditCount).Error; err != nil || auditCount != 1 {
		t.Fatalf("replay audit count=%d err=%v", auditCount, err)
	}
	if err := repository.ReplayDeadLetter(ctx, operation); !errors.Is(err, ErrDeadLetterNotActionable) {
		t.Fatalf("duplicate replay error=%v", err)
	}
	replayed, err := repository.ClaimReady(ctx, 1, time.Minute)
	if err != nil || len(replayed) != 1 || replayed[0].ID != expiringDelivery.ID {
		t.Fatalf("replayed claim=%#v err=%v", replayed, err)
	}
	if err := repository.MarkDelivered(ctx, &replayed[0]); err != nil {
		t.Fatal(err)
	}

	health, err := repository.Health(ctx)
	if err != nil || health.DeadLetter < 1 {
		t.Fatalf("outbox health = %#v, %v", health, err)
	}
	resolver, err := NewDatabaseTargetResolver(db, "https://global.example/events", true)
	if err != nil {
		t.Fatal(err)
	}
	if target, err := resolver.WebhookTarget(ctx, DestinationInstance, instance.Id); err != nil || target != instance.Webhook {
		t.Fatalf("instance webhook target = %q, %v", target, err)
	}
	if mode, err := resolver.RabbitMQMode(ctx, DestinationInstance, instance.Id); err != nil || mode != "enabled" {
		t.Fatalf("instance RabbitMQ mode = %q, %v", mode, err)
	}
	if err := db.Model(&instance_model.Instance{}).Where("id = ?", instance.Id).Updates(map[string]any{
		"webhook": "disabled", "rabbitmq_enable": "true",
	}).Error; err != nil {
		t.Fatal(err)
	}
	_, err = resolver.WebhookTarget(ctx, DestinationInstance, instance.Id)
	var disabled *DeliveryError
	if !errors.As(err, &disabled) || disabled.Code != "destination_disabled" || disabled.Retryable {
		t.Fatalf("disabled webhook classification = %#v, %v", disabled, err)
	}
	if mode, err := resolver.RabbitMQMode(ctx, DestinationInstance, instance.Id); err != nil || mode != "enabled" {
		t.Fatalf("legacy true RabbitMQ mode = %q, %v", mode, err)
	}
	if target, err := resolver.WebhookTarget(ctx, DestinationGlobal, instance.Id); err != nil || target != "https://global.example/events" {
		t.Fatalf("global webhook target = %q, %v", target, err)
	}
	_, err = resolver.WebhookTarget(ctx, DestinationInstance, uuid.NewString())
	var missing *DeliveryError
	if !errors.As(err, &missing) || missing.Code != "instance_not_found" || missing.Retryable {
		t.Fatalf("missing instance classification = %#v, %v", missing, err)
	}

	rollbackEvent := testDurableEvent(instance.Id, now)
	duplicateRoute := testDelivery(TransportNATS, DestinationGlobal, "message")
	secondDuplicate := duplicateRoute
	secondDuplicate.ID = uuid.NewString()
	if err := repository.Record(ctx, rollbackEvent, []Delivery{duplicateRoute, secondDuplicate}); err == nil {
		t.Fatal("duplicate route transaction unexpectedly committed")
	}
	var durableCount int64
	if err := db.Model(&projection_model.DurableEvent{}).Where("id = ?", rollbackEvent.ID).Count(&durableCount).Error; err != nil || durableCount != 0 {
		t.Fatalf("failed outbox transaction left durable event count=%d err=%v", durableCount, err)
	}

	other := instance_model.Instance{Name: "outbox-isolation-" + uuid.NewString(), Token: "outbox-isolation-token-" + uuid.NewString()}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Delete(&instance_model.Instance{}, "id = ?", other.Id).Error })
	otherEvent := testDurableEvent(other.Id, now)
	otherDelivery := testDelivery(TransportNATS, DestinationGlobal, "isolated")
	if err := repository.Record(ctx, otherEvent, []Delivery{otherDelivery}); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&instance_model.Instance{}, "id = ?", instance.Id).Error; err != nil {
		t.Fatal(err)
	}
	var firstCount, otherCount int64
	if err := db.Model(&Delivery{}).Where("instance_id = ?", instance.Id).Count(&firstCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&Delivery{}).Where("instance_id = ?", other.Id).Count(&otherCount).Error; err != nil {
		t.Fatal(err)
	}
	if firstCount != 0 || otherCount != 1 {
		t.Fatalf("instance cascade isolation counts = first:%d other:%d", firstCount, otherCount)
	}
}

func testDurableEvent(instanceID string, now time.Time) *projection_model.DurableEvent {
	return &projection_model.DurableEvent{
		ID: uuid.NewString(), InstanceID: instanceID, Type: "Message", OccurredAt: now,
		IngestedAt: now, ExpiresAt: now.Add(24 * time.Hour), Summary: json.RawMessage(`{}`),
	}
}

func testDelivery(transport Transport, destination Destination, routingKey string) Delivery {
	return Delivery{
		ID: uuid.NewString(), Transport: transport, Destination: destination, RoutingKey: routingKey,
		Payload: json.RawMessage(`{"event":"Message"}`),
	}
}
