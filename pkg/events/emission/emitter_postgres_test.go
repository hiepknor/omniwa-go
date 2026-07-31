package emission

import (
	"context"
	"os"
	"testing"
	"time"

	event_outbox "github.com/evolution-foundation/evolution-go/pkg/events/outbox"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestEmitterPostgresAtomicallyRecordsHistoryAndRoutes(t *testing.T) {
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
	instance := &instance_model.Instance{
		Name: "emitter-test-" + uuid.NewString(), Token: "emitter-token-" + uuid.NewString(), Events: "MESSAGE",
		Webhook: "https://instance.example/events", RabbitmqEnable: "enabled",
	}
	if err := db.Create(instance).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Delete(&instance_model.Instance{}, "id = ?", instance.Id).Error })

	builder := projection_service.NewDurableEventService(projection_repository.NewDurableEventRepository(db), time.Hour)
	emitter, err := NewEmitter(builder, event_outbox.NewRepository(db), Settings{
		GlobalWebhookEnabled: true,
		GlobalRabbitEnabled:  true, AMQPGlobalEvents: []string{"MESSAGE"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"event":"Message","data":{"Info":{"Chat":"15551234567@s.whatsapp.net"}}}`)
	if err := emitter.Record(context.Background(), Event{
		Instance: instance, Type: "Message", QueueName: instance.Id + ".message", Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	directEmitter, err := NewEmitter(builder, event_outbox.NewRepository(db), Settings{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := directEmitter.Record(context.Background(), Event{
		Instance: instance, Type: "Presence", QueueName: instance.Id + ".presence", Payload: []byte(`{"event":"Presence"}`),
	}); err != nil {
		t.Fatal(err)
	}
	var historyCount, routeCount int64
	if err := db.Model(&projection_model.DurableEvent{}).Where("instance_id = ?", instance.Id).Count(&historyCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&event_outbox.Delivery{}).Where("instance_id = ?", instance.Id).Count(&routeCount).Error; err != nil {
		t.Fatal(err)
	}
	if historyCount != 2 || routeCount != 4 {
		t.Fatalf("atomic rows history=%d routes=%d", historyCount, routeCount)
	}
}
