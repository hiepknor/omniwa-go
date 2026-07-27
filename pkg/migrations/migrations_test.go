package migrations

import (
	"strings"
	"testing"
)

func registeredMigration(t *testing.T, version int64) Migration {
	t.Helper()
	for _, migration := range registry {
		if migration.Version == version {
			return migration
		}
	}
	t.Fatalf("migration version %d is not registered", version)
	return Migration{}
}

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
	migration := registeredMigration(t, 12)
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
	migration := registeredMigration(t, 13)
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
	migration := registeredMigration(t, 14)
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
	migration := registeredMigration(t, 15)
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

func TestProjectionWorkHealthIndexIsVersionedAndPartial(t *testing.T) {
	migration := registeredMigration(t, 16)
	if migration.Version != 16 || migration.Name != "index_projection_work_health" {
		t.Fatalf("projection work health migration = %#v", migration)
	}
	for _, expected := range []string{
		"projection_event_inbox_work_health_idx", "instance_id, resource, ingested_at", "INCLUDE (status)", "status <> 'processed'",
	} {
		if !strings.Contains(migration.SQL, expected) {
			t.Fatalf("projection work health migration does not contain %q", expected)
		}
	}
}

func TestProjectionFailureOperationsMigrationIsAuditedAndTerminal(t *testing.T) {
	migration := registeredMigration(t, 17)
	if migration.Version != 17 || migration.Name != "create_projection_failure_operations" {
		t.Fatalf("projection failure operations migration = %#v", migration)
	}
	for _, expected := range []string{
		"ADD COLUMN discarded_at", "status = 'processed'", "CREATE TABLE projection_failure_audit",
		"projection_failure_audit_event_fk", "projection_failure_audit_actor_hash_check",
		"request_id VARCHAR(64) NOT NULL", "projection_event_inbox_dead_letter_admin_idx",
		"projection_event_inbox_instance_dead_letter_admin_idx", "projection_failure_audit_history_idx",
	} {
		if !strings.Contains(migration.SQL, expected) {
			t.Fatalf("projection failure operations migration does not contain %q", expected)
		}
	}
}

func TestInstanceTokenDigestMigrationIsAdditiveAndConstrained(t *testing.T) {
	migration := registeredMigration(t, 18)
	if migration.Version != 18 || migration.Name != "add_instance_token_lookup_digests" {
		t.Fatalf("instance token digest migration = %#v", migration)
	}
	for _, expected := range []string{
		"ADD COLUMN token_digest", "ADD COLUMN token_key_version", "instances_token_digest_pair_check",
		"instances_token_digest_format_check", "instances_token_digest_unique_idx", "instances_token_digest_backfill_idx",
	} {
		if !strings.Contains(migration.SQL, expected) {
			t.Fatalf("instance token digest migration does not contain %q", expected)
		}
	}
}

func TestInstanceTokenRotationMigrationUsesCASAndSafeAudit(t *testing.T) {
	migration := registeredMigration(t, 19)
	if migration.Version != 19 || migration.Name != "create_instance_token_rotation_audit" {
		t.Fatalf("instance token rotation migration = %#v", migration)
	}
	for _, expected := range []string{
		"ADD COLUMN token_generation", "ADD COLUMN token_rotated_at", "CREATE TABLE instance_token_rotation_audit",
		"new_generation = previous_generation + 1", "actor_reference_hash", "UNIQUE (instance_id, request_id)",
		"instance_token_rotation_audit_history_idx",
	} {
		if !strings.Contains(migration.SQL, expected) {
			t.Fatalf("instance token rotation migration does not contain %q", expected)
		}
	}
}

func TestInstanceTokenFallbackMigrationIsBoundedAndSecretFree(t *testing.T) {
	migration := registeredMigration(t, 20)
	if migration.Version != 20 || migration.Name != "measure_instance_token_plaintext_fallback" {
		t.Fatalf("instance token fallback migration = %#v", migration)
	}
	for _, expected := range []string{
		"CREATE TABLE instance_token_fallback_usage", "PRIMARY KEY (instance_id, key_version)",
		"lookup_count BIGINT", "first_used_at", "last_used_at", "ON DELETE CASCADE",
		"instance_token_fallback_usage_last_used_idx",
	} {
		if !strings.Contains(migration.SQL, expected) {
			t.Fatalf("instance token fallback migration does not contain %q", expected)
		}
	}
	for _, forbidden := range []string{" token ", "token_digest", "actor_reference_hash"} {
		if strings.Contains(strings.ToLower(migration.SQL), forbidden) {
			t.Fatalf("instance token fallback migration contains secret-bearing field %q", forbidden)
		}
	}
}

func TestGroupListsMigrationIsInstanceScopedAuditedAndGroupOnly(t *testing.T) {
	migration := registeredMigration(t, 21)
	if migration.Name != "create_group_lists" {
		t.Fatalf("group lists migration = %#v", migration)
	}
	for _, expected := range []string{
		"ADD COLUMN tombstone_cause", "projected_groups_tombstone_cause_check",
		"CREATE TABLE group_lists", "CREATE TABLE group_list_entries", "CREATE TABLE group_list_audit_events",
		"UNIQUE (id, instance_id)", "group_list_entries_list_fk", "group_list_audit_list_fk",
		"group_lists_active_name_unique_idx", "WHERE deleted_at IS NULL",
		"group_list_entries_jid_check", "@g[.]us", "authorization_reference_hash", "actor_reference_hash",
	} {
		if !strings.Contains(migration.SQL, expected) {
			t.Fatalf("group lists migration does not contain %q", expected)
		}
	}
	for _, forbidden := range []string{"evidence_reference VARCHAR", "authorization_reference TEXT", "actor_reference VARCHAR"} {
		if strings.Contains(migration.SQL, forbidden) {
			t.Fatalf("group lists migration stores raw authorization material: %q", forbidden)
		}
	}
}

