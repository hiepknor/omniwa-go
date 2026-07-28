package projection_repository

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/evolution-foundation/evolution-go/pkg/migrations"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestContactMergeKeepsPermanentFlattenedRedirectsAndChatReferences(t *testing.T) {
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
	instances := []instance_model.Instance{
		{Name: "contact-redirect-a", Token: "contact-redirect-a-token"},
		{Name: "contact-redirect-b", Token: "contact-redirect-b-token"},
	}
	for index := range instances {
		if err = db.Create(&instances[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for index := range instances {
			_ = db.Delete(&instances[index]).Error
		}
	})

	repository := NewContactRepository(db)
	create := func(kind projection_model.ContactIdentityKind, value string, eventTime int64) *projection_model.Contact {
		t.Helper()
		contact, applied, applyErr := repository.Apply(context.Background(), ContactPatch{
			InstanceID: instances[0].Id,
			Identities: []ContactIdentityRef{{Kind: kind, Value: value}},
			Aspect:     ContactAspectDetails, OccurredAt: time.Unix(eventTime, 0).UTC(), EventKey: value,
		})
		if applyErr != nil || !applied {
			t.Fatalf("create contact %s = %#v, %v, %v", value, contact, applied, applyErr)
		}
		return contact
	}

	phone := create(projection_model.ContactIdentityKindPhoneJID, "15551230001@s.whatsapp.net", 100)
	lid := create(projection_model.ContactIdentityKindLID, "900000000001@lid", 101)
	current := create(projection_model.ContactIdentityKindJID, "current@s.whatsapp.net", 102)
	createdAt := map[string]time.Time{
		phone.ContactID:   time.Unix(300, 0).UTC(),
		lid.ContactID:     time.Unix(200, 0).UTC(),
		current.ContactID: time.Unix(100, 0).UTC(),
	}
	for contactID, at := range createdAt {
		if err = db.Model(&projection_model.Contact{}).
			Where("instance_id = ? AND contact_id = ?", instances[0].Id, contactID).
			Update("created_at", at).Error; err != nil {
			t.Fatal(err)
		}
	}

	chatRepository := NewChatMessageRepository(db)
	if _, err = chatRepository.ApplyChat(context.Background(), &projection_model.Chat{
		InstanceID: instances[0].Id, ChatID: "direct-chat", ContactID: &phone.ContactID,
		Type: projection_model.ChatTypeDirect, SourceOccurredAt: time.Unix(110, 0).UTC(), SourceEventKey: "chat",
	}, ChatAspectIdentity); err != nil {
		t.Fatal(err)
	}

	if _, _, err = repository.Apply(context.Background(), ContactPatch{
		InstanceID: instances[0].Id,
		Identities: []ContactIdentityRef{
			{Kind: projection_model.ContactIdentityKindPhoneJID, Value: "15551230001@s.whatsapp.net"},
			{Kind: projection_model.ContactIdentityKindLID, Value: "900000000001@lid"},
		},
		Aspect: ContactAspectDetails, OccurredAt: time.Unix(120, 0).UTC(), EventKey: "phone-lid-map",
	}); err != nil {
		t.Fatal(err)
	}
	if resolved, getErr := repository.Get(context.Background(), instances[0].Id, phone.ContactID); getErr != nil || resolved.ContactID != lid.ContactID {
		t.Fatalf("first redirect = %#v, %v", resolved, getErr)
	}

	if _, _, err = repository.Apply(context.Background(), ContactPatch{
		InstanceID: instances[0].Id,
		Identities: []ContactIdentityRef{
			{Kind: projection_model.ContactIdentityKindLID, Value: "900000000001@lid"},
			{Kind: projection_model.ContactIdentityKindJID, Value: "current@s.whatsapp.net"},
		},
		Aspect: ContactAspectDetails, OccurredAt: time.Unix(130, 0).UTC(), EventKey: "lid-current-map",
	}); err != nil {
		t.Fatal(err)
	}

	for _, absorbedID := range []string{phone.ContactID, lid.ContactID} {
		resolved, getErr := repository.Get(context.Background(), instances[0].Id, absorbedID)
		if getErr != nil || resolved.ContactID != current.ContactID {
			t.Fatalf("flattened redirect %s = %#v, %v", absorbedID, resolved, getErr)
		}
		if resolved.PhoneJID == nil || *resolved.PhoneJID != "15551230001@s.whatsapp.net" || resolved.LID == nil || *resolved.LID != "900000000001@lid" || resolved.PreferredJID != "15551230001@s.whatsapp.net" {
			t.Fatalf("canonical addressing aliases = %#v", resolved)
		}
		if _, getErr = repository.Get(context.Background(), instances[1].Id, absorbedID); !errors.Is(getErr, gorm.ErrRecordNotFound) {
			t.Fatalf("cross-instance redirect error = %v", getErr)
		}
	}

	var redirects []projection_model.ContactRedirect
	if err = db.Where("instance_id = ?", instances[0].Id).Order("absorbed_contact_id").Find(&redirects).Error; err != nil {
		t.Fatal(err)
	}
	if len(redirects) != 2 {
		t.Fatalf("redirects = %#v", redirects)
	}
	for _, redirect := range redirects {
		if redirect.CanonicalContactID != current.ContactID {
			t.Fatalf("redirect was not flattened = %#v", redirect)
		}
	}
	chat, err := chatRepository.GetChat(context.Background(), instances[0].Id, "direct-chat")
	if err != nil || chat.ContactID == nil || *chat.ContactID != current.ContactID {
		t.Fatalf("chat contact reference = %#v, %v", chat, err)
	}
	validationRepository := NewContactIdentityBackfillRepository(db)
	if validation, validateErr := validationRepository.Validate(context.Background(), instances[0].Id); validateErr != nil || !validation.Valid() {
		t.Fatalf("canonical identity validation = %#v, %v", validation, validateErr)
	}
	if err = db.Model(&projection_model.Chat{}).Where("instance_id = ? AND chat_id = ?", instances[0].Id, "direct-chat").Update("contact_id", phone.ContactID).Error; err != nil {
		t.Fatal(err)
	}
	if validation, validateErr := validationRepository.Validate(context.Background(), instances[0].Id); !errors.Is(validateErr, ErrContactIdentityValidation) || validation.ChatsReferencingAbsorbed != 1 {
		t.Fatalf("invalid canonical identity graph = %#v, %v", validation, validateErr)
	}
	var activeContacts int64
	if err = db.Model(&projection_model.Contact{}).Where("instance_id = ? AND tombstoned_at IS NULL", instances[0].Id).Count(&activeContacts).Error; err != nil {
		t.Fatal(err)
	}
	if activeContacts != 1 {
		t.Fatalf("active canonical contacts = %d", activeContacts)
	}
}
