package user_service

import (
	"context"
	"errors"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
	projection_repository "github.com/evolution-foundation/evolution-go/pkg/projection/repository"
	projection_service "github.com/evolution-foundation/evolution-go/pkg/projection/service"
	"gorm.io/gorm"
)

type userContactReadRepositoryStub struct{ contacts []projection_model.Contact }

type contactPhoneEvidenceStub struct {
	listCalls, resolveCalls int
	rows                    []projection_model.PhoneIdentityEvidence
}

func (*contactPhoneEvidenceStub) Observe(context.Context, projection_model.PhoneIdentityEvidence) (bool, error) {
	return false, nil
}
func (s *contactPhoneEvidenceStub) Resolve(context.Context, string, []string) (map[string]string, error) {
	s.resolveCalls++
	return nil, nil
}
func (s *contactPhoneEvidenceStub) List(context.Context, string) ([]projection_model.PhoneIdentityEvidence, error) {
	s.listCalls++
	return s.rows, nil
}

func (s *userContactReadRepositoryStub) List(context.Context, string) ([]projection_model.Contact, error) {
	return s.contacts, nil
}

func (s *userContactReadRepositoryStub) Count(context.Context, string) (int64, error) {
	return int64(len(s.contacts)), nil
}

func (s *userContactReadRepositoryStub) Get(context.Context, string, string) (*projection_model.Contact, error) {
	if len(s.contacts) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &s.contacts[0], nil
}

func (s *userContactReadRepositoryStub) GetByIdentity(context.Context, string, projection_model.ContactIdentityKind, string) (*projection_model.Contact, error) {
	if len(s.contacts) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &s.contacts[0], nil
}

func (s *userContactReadRepositoryStub) Search(context.Context, string, string, int, *projection_repository.ContactCursor) (*projection_repository.ContactPage, error) {
	return &projection_repository.ContactPage{Items: s.contacts, Total: int64(len(s.contacts))}, nil
}

type userContactReadStateStub struct {
	state *projection_model.State
	err   error
}

func (s *userContactReadStateStub) GetServingState(string, string) (*projection_model.State, error) {
	return s.state, s.err
}

func TestGetContactsUsesProjectionWithoutWhatsAppConnection(t *testing.T) {
	firstName, fullName, pushName, businessName := "Ada", "Ada Lovelace", "Ada", "Analytical Engines"
	pictureID, about := "picture-1", "Available"
	phoneJID, lid := "15550001@s.whatsapp.net", "900000000001@lid"
	pictureRemoved := false
	pictureUpdatedAt, aboutUpdatedAt := time.Unix(450, 0), time.Unix(460, 0)
	reconciledAt := time.Unix(500, 0)
	reader := projection_service.NewContactReader(&userContactReadRepositoryStub{contacts: []projection_model.Contact{{
		ContactID: "11111111-1111-1111-1111-111111111111", PreferredJID: phoneJID, PhoneJID: &phoneJID, LID: &lid,
		Found: true, FirstName: &firstName, FullName: &fullName, PushName: &pushName, BusinessName: &businessName,
		PictureID: &pictureID, PictureRemoved: &pictureRemoved, PictureUpdatedAt: &pictureUpdatedAt, About: &about, AboutUpdatedAt: &aboutUpdatedAt,
		UpdatedAt: reconciledAt, Aliases: []projection_model.ContactIdentity{{Value: phoneJID}, {Value: lid}},
	}}}, &userContactReadStateStub{state: &projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.ContactsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}})
	service := &userService{contactReader: reader}
	contacts, meta, err := service.GetContacts(context.Background(), &instance_model.Instance{Id: "instance-a"})
	if err != nil || len(contacts) != 1 || contacts[0].Jid != phoneJID || contacts[0].ContactID == "" || contacts[0].AddressingJID != phoneJID ||
		contacts[0].IdentityStatus != "complete" || contacts[0].DisplayName != fullName || contacts[0].DisplayNameSource != "full_name" || len(contacts[0].Aliases) != 2 || !contacts[0].Found || contacts[0].FirstName != firstName ||
		contacts[0].FullName != fullName || contacts[0].PushName != pushName || contacts[0].BusinessName != businessName || contacts[0].PictureID != pictureID ||
		contacts[0].PictureRemoved == nil || *contacts[0].PictureRemoved || contacts[0].PictureUpdatedAt == nil || contacts[0].About != about ||
		contacts[0].AboutUpdatedAt == nil || meta == nil || meta.Source != "projection" || meta.Total == nil || *meta.Total != 1 {
		t.Fatalf("projection contacts = %#v, meta=%#v, error=%v", contacts, meta, err)
	}
}

