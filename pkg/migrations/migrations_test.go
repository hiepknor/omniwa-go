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
	migration := registry[7]
	if migration.Version != 8 || migration.Name != "create_chats_messages_projection" {
		t.Fatalf("chats/messages migration = %#v", migration)
	}
	for _, expected := range []string{
		"CREATE TABLE projected_chats", "CREATE TABLE projected_messages", "CREATE TABLE projected_message_receipts",
		"projected_messages_history_idx", "projected_messages_retention_idx",
	} {
		if !strings.Contains(migration.SQL, expected) {
			t.Fatalf("chats/messages migration does not contain %q", expected)
		}
	}
}

func TestMessageRetentionCutoffIndexIsVersioned(t *testing.T) {
	migration := registry[8]
	if migration.Version != 9 || migration.Name != "index_message_retention_cutoff" ||
		!strings.Contains(migration.SQL, "projected_messages_retention_cutoff_idx") ||
		!strings.Contains(migration.SQL, "projection_event_inbox_message_retention_idx") {
		t.Fatalf("message retention migration = %#v", migration)
	}
}

func TestDurableEventsMigrationIsVersionedAndIndexed(t *testing.T) {
	migration := registry[9]
	if migration.Version != 10 || migration.Name != "create_durable_events" || !strings.Contains(migration.SQL, "CREATE TABLE durable_events") ||
		!strings.Contains(migration.SQL, "durable_events_history_idx") || !strings.Contains(migration.SQL, "durable_events_retention_idx") {
		t.Fatalf("durable events migration = %#v", migration)
	}
}

func TestProjectionOverviewWindowIndexesAreVersioned(t *testing.T) {
	migration := registry[10]
	if migration.Version != 11 || migration.Name != "index_projection_overview_windows" ||
		!strings.Contains(migration.SQL, "projected_messages_overview_window_idx") || !strings.Contains(migration.SQL, "durable_events_overview_window_idx") {
		t.Fatalf("projection overview migration = %#v", migration)
	}
}

func TestCampaignPersistenceMigrationIsVersionedAndConsentBound(t *testing.T) {
	migration := registry[len(registry)-4]
	if migration.Version != 12 || migration.Name != "create_campaign_persistence" {
		t.Fatalf("campaign migration = %#v", migration)
	}
	for _, expected := range []string{
		"CREATE TABLE campaigns", "CREATE TABLE campaign_recipients", "CREATE TABLE campaign_audit_events",
		"campaign_recipients_work_idx", "opt_in_reference_hash", "campaign_recipients_opt_in_hash_check",
	} {
		if !strings.Contains(migration.SQL, expected) {
			t.Fatalf("campaign migration does not contain %q", expected)
		}
	}
}

func TestContactsSearchIndexesAreVersioned(t *testing.T) {
	migration := registry[len(registry)-3]
	if migration.Version != 13 || migration.Name != "index_contacts_projection_search" {
		t.Fatalf("contacts search migration = %#v", migration)
	}
	for _, expected := range []string{
		"projected_contacts_search_sort_idx", "projected_contacts_search_jid_idx", "projected_contacts_search_full_name_idx",
		"text_pattern_ops", "WHERE tombstoned_at IS NULL",
	} {
		if !strings.Contains(migration.SQL, expected) {
			t.Fatalf("contacts search migration does not contain %q", expected)
		}
	}
}

func TestGroupsSearchIndexesAreVersioned(t *testing.T) {
	migration := registry[len(registry)-2]
	if migration.Version != 14 || migration.Name != "index_groups_projection_search" {
		t.Fatalf("groups search migration = %#v", migration)
	}
	for _, expected := range []string{"projected_groups_search_page_idx", "projected_groups_search_jid_idx", "projected_groups_search_name_idx", "text_pattern_ops", "WHERE tombstoned_at IS NULL"} {
		if !strings.Contains(migration.SQL, expected) {
			t.Fatalf("groups search migration does not contain %q", expected)
		}
	}
}

func TestProjectionFailureMetadataMigrationIsAdditiveAndIndexed(t *testing.T) {
	migration := registry[len(registry)-1]
	if migration.Version != 15 || migration.Name != "add_projection_event_failure_metadata" {
		t.Fatalf("projection failure migration = %#v", migration)
	}
	for _, expected := range []string{
		"ADD COLUMN last_attempt_at", "ADD COLUMN failure_class", "ADD COLUMN retry_policy_version",
		"ADD COLUMN max_attempts", "ADD COLUMN dead_lettered_at", "'dead_letter'",
		"projection_event_inbox_dead_letter_idx", "projection_event_inbox_health_idx",
	} {
		if !strings.Contains(migration.SQL, expected) {
			t.Fatalf("projection failure migration does not contain %q", expected)
		}
	}
}
