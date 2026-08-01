package projection_service

import (
	"context"
	"testing"
)

func TestConversationMessagePhoneFieldsPreserveIdentityRoles(t *testing.T) {
	sender, recipient, participant := "sender@lid", "recipient@lid", "participant@lid"
	repository := &phoneResolverRepositoryStub{resolved: map[string]string{
		sender: "15550001@s.whatsapp.net", recipient: "15550002@c.us", participant: "15550003:9@s.whatsapp.net",
	}}
	reader := &ChatMessageReader{phoneNumbers: NewPhoneNumberResolver(repository, true, nil)}
	items := []ProjectedConversationMessage{{SenderJID: &sender, RecipientJID: &recipient, ParticipantJID: &participant}}
	reader.enrichMessagePhones(context.Background(), "11111111-1111-1111-1111-111111111111", items)
	if items[0].SenderPhoneNumber == nil || *items[0].SenderPhoneNumber != "15550001" ||
		items[0].RecipientPhoneNumber == nil || *items[0].RecipientPhoneNumber != "15550002" ||
		items[0].ParticipantPhoneNumber == nil || *items[0].ParticipantPhoneNumber != "15550003" {
		t.Fatalf("item=%#v", items[0])
	}
}

func TestConversationPhoneNumberOnlyAppearsForDirectConversation(t *testing.T) {
	direct, group := "direct@lid", "12345@g.us"
	repository := &phoneResolverRepositoryStub{resolved: map[string]string{direct: "15550001@s.whatsapp.net"}}
	reader := &ChatMessageReader{phoneNumbers: NewPhoneNumberResolver(repository, true, nil)}
	items := []ProjectedConversation{
		{Type: ConversationTypeDirect, AddressingJID: &direct},
		{Type: ConversationTypeGroup, AddressingJID: &group},
	}
	reader.enrichConversationPhones(context.Background(), "11111111-1111-1111-1111-111111111111", items)
	if items[0].PhoneNumber == nil || *items[0].PhoneNumber != "15550001" || items[1].PhoneNumber != nil {
		t.Fatalf("items=%#v", items)
	}
}