func TestGroupCampaignContractMigrationIsAdditiveAndBackfillsDirectTargets(t *testing.T) {
	migration := registeredMigration(t, 22)
	if migration.Name != "add_group_campaign_contract" {
		t.Fatalf("group campaign migration = %#v", migration)
	}
	for _, expected := range []string{
		"ADD COLUMN target_type", "DEFAULT 'direct'", "group_list_name_snapshot", "group_list_version",
		"needs_attention", "campaigns_target_snapshot_check", "ADD COLUMN target_label",
		"campaign_recipients_target_check", "campaign_recipients_group_target_idx",
	} {
		if !strings.Contains(migration.SQL, expected) {
			t.Fatalf("group campaign migration does not contain %q", expected)
		}
	}
}

func TestGroupCampaignSafetyMigrationIsDurableScopedAndFenced(t *testing.T) {
	migration := registeredMigration(t, 23)
	if migration.Name != "add_group_campaign_delivery_safety" {
		t.Fatalf("group campaign safety migration = %#v", migration)
	}
	for _, expected := range []string{
		"CREATE TABLE campaign_group_delivery_guards", "PRIMARY KEY (instance_id, group_jid)",
		"owner_recipient_id", "claim_token", "lease_until", "last_acknowledged_at",
		"campaign_group_delivery_guards_claim_check", "CREATE TABLE campaign_instance_circuits",
		"open_until", "rate_limit_signal_count", "failure_signal_count", "ON CONFLICT (instance_id, group_jid) DO NOTHING",
	} {
		if !strings.Contains(migration.SQL, expected) {
			t.Fatalf("group campaign safety migration does not contain %q", expected)
		}
	}
}

func TestCampaignMediaAssetsMigrationIsPrivateScopedAndCleanupFenced(t *testing.T) {
	migration := registeredMigration(t, 24)
	if migration.Name != "create_campaign_media_assets" {
		t.Fatalf("migration name = %q", migration.Name)
	}
	for _, required := range []string{
		"CREATE TABLE campaign_media_assets", "instance_id UUID NOT NULL", "object_key TEXT NOT NULL",
		"campaign_media_assets_instance_identity_unique", "campaign_media_assets_object_key_unique",
		"campaign_media_assets_request_unique_idx", "request_reference_hash", "cleanup_claim_token",
		"cleanup_lease_until", "campaign_media_assets_ready_check", "campaign_media_assets_deleted_check",
		"mime_type IN ('image/jpeg', 'image/png')", "campaign_media_assets_cleanup_idx",
	} {
		if !strings.Contains(migration.SQL, required) {
			t.Fatalf("campaign media migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"presigned", "public_url", "original_filename", "file_bytes", "caption"} {
		if strings.Contains(strings.ToLower(migration.SQL), forbidden) {
			t.Fatalf("campaign media migration stores forbidden material %q", forbidden)
		}
	}
}

func TestCampaignImageContentMigrationSnapshotsPrivateAssetMetadata(t *testing.T) {
	migration := registeredMigration(t, 25)
	if migration.Name != "add_campaign_image_content_contract" {
		t.Fatalf("migration name = %q", migration.Name)
	}
	for _, required := range []string{
		"content_type IN ('text', 'image')", "media_asset_id UUID", "campaigns_media_asset_fk",
		"REFERENCES campaign_media_assets(id, instance_id) ON DELETE RESTRICT", "campaigns_content_shape_check",
		"DROP CONSTRAINT campaigns_text_body_check", "CHAR_LENGTH(text_body) <= 1024", "media_mime_type IN ('image/jpeg', 'image/png')",
		"media_sha256", "campaigns_media_asset_idx",
	} {
		if !strings.Contains(migration.SQL, required) {
			t.Fatalf("campaign image migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"object_key", "public_url", "presigned", "file_bytes"} {
		if strings.Contains(strings.ToLower(migration.SQL), forbidden) {
			t.Fatalf("campaign image migration stores forbidden material %q", forbidden)
		}
	}
}

func TestSharedMediaAssetFoundationMigrationIsPrivateScopedAndFenced(t *testing.T) {
	migration := registeredMigration(t, 26)
	if migration.Name != "create_shared_media_asset_foundation" {
		t.Fatalf("migration name = %q", migration.Name)
	}
	for _, required := range []string{
		"CREATE TABLE media_assets", "media_assets_instance_identity_unique", "ON DELETE RESTRICT",
		"CREATE TABLE media_asset_variants", "provider_original", "canonical", "media_asset_variants_object_key_unique",
		"CREATE TABLE media_asset_references", "owner_type IN ('campaign', 'message')", "media_asset_references_retention_idx",
		"CREATE TABLE media_download_jobs", "descriptor_ciphertext BYTEA", "OCTET_LENGTH(descriptor_nonce) = 12",
		"media_download_jobs_message_unique", "media_download_jobs_claim_check", "CREATE TABLE media_asset_audit_events",
		"actor_reference_hash", "jsonb_typeof(details) = 'object'", "media_assets_cleanup_idx",
	} {
		if !strings.Contains(migration.SQL, required) {
			t.Fatalf("shared media foundation migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"public_url", "presigned_url", "original_filename", "file_bytes", "caption", "media_key"} {
		if strings.Contains(strings.ToLower(migration.SQL), forbidden) {
			t.Fatalf("shared media foundation stores forbidden public or content field %q", forbidden)
		}
	}
}
