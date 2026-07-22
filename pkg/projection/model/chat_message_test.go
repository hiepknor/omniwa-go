package projection_model

import "testing"

func TestChatMessageProjectionTablesAndEnums(t *testing.T) {
	if (Chat{}).TableName() != "projected_chats" || (ProjectedMessage{}).TableName() != "projected_messages" || (MessageReceipt{}).TableName() != "projected_message_receipts" {
		t.Fatalf("unexpected projection table names: %q, %q, %q", (Chat{}).TableName(), (ProjectedMessage{}).TableName(), (MessageReceipt{}).TableName())
	}
	for name, value := range map[string]string{
		"direct": string(ChatTypeDirect), "group": string(ChatTypeGroup), "incoming": string(MessageDirectionIncoming),
		"outgoing": string(MessageDirectionOutgoing), "history": string(MessageProvenanceHistorySync), "write-through": string(MessageProvenanceWriteThrough),
	} {
		if value == "" {
			t.Fatalf("empty %s enum", name)
		}
	}
}
