package apidocs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedOpenAPIContainsAdditiveConversationContract(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "swagger.json")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	paths := object(t, document, "paths")
	wantedOperations := map[string]string{
		"/conversations":                            "listConversations",
		"/conversations/{conversationRef}":          "getConversation",
		"/conversations/{conversationRef}/messages": "listConversationMessages",
	}
	operationIDs := map[string]bool{}
	for path, rawPath := range paths {
		pathItem, ok := rawPath.(map[string]any)
		if !ok {
			continue
		}
		for method, rawOperation := range pathItem {
			operation, ok := rawOperation.(map[string]any)
			if !ok {
				continue
			}
			operationID, _ := operation["operationId"].(string)
			if operationID == "" {
				continue
			}
			if operationIDs[operationID] {
				t.Fatalf("duplicate operationId %q at %s %s", operationID, method, path)
			}
			operationIDs[operationID] = true
		}
	}
	for path, operationID := range wantedOperations {
		operation := object(t, object(t, paths, path), "get")
		if operation["operationId"] != operationID {
			t.Fatalf("%s operationId=%v want=%q", path, operation["operationId"], operationID)
		}
		responses := object(t, operation, "responses")
		for _, status := range []string{"200", "500", "503"} {
			if _, ok := responses[status]; !ok {
				t.Fatalf("%s missing response %s", path, status)
			}
		}
	}
	for path, statuses := range map[string][]string{
		"/conversations":                            {"400"},
		"/conversations/{conversationRef}":          {"404"},
		"/conversations/{conversationRef}/messages": {"400", "404"},
	} {
		responses := object(t, object(t, object(t, paths, path), "get"), "responses")
		for _, status := range statuses {
			if _, ok := responses[status]; !ok {
				t.Fatalf("%s missing response %s", path, status)
			}
		}
	}
	for _, path := range []string{"/chat/list", "/chat/info/{chatId}", "/chat/{chatId}/messages"} {
		operation := object(t, object(t, paths, path), "get")
		if operation["deprecated"] != true {
			t.Fatalf("legacy Chat read %s is not deprecated", path)
		}
	}

	definitions := object(t, document, "definitions")
	conversation := definitionWithSuffix(t, definitions, ".ProjectedConversation")
	assertRequired(t, conversation, "conversationId", "type", "unreadCount")
	conversationProperties := object(t, conversation, "properties")
	for _, field := range []string{"conversationId", "type", "unreadCount", "aliases", "addressingJid"} {
		if _, ok := conversationProperties[field]; !ok {
			t.Fatalf("ProjectedConversation missing %q", field)
		}
	}
	for _, legacyField := range []string{"chatId", "chatAliases"} {
		if _, ok := conversationProperties[legacyField]; ok {
			t.Fatalf("ProjectedConversation exposes legacy field %q", legacyField)
		}
	}

	message := definitionWithSuffix(t, definitions, ".ProjectedConversationMessage")
	assertRequired(t, message, "messageId", "conversationId", "direction", "messageType", "providerTimestamp", "provenance")
	messageProperties := object(t, message, "properties")
	if _, ok := messageProperties["providerChatId"]; !ok {
		t.Fatal("ProjectedConversationMessage missing providerChatId provenance")
	}
	if _, ok := messageProperties["chatId"]; ok {
		t.Fatal("ProjectedConversationMessage exposes legacy chatId identity")
	}

	legacyChat := definitionWithSuffix(t, definitions, ".ProjectedChat")
	assertRequired(t, legacyChat, "chatId", "type", "unreadCount")
	legacyMessage := definitionWithSuffix(t, definitions, ".ProjectedMessage")
	assertRequired(t, legacyMessage, "chatId", "messageId", "direction", "messageType", "providerTimestamp", "provenance")

	capabilities := definitionWithSuffix(t, definitions, ".CapabilitiesData")
	capabilityItems := object(t, object(t, capabilities, "properties"), "capabilities")
	example, ok := capabilityItems["example"].([]any)
	if !ok {
		t.Fatalf("capability example=%T", capabilityItems["example"])
	}
	seen := map[string]bool{}
	for _, value := range example {
		if capability, ok := value.(string); ok {
			seen[capability] = true
		}
	}
	for _, capability := range []string{"canonical_chat_identity", "canonical_conversation_identity"} {
		if !seen[capability] {
			t.Fatalf("capability example missing %q", capability)
		}
	}
}

func object(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%q=%T, want object", key, parent[key])
	}
	return value
}

func definitionWithSuffix(t *testing.T, definitions map[string]any, suffix string) map[string]any {
	t.Helper()
	for name, value := range definitions {
		if strings.HasSuffix(name, suffix) {
			definition, ok := value.(map[string]any)
			if !ok {
				t.Fatalf("definition %q=%T", name, value)
			}
			return definition
		}
	}
	t.Fatalf("definition with suffix %q not found", suffix)
	return nil
}

func assertRequired(t *testing.T, definition map[string]any, fields ...string) {
	t.Helper()
	requiredValues, ok := definition["required"].([]any)
	if !ok {
		t.Fatalf("required=%T", definition["required"])
	}
	required := map[string]bool{}
	for _, value := range requiredValues {
		if field, ok := value.(string); ok {
			required[field] = true
		}
	}
	for _, field := range fields {
		if !required[field] {
			t.Fatalf("required fields missing %q: %v", field, requiredValues)
		}
	}
}
