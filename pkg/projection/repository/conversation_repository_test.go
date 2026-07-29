package projection_repository

import (
	"testing"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
)

func TestDeterministicConversationIdentityKeepsProviderTypesIsolated(t *testing.T) {
	contactID := "845c98ac-89b4-46be-9b83-1120c812cec3"
	phone := projection_model.Chat{InstanceID: "instance-a", ChatID: "15551234567@s.whatsapp.net", ContactID: &contactID, Type: projection_model.ChatTypeDirect}
	lid := projection_model.Chat{InstanceID: "instance-a", ChatID: "900000001234@lid", ContactID: &contactID, Type: projection_model.ChatTypeDirect}
	phoneID, _ := deterministicConversationIdentity(&phone)
	lidID, _ := deterministicConversationIdentity(&lid)
	if phoneID != lidID {
		t.Fatalf("authoritative direct aliases did not share identity: %q != %q", phoneID, lidID)
	}

	seen := map[string]projection_model.ChatType{}
	for _, chat := range []projection_model.Chat{
		{InstanceID: "instance-a", ChatID: "provider-id", Type: projection_model.ChatTypeGroup},
		{InstanceID: "instance-a", ChatID: "provider-id", Type: projection_model.ChatTypeNewsletter},
		{InstanceID: "instance-a", ChatID: "provider-id", Type: projection_model.ChatTypeBroadcast},
		{InstanceID: "instance-a", ChatID: "provider-id", Type: projection_model.ChatTypeUnknown},
	} {
		conversationID, _ := deterministicConversationIdentity(&chat)
		if previous, exists := seen[conversationID]; exists {
			t.Fatalf("provider types %q and %q shared identity %q", previous, chat.Type, conversationID)
		}
		seen[conversationID] = chat.Type
	}

	crossInstance := phone
	crossInstance.InstanceID = "instance-b"
	crossInstanceID, _ := deterministicConversationIdentity(&crossInstance)
	if crossInstanceID == phoneID {
		t.Fatal("canonical direct conversation identity crossed instances")
	}
}
