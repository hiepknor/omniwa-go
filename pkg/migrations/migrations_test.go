package migrations

import "testing"

func TestRegistryIsOrderedAndImmutableInputIsChecksummed(t *testing.T) {
	if err := validateRegistry(registry); err != nil {
		t.Fatal(err)
	}
	first := migrationChecksum(registry[0])
	changed := registry[0]
	changed.SQL += " "
	if first == migrationChecksum(changed) {
		t.Fatal("checksum did not change with migration content")
	}
}

func TestRegistryRejectsDuplicateAndOutOfOrderVersions(t *testing.T) {
	migration := Migration{Version: 1, Name: "one", SQL: "SELECT 1"}
	if err := validateRegistry([]Migration{migration, migration}); err == nil {
		t.Fatal("duplicate version was accepted")
	}
	second := Migration{Version: 2, Name: "two", SQL: "SELECT 2"}
	if err := validateRegistry([]Migration{second, migration}); err == nil {
		t.Fatal("out-of-order registry was accepted")
	}
}
