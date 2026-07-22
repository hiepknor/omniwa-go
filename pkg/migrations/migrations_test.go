package migrations

import (
	"strings"
	"testing"
)

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

func TestContactsProjectionMigrationIsVersionedAndIncludesAliases(t *testing.T) {
	contacts := registry[6]
	if contacts.Version != 7 || contacts.Name != "create_contacts_projection" {
		t.Fatalf("contacts migration = %#v", contacts)
	}
	for _, table := range []string{"projected_contacts", "projected_contact_identities"} {
		if !strings.Contains(contacts.SQL, "CREATE TABLE "+table) {
			t.Fatalf("contacts migration does not create %s", table)
		}
	}
}

func TestChatsMessagesProjectionMigrationIsVersionedAndIndexed(t *testing.T) {
	last := registry[len(registry)-1]
	if last.Version != 8 || last.Name != "create_chats_messages_projection" {
		t.Fatalf("last migration = %#v", last)
	}
	for _, expected := range []string{
		"CREATE TABLE projected_chats", "CREATE TABLE projected_messages", "CREATE TABLE projected_message_receipts",
		"projected_messages_history_idx", "projected_messages_retention_idx",
	} {
		if !strings.Contains(last.SQL, expected) {
			t.Fatalf("chats/messages migration does not contain %q", expected)
		}
	}
}