func TestGetContactsResolvesPhoneNumbersWithOneInstanceQuery(t *testing.T) {
	phoneJID, lid := "15550001@s.whatsapp.net", "900000000001@lid"
	reconciledAt := time.Unix(500, 0)
	reader := projection_service.NewContactReader(&userContactReadRepositoryStub{contacts: []projection_model.Contact{
		{ContactID: "11111111-1111-1111-1111-111111111111", PreferredJID: lid, LID: &lid, UpdatedAt: reconciledAt},
		{ContactID: "22222222-2222-2222-2222-222222222222", PreferredJID: "other@lid", UpdatedAt: reconciledAt},
	}}, &userContactReadStateStub{state: &projection_model.State{SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.ContactsProjectionSchemaVersion, LastReconciledAt: &reconciledAt}})
	evidence := &contactPhoneEvidenceStub{rows: []projection_model.PhoneIdentityEvidence{{PhoneJID: phoneJID, LIDJID: &lid}}}
	service := &userService{contactReader: reader, phoneNumbers: projection_service.NewPhoneNumberResolver(evidence, true, nil)}
	contacts, _, err := service.GetContacts(context.Background(), &instance_model.Instance{Id: "11111111-1111-1111-1111-111111111111"})
	if err != nil || evidence.listCalls != 1 || evidence.resolveCalls != 0 || contacts[0].PhoneNumber == nil || *contacts[0].PhoneNumber != "15550001" || contacts[1].PhoneNumber != nil {
		t.Fatalf("contacts=%#v list=%d resolve=%d err=%v", contacts, evidence.listCalls, evidence.resolveCalls, err)
	}
}

func TestGetContactsPropagatesNotReady(t *testing.T) {
	reader := projection_service.NewContactReader(&userContactReadRepositoryStub{}, &userContactReadStateStub{err: gorm.ErrRecordNotFound})
	service := &userService{contactReader: reader}
	if _, _, err := service.GetContacts(context.Background(), &instance_model.Instance{Id: "instance-a"}); !errors.Is(err, projection_service.ErrContactsProjectionNotReady) {
		t.Fatalf("GetContacts() error = %v", err)
	}
}

func TestSearchContactsUsesNormalizedProjectionModel(t *testing.T) {
	fullName := "Ada Lovelace"
	reconciledAt := time.Unix(500, 0)
	reader := projection_service.NewContactReader(&userContactReadRepositoryStub{contacts: []projection_model.Contact{{
		ContactID: "11111111-1111-1111-1111-111111111111", PreferredJID: "ada@s.whatsapp.net", Found: true, FullName: &fullName,
	}}}, &userContactReadStateStub{state: &projection_model.State{
		SyncStatus: projection_model.SyncStatusReady, SchemaVersion: projection_service.ContactsProjectionSchemaVersion, LastReconciledAt: &reconciledAt,
	}})
	service := &userService{contactReader: reader}
	contacts, meta, err := service.SearchContacts(context.Background(), &instance_model.Instance{Id: "instance-a"}, "ada", 50, "")
	if err != nil || len(contacts) != 1 || contacts[0].Jid != "ada@s.whatsapp.net" || contacts[0].FullName != fullName || meta == nil || meta.Source != "projection" {
		t.Fatalf("SearchContacts() = %#v, %#v, %v", contacts, meta, err)
	}
}
