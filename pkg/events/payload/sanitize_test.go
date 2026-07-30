package payload

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSanitizeJSONRemovesCredentialsRecursively(t *testing.T) {
	raw := []byte(`{
		"event":"Message",
		"instanceToken":"instance-secret",
		"data":{"Authorization":"Bearer secret","items":[{"api_key":"key"},{"value":9007199254740993}]},
		"instanceId":"instance-a"
	}`)
	sanitized, err := SanitizeJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"instance-secret", "Bearer secret", `"api_key"`, `"instanceToken"`} {
		if strings.Contains(string(sanitized), secret) {
			t.Fatalf("sanitized payload still contains %q: %s", secret, sanitized)
		}
	}
	var decoded map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(sanitized)))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["event"] != "Message" || decoded["instanceId"] != "instance-a" {
		t.Fatalf("safe fields changed: %#v", decoded)
	}
	data := decoded["data"].(map[string]any)
	items := data["items"].([]any)
	if got := items[1].(map[string]any)["value"].(json.Number).String(); got != "9007199254740993" {
		t.Fatalf("large integer changed: %s", got)
	}
}

func TestSanitizeJSONRejectsInvalidOrScalarPayloads(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte(`not-json`), []byte(`"value"`), []byte(`{} {}`)} {
		if _, err := SanitizeJSON(raw); err == nil {
			t.Fatalf("SanitizeJSON(%q) succeeded", raw)
		}
	}
}
