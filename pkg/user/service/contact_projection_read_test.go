package user_service

import (
	"context"
	"errors"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"gorm.io/gorm"
)

type userContactReadRepositoryStub struct{ contacts []projection_model.Contact }

func (s *userContactReadRepositoryStub) List(context.Context, string) ([]projection_model.Contact, error) {
	return s.contacts, nil
}

func (s *userContactReadRepositoryStub) GetByIdentity(context.Context, string, projection_model.ContactIdentityKind, string) (*projection_model.Contact, error) {
	if len(s.contacts) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &s.contacts[0], nil
}

type userContactReadStateStub struct {
	state *projection_model.State
	err   error
}

func (s *userContactReadStateStub) Get(string, string) (*projection_model.State, error) {
	return s.state, s.err
}

func TestGetContactsUsesProjectionWithoutWhatsAppConnection(t *testing.T) {
	firstName, fullName, pushName, businessName := "Ada", "Ada Lovelace", "Ada", "Analytical Engines"
	pictureID, about := "picture-1", "Available"
	pictureRemoved := false
	pictureUpdatedAt, aboutUpdatedAt := time.Unix(450, 0), time.Unix(460, 0)
	reconciledAt := time.Unix(500, 0)
	reader := projection_service.NewContactReader(&userContactReadRepositoryStub{contacts: []projection_model.Contact{{
		PreferredJID: "15550001@s.whatsapp.net", Found: true, FirstName: &firstName, FullName: &fullName, PushName: &pushName, BusinessName: &businessName,
		PictureID: &pictureID, PictureRemoved: &pictureRemoved, PictureUpdatedAt: &pictureUpdatedAt, About: &about, AboutUpdatedAt: &aboutUpdatedAt,
	}}}, &userContactReadStateStub{state: &projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.ContactsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}})
	service := &userService{contactReader: reader}
	contacts, meta, err := service.GetContacts(context.Background(), &instance_model.Instance{Id: "instance-a"})
	if err != nil || len(contacts) != 1 || contacts[0].Jid != "15550001@s.whatsapp.net" || !contacts[0].Found || contacts[0].FirstName != firstName ||
		contacts[0].FullName != fullName || contacts[0].PushName != pushName || contacts[0].BusinessName != businessName || contacts[0].PictureID != pictureID ||
		contacts[0].PictureRemoved == nil || *contacts[0].PictureRemoved || contacts[0].PictureUpdatedAt == nil || contacts[0].About != about ||
		contacts[0].AboutUpdatedAt == nil || meta == nil || meta.Source != "projection" {
		t.Fatalf("projection contacts = %#v, meta=%#v, error=%v", contacts, meta, err)
	}
}

func TestGetContactsPropagatesNotReady(t *testing.T) {
	reader := projection_service.NewContactReader(&userContactReadRepositoryStub{}, &userContactReadStateStub{err: gorm.ErrRecordNotFound})
	service := &userService{contactReader: reader}
	if _, _, err := service.GetContacts(context.Background(), &instance_model.Instance{Id: "instance-a"}); !errors.Is(err, projection_service.ErrContactsProjectionNotReady) {
		t.Fatalf("GetContacts() error = %v", err)
	}
}
