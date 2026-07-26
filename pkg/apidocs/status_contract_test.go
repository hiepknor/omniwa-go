package apidocs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGeneratedStatusSchemaRequiresConfiguredIdentityWithExactCasing(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate contract test")
	}
	swaggerPath := filepath.Join(filepath.Dir(filename), "..", "..", "docs", "swagger.json")
	contents, err := os.ReadFile(swaggerPath)
	if err != nil {
		t.Fatalf("read generated Swagger: %v", err)
	}

	var document struct {
		Definitions map[string]struct {
			Required   []string                   `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatalf("decode generated Swagger: %v", err)
	}
	schema, ok := document.Definitions["apidocs.StatusData"]
	if !ok {
		t.Fatal("apidocs.StatusData is missing from generated Swagger")
	}
	for _, field := range []string{"InstanceId", "InstanceName"} {
		if _, ok := schema.Properties[field]; !ok {
			t.Fatalf("property %q is missing or has incorrect JSON casing", field)
		}
		if !containsStatusContractField(schema.Required, field) {
			t.Fatalf("property %q is not required: %v", field, schema.Required)
		}
	}
	for _, incorrect := range []string{"instanceId", "instanceID", "instanceName"} {
		if _, ok := schema.Properties[incorrect]; ok {
			t.Fatalf("unexpected incorrectly-cased property %q", incorrect)
		}
	}
}

func containsStatusContractField(fields []string, target string) bool {
	for _, field := range fields {
		if field == target {
			return true
		}
	}
	return false
}
