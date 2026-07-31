package apidocs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedOpenAPIContainsAdditiveConversationOverviewCount(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "docs", "swagger.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}

	counts := definitionWithSuffix(t, object(t, document, "definitions"), ".OverviewProjectionCounts")
	properties := object(t, counts, "properties")
	for _, field := range []string{"groups", "contacts", "conversations", "chats", "messages", "events"} {
		if _, ok := properties[field]; !ok {
			t.Fatalf("OverviewProjectionCounts missing %q", field)
		}
	}
	canonicalDescription, _ := object(t, properties, "conversations")["description"].(string)
	if !strings.Contains(canonicalDescription, "canonical public Conversation") {
		t.Fatalf("conversations description does not identify the canonical entity count: %q", canonicalDescription)
	}
	legacyDescription, _ := object(t, properties, "chats")["description"].(string)
	if !strings.Contains(strings.ToLower(legacyDescription), "deprecated") || !strings.Contains(legacyDescription, "Conversations") {
		t.Fatalf("chats description does not identify the compatibility alias: %q", legacyDescription)
	}
}
