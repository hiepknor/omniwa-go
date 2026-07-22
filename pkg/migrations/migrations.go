// Package migrations applies ordered, immutable PostgreSQL schema migrations.
package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"
)

const advisoryLockKey int64 = 0x4f4d4e495741 // "OMNIWA"

type Migration struct {
	Version int64
	Name    string
	SQL     string
}

type appliedMigration struct {
	Version   int64     `gorm:"primaryKey"`
	Name      string    `gorm:"not null"`
	Checksum  string    `gorm:"not null"`
	AppliedAt time.Time `gorm:"not null"`
}

func (appliedMigration) TableName() string { return "schema_migrations" }

var registry = []Migration{
	{
		Version: 1,
		Name:    "create_projection_states",
		SQL: `CREATE TABLE projection_states (
    instance_id UUID NOT NULL,
    resource VARCHAR(64) NOT NULL,
    sync_status VARCHAR(32) NOT NULL DEFAULT 'not_started',
    last_event_at TIMESTAMPTZ NULL,
    last_reconciled_at TIMESTAMPTZ NULL,
    stale_since TIMESTAMPTZ NULL,
    schema_version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (instance_id, resource),
    CONSTRAINT projection_states_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE,
    CONSTRAINT projection_states_status_check CHECK (sync_status IN ('not_started', 'syncing', 'ready', 'stale', 'failed'))
);
CREATE INDEX projection_states_status_idx ON projection_states (sync_status, updated_at);`,
	},
	{
		Version: 2,
		Name:    "create_projection_event_inbox",
		SQL: `CREATE TABLE projection_event_inbox (
    instance_id UUID NOT NULL,
    resource VARCHAR(64) NOT NULL,
    event_key VARCHAR(255) NOT NULL,
    entity_key VARCHAR(255) NOT NULL,
    event_type VARCHAR(64) NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    ingested_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    payload JSONB NOT NULL,
    claim_token VARCHAR(64) NULL,
    lease_until TIMESTAMPTZ NULL,
    processed_at TIMESTAMPTZ NULL,
    retry_count INTEGER NOT NULL DEFAULT 0,
    last_error_code VARCHAR(64) NULL,
    PRIMARY KEY (instance_id, resource, event_key),
    CONSTRAINT projection_event_inbox_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE,
    CONSTRAINT projection_event_inbox_status_check CHECK (status IN ('pending', 'processing', 'processed', 'failed')),
    CONSTRAINT projection_event_inbox_retry_count_check CHECK (retry_count >= 0)
);
CREATE INDEX projection_event_inbox_work_idx ON projection_event_inbox (available_at, occurred_at, ingested_at)
    WHERE status IN ('pending', 'failed');
CREATE INDEX projection_event_inbox_expired_lease_idx ON projection_event_inbox (lease_until)
    WHERE status = 'processing';`,
	},
	{
		Version: 3,
		Name:    "create_groups_projection",
		SQL: `CREATE TABLE projected_groups (
    instance_id UUID NOT NULL,
    group_id VARCHAR(255) NOT NULL,
    name TEXT NULL,
    topic TEXT NULL,
    owner_jid VARCHAR(255) NULL,
    owner_phone_jid VARCHAR(255) NULL,
    locked BOOLEAN NULL,
    announce BOOLEAN NULL,
    ephemeral_enabled BOOLEAN NULL,
    ephemeral_timer BIGINT NULL,
    join_approval_required BOOLEAN NULL,
    suspended BOOLEAN NULL,
    participant_version VARCHAR(255) NULL,
    addressing_mode VARCHAR(32) NULL,
    member_add_mode VARCHAR(32) NULL,
    parent_group_id VARCHAR(255) NULL,
    is_parent BOOLEAN NULL,
    is_default_subgroup BOOLEAN NULL,
    invite_link TEXT NULL,
    invite_link_updated_at TIMESTAMPTZ NULL,
    provider_created_at TIMESTAMPTZ NULL,
    source_occurred_at TIMESTAMPTZ NOT NULL,
    source_event_key VARCHAR(255) NOT NULL,
    last_synced_at TIMESTAMPTZ NOT NULL,
    tombstoned_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (instance_id, group_id),
    CONSTRAINT projected_groups_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE,
    CONSTRAINT projected_groups_ephemeral_timer_check CHECK (ephemeral_timer IS NULL OR ephemeral_timer >= 0)
);
CREATE INDEX projected_groups_list_idx ON projected_groups (instance_id, name, group_id) WHERE tombstoned_at IS NULL;
CREATE INDEX projected_groups_freshness_idx ON projected_groups (instance_id, last_synced_at);

CREATE TABLE projected_group_participants (
    instance_id UUID NOT NULL,
    group_id VARCHAR(255) NOT NULL,
    participant_id VARCHAR(255) NOT NULL,
    phone_number_jid VARCHAR(255) NULL,
    lid VARCHAR(255) NULL,
    display_name TEXT NULL,
    role VARCHAR(32) NOT NULL DEFAULT 'member',
    source_occurred_at TIMESTAMPTZ NOT NULL,
    source_event_key VARCHAR(255) NOT NULL,
    last_synced_at TIMESTAMPTZ NOT NULL,
    tombstoned_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (instance_id, group_id, participant_id),
    CONSTRAINT projected_group_participants_group_fk FOREIGN KEY (instance_id, group_id) REFERENCES projected_groups(instance_id, group_id) ON DELETE CASCADE,
    CONSTRAINT projected_group_participants_role_check CHECK (role IN ('member', 'admin', 'super_admin'))
);
CREATE INDEX projected_group_participants_list_idx ON projected_group_participants (instance_id, group_id, role, participant_id) WHERE tombstoned_at IS NULL;`,
	},
	{
		Version: 4,
		Name:    "add_group_field_versions",
		SQL: `ALTER TABLE projected_groups ADD COLUMN field_versions JSONB NOT NULL DEFAULT '{}'::jsonb;
UPDATE projected_groups
SET field_versions = jsonb_build_object(
    '_snapshot', jsonb_build_object('occurredAt', source_occurred_at, 'eventKey', source_event_key)
);`,
	},
	{
		Version: 5,
		Name:    "complete_group_read_model",
		SQL: `ALTER TABLE projected_groups
    ADD COLUMN name_set_at TIMESTAMPTZ NULL,
    ADD COLUMN name_set_by VARCHAR(255) NULL,
    ADD COLUMN name_set_by_phone VARCHAR(255) NULL,
    ADD COLUMN topic_id VARCHAR(255) NULL,
    ADD COLUMN topic_set_at TIMESTAMPTZ NULL,
    ADD COLUMN topic_set_by VARCHAR(255) NULL,
    ADD COLUMN topic_set_by_phone VARCHAR(255) NULL,
    ADD COLUMN topic_deleted BOOLEAN NULL,
    ADD COLUMN announce_version VARCHAR(255) NULL,
    ADD COLUMN incognito BOOLEAN NULL,
    ADD COLUMN creator_country_code VARCHAR(32) NULL,
    ADD COLUMN participant_count INTEGER NULL,
    ADD COLUMN default_membership_approval_mode VARCHAR(64) NULL,
		    ADD CONSTRAINT projected_groups_participant_count_check CHECK (participant_count IS NULL OR participant_count >= 0);`,
	},
	{
		Version: 6,
		Name:    "create_labels_projection",
		SQL: `CREATE TABLE projected_labels (
    instance_id UUID NOT NULL,
    label_id VARCHAR(255) NOT NULL,
    name TEXT NULL,
    color INTEGER NULL,
    predefined_id INTEGER NULL,
    order_index INTEGER NULL,
    active BOOLEAN NULL,
    immutable BOOLEAN NULL,
    kind VARCHAR(64) NULL,
    source_occurred_at TIMESTAMPTZ NOT NULL,
    source_event_key VARCHAR(255) NOT NULL,
    last_synced_at TIMESTAMPTZ NOT NULL,
    tombstoned_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (instance_id, label_id),
    CONSTRAINT projected_labels_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE
);
CREATE INDEX projected_labels_list_idx ON projected_labels (instance_id, order_index, label_id) WHERE tombstoned_at IS NULL;

CREATE TABLE projected_label_chat_associations (
    instance_id UUID NOT NULL,
    label_id VARCHAR(255) NOT NULL,
    chat_id VARCHAR(255) NOT NULL,
    source_occurred_at TIMESTAMPTZ NOT NULL,
    source_event_key VARCHAR(255) NOT NULL,
    last_synced_at TIMESTAMPTZ NOT NULL,
    tombstoned_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (instance_id, label_id, chat_id),
    CONSTRAINT projected_label_chats_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE
);
CREATE INDEX projected_label_chats_by_chat_idx ON projected_label_chat_associations (instance_id, chat_id, label_id) WHERE tombstoned_at IS NULL;

CREATE TABLE projected_label_message_associations (
    instance_id UUID NOT NULL,
    label_id VARCHAR(255) NOT NULL,
    chat_id VARCHAR(255) NOT NULL,
    message_id VARCHAR(255) NOT NULL,
    source_occurred_at TIMESTAMPTZ NOT NULL,
    source_event_key VARCHAR(255) NOT NULL,
    last_synced_at TIMESTAMPTZ NOT NULL,
    tombstoned_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (instance_id, label_id, chat_id, message_id),
    CONSTRAINT projected_label_messages_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE
);
CREATE INDEX projected_label_messages_by_message_idx ON projected_label_message_associations (instance_id, chat_id, message_id, label_id) WHERE tombstoned_at IS NULL;`,
	},
	{
		Version: 7,
		Name:    "create_contacts_projection",
		SQL: `CREATE TABLE projected_contacts (
    instance_id UUID NOT NULL,
    contact_id UUID NOT NULL,
    preferred_jid VARCHAR(255) NOT NULL,
    phone_jid VARCHAR(255) NULL,
    lid VARCHAR(255) NULL,
    username VARCHAR(255) NULL,
    found BOOLEAN NOT NULL DEFAULT FALSE,
    first_name TEXT NULL,
    full_name TEXT NULL,
    push_name TEXT NULL,
    business_name TEXT NULL,
    redacted_phone TEXT NULL,
    save_on_primary_addressbook BOOLEAN NULL,
    picture_id VARCHAR(255) NULL,
    picture_author_jid VARCHAR(255) NULL,
    picture_removed BOOLEAN NULL,
    picture_updated_at TIMESTAMPTZ NULL,
    about TEXT NULL,
    about_updated_at TIMESTAMPTZ NULL,
    source_occurred_at TIMESTAMPTZ NOT NULL,
    source_event_key VARCHAR(255) NOT NULL,
    field_versions JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_synced_at TIMESTAMPTZ NOT NULL,
    tombstoned_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (instance_id, contact_id),
    CONSTRAINT projected_contacts_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX projected_contacts_preferred_jid_idx ON projected_contacts (instance_id, preferred_jid) WHERE tombstoned_at IS NULL;
CREATE INDEX projected_contacts_list_idx ON projected_contacts (instance_id, full_name, preferred_jid) WHERE tombstoned_at IS NULL;
CREATE INDEX projected_contacts_freshness_idx ON projected_contacts (instance_id, last_synced_at);

CREATE TABLE projected_contact_identities (
    instance_id UUID NOT NULL,
    identity_kind VARCHAR(32) NOT NULL,
    identity_value VARCHAR(255) NOT NULL,
    contact_id UUID NOT NULL,
    source_occurred_at TIMESTAMPTZ NOT NULL,
    source_event_key VARCHAR(255) NOT NULL,
    last_synced_at TIMESTAMPTZ NOT NULL,
    tombstoned_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (instance_id, identity_kind, identity_value),
    CONSTRAINT projected_contact_identities_contact_fk FOREIGN KEY (instance_id, contact_id) REFERENCES projected_contacts(instance_id, contact_id) ON DELETE CASCADE,
    CONSTRAINT projected_contact_identities_kind_check CHECK (identity_kind IN ('jid', 'phone_jid', 'lid', 'username'))
);
CREATE INDEX projected_contact_identities_contact_idx ON projected_contact_identities (instance_id, contact_id) WHERE tombstoned_at IS NULL;`,
	},
	{
		Version: 8,
		Name:    "create_chats_messages_projection",
		SQL: `CREATE TABLE projected_chats (
    instance_id UUID NOT NULL,
    chat_id VARCHAR(255) NOT NULL,
    contact_id UUID NULL,
    chat_type VARCHAR(32) NOT NULL,
    display_name TEXT NULL,
    last_message_id VARCHAR(255) NULL,
    last_message_at TIMESTAMPTZ NULL,
    last_activity_at TIMESTAMPTZ NULL,
    unread_count INTEGER NOT NULL DEFAULT 0,
    archived BOOLEAN NULL,
    pinned BOOLEAN NULL,
    muted_until TIMESTAMPTZ NULL,
    disappearing_timer BIGINT NULL,
    source_occurred_at TIMESTAMPTZ NOT NULL,
    source_event_key VARCHAR(255) NOT NULL,
    field_versions JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_synced_at TIMESTAMPTZ NOT NULL,
    tombstoned_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (instance_id, chat_id),
    CONSTRAINT projected_chats_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE,
    CONSTRAINT projected_chats_type_check CHECK (chat_type IN ('direct', 'group', 'newsletter', 'broadcast', 'unknown')),
    CONSTRAINT projected_chats_unread_count_check CHECK (unread_count >= 0),
    CONSTRAINT projected_chats_disappearing_timer_check CHECK (disappearing_timer IS NULL OR disappearing_timer >= 0)
);
CREATE INDEX projected_chats_list_idx ON projected_chats (instance_id, last_activity_at DESC NULLS LAST, chat_id DESC) WHERE tombstoned_at IS NULL;
CREATE INDEX projected_chats_contact_idx ON projected_chats (instance_id, contact_id) WHERE contact_id IS NOT NULL AND tombstoned_at IS NULL;

CREATE TABLE projected_messages (
    instance_id UUID NOT NULL,
    message_id VARCHAR(255) NOT NULL,
    chat_id VARCHAR(255) NOT NULL,
    sender_jid VARCHAR(255) NULL,
    recipient_jid VARCHAR(255) NULL,
    participant_jid VARCHAR(255) NULL,
    direction VARCHAR(32) NOT NULL,
    message_type VARCHAR(64) NOT NULL,
    content_text TEXT NULL,
    caption TEXT NULL,
    content_summary TEXT NULL,
    quoted_message_id VARCHAR(255) NULL,
    media_type VARCHAR(64) NULL,
    media_mime_type VARCHAR(255) NULL,
    media_file_name TEXT NULL,
    media_size BIGINT NULL,
    media_duration_seconds BIGINT NULL,
    media_width BIGINT NULL,
    media_height BIGINT NULL,
    media_object_key TEXT NULL,
    status VARCHAR(32) NULL,
    provider_timestamp TIMESTAMPTZ NOT NULL,
    sent_at TIMESTAMPTZ NULL,
    delivered_at TIMESTAMPTZ NULL,
    read_at TIMESTAMPTZ NULL,
    played_at TIMESTAMPTZ NULL,
    provenance VARCHAR(32) NOT NULL,
    history_sync_id VARCHAR(255) NULL,
    retention_expires_at TIMESTAMPTZ NULL,
    deleted_at TIMESTAMPTZ NULL,
    source_occurred_at TIMESTAMPTZ NOT NULL,
    source_event_key VARCHAR(255) NOT NULL,
    field_versions JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_synced_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (instance_id, message_id),
    CONSTRAINT projected_messages_chat_fk FOREIGN KEY (instance_id, chat_id) REFERENCES projected_chats(instance_id, chat_id) ON DELETE CASCADE,
    CONSTRAINT projected_messages_direction_check CHECK (direction IN ('incoming', 'outgoing', 'system')),
    CONSTRAINT projected_messages_provenance_check CHECK (provenance IN ('live', 'history_sync', 'write_through')),
    CONSTRAINT projected_messages_media_size_check CHECK (media_size IS NULL OR media_size >= 0),
    CONSTRAINT projected_messages_media_duration_check CHECK (media_duration_seconds IS NULL OR media_duration_seconds >= 0),
    CONSTRAINT projected_messages_media_width_check CHECK (media_width IS NULL OR media_width >= 0),
    CONSTRAINT projected_messages_media_height_check CHECK (media_height IS NULL OR media_height >= 0)
);
CREATE INDEX projected_messages_history_idx ON projected_messages (instance_id, chat_id, provider_timestamp DESC, message_id DESC) WHERE deleted_at IS NULL;
CREATE INDEX projected_messages_sender_idx ON projected_messages (instance_id, sender_jid, provider_timestamp DESC) WHERE deleted_at IS NULL;
CREATE INDEX projected_messages_retention_idx ON projected_messages (retention_expires_at) WHERE retention_expires_at IS NOT NULL AND deleted_at IS NULL;

CREATE TABLE projected_message_receipts (
    instance_id UUID NOT NULL,
    message_id VARCHAR(255) NOT NULL,
    recipient_jid VARCHAR(255) NOT NULL,
    receipt_type VARCHAR(32) NOT NULL,
    receipt_at TIMESTAMPTZ NOT NULL,
    source_occurred_at TIMESTAMPTZ NOT NULL,
    source_event_key VARCHAR(255) NOT NULL,
    last_synced_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (instance_id, message_id, recipient_jid, receipt_type),
    CONSTRAINT projected_message_receipts_message_fk FOREIGN KEY (instance_id, message_id) REFERENCES projected_messages(instance_id, message_id) ON DELETE CASCADE,
    CONSTRAINT projected_message_receipts_type_check CHECK (receipt_type IN ('sent', 'delivered', 'read', 'played', 'error'))
);
CREATE INDEX projected_message_receipts_history_idx ON projected_message_receipts (instance_id, message_id, receipt_at ASC, recipient_jid ASC, receipt_type ASC);`,
	},
	{
		Version: 9,
		Name:    "index_message_retention_cutoff",
		SQL: `CREATE INDEX projected_messages_retention_cutoff_idx
ON projected_messages (provider_timestamp ASC, instance_id ASC, message_id ASC);
CREATE INDEX projection_event_inbox_message_retention_idx
ON projection_event_inbox (occurred_at ASC, ingested_at ASC, event_key ASC)
WHERE resource = 'messages' AND event_type IN ('message', 'history_message', 'receipt');`,
	},
	{
		Version: 10,
		Name:    "create_durable_events",
		SQL: `CREATE TABLE durable_events (
    id UUID PRIMARY KEY,
    instance_id UUID NOT NULL,
    event_type VARCHAR(64) NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    ingested_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    summary JSONB NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT durable_events_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE
);
CREATE INDEX durable_events_history_idx ON durable_events (instance_id, occurred_at DESC, id DESC);
CREATE INDEX durable_events_retention_idx ON durable_events (expires_at ASC, id ASC);`,
	},
	{
		Version: 11,
		Name:    "index_projection_overview_windows",
		SQL: `CREATE INDEX projected_messages_overview_window_idx
ON projected_messages (provider_timestamp ASC, direction)
WHERE deleted_at IS NULL;
CREATE INDEX durable_events_overview_window_idx
ON durable_events (occurred_at ASC);`,
	},
	{
		Version: 12,
		Name:    "create_campaign_persistence",
		SQL: `CREATE TABLE campaigns (
    id UUID PRIMARY KEY,
    instance_id UUID NOT NULL,
    name VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'draft',
    content_type VARCHAR(32) NOT NULL,
    text_body TEXT NOT NULL,
    starts_at TIMESTAMPTZ NULL,
    finished_at TIMESTAMPTZ NULL,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT campaigns_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE,
    CONSTRAINT campaigns_instance_identity_unique UNIQUE (id, instance_id),
    CONSTRAINT campaigns_status_check CHECK (status IN ('draft', 'scheduled', 'running', 'paused', 'completed', 'aborted', 'failed')),
    CONSTRAINT campaigns_content_type_check CHECK (content_type = 'text'),
    CONSTRAINT campaigns_text_body_check CHECK (char_length(text_body) BETWEEN 1 AND 4096),
    CONSTRAINT campaigns_version_check CHECK (version >= 1),
    CONSTRAINT campaigns_schedule_check CHECK (status <> 'scheduled' OR starts_at IS NOT NULL)
);
CREATE INDEX campaigns_instance_status_idx ON campaigns (instance_id, status, starts_at, id);

CREATE TABLE campaign_recipients (
    id UUID PRIMARY KEY,
    campaign_id UUID NOT NULL,
    instance_id UUID NOT NULL,
    recipient_jid VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    opt_in_source VARCHAR(64) NOT NULL,
    opt_in_reference_hash VARCHAR(64) NOT NULL,
    opted_in_at TIMESTAMPTZ NOT NULL,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    claim_token VARCHAR(64) NULL,
    lease_until TIMESTAMPTZ NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    provider_message_id VARCHAR(255) NULL,
    sent_at TIMESTAMPTZ NULL,
    delivered_at TIMESTAMPTZ NULL,
    read_at TIMESTAMPTZ NULL,
    last_error_code VARCHAR(64) NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT campaign_recipients_campaign_fk FOREIGN KEY (campaign_id, instance_id) REFERENCES campaigns(id, instance_id) ON DELETE CASCADE,
    CONSTRAINT campaign_recipients_identity_unique UNIQUE (campaign_id, recipient_jid),
    CONSTRAINT campaign_recipients_campaign_identity_unique UNIQUE (id, campaign_id, instance_id),
    CONSTRAINT campaign_recipients_status_check CHECK (status IN ('pending', 'processing', 'sent', 'delivered', 'read', 'failed', 'skipped', 'aborted')),
    CONSTRAINT campaign_recipients_attempt_count_check CHECK (attempt_count >= 0),
    CONSTRAINT campaign_recipients_opt_in_source_check CHECK (char_length(opt_in_source) BETWEEN 1 AND 64),
    CONSTRAINT campaign_recipients_opt_in_hash_check CHECK (opt_in_reference_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT campaign_recipients_claim_check CHECK ((status = 'processing') = (claim_token IS NOT NULL AND lease_until IS NOT NULL))
);
CREATE INDEX campaign_recipients_work_idx ON campaign_recipients (instance_id, next_attempt_at, campaign_id, id)
    WHERE status = 'pending';
CREATE INDEX campaign_recipients_expired_lease_idx ON campaign_recipients (lease_until, campaign_id, id)
    WHERE status = 'processing';
CREATE INDEX campaign_recipients_provider_message_idx ON campaign_recipients (instance_id, provider_message_id)
    WHERE provider_message_id IS NOT NULL;

CREATE TABLE campaign_audit_events (
    id UUID PRIMARY KEY,
    campaign_id UUID NOT NULL,
    instance_id UUID NOT NULL,
    recipient_id UUID NULL,
    event_type VARCHAR(64) NOT NULL,
    actor_type VARCHAR(32) NOT NULL,
    actor_reference_hash VARCHAR(64) NULL,
    from_status VARCHAR(32) NULL,
    to_status VARCHAR(32) NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT campaign_audit_campaign_fk FOREIGN KEY (campaign_id, instance_id) REFERENCES campaigns(id, instance_id) ON DELETE CASCADE,
    CONSTRAINT campaign_audit_recipient_fk FOREIGN KEY (recipient_id, campaign_id, instance_id) REFERENCES campaign_recipients(id, campaign_id, instance_id) ON DELETE CASCADE,
    CONSTRAINT campaign_audit_actor_type_check CHECK (actor_type IN ('admin', 'instance', 'system')),
    CONSTRAINT campaign_audit_actor_hash_check CHECK (actor_reference_hash IS NULL OR actor_reference_hash ~ '^[0-9a-f]{64}$')
);
CREATE INDEX campaign_audit_history_idx ON campaign_audit_events (instance_id, campaign_id, occurred_at ASC, id ASC);`,
	},
	{
		Version: 13,
		Name:    "index_contacts_projection_search",
		SQL: `CREATE INDEX projected_contacts_search_sort_idx
ON projected_contacts (
    instance_id,
    (LOWER(preferred_jid)),
    contact_id
)
WHERE tombstoned_at IS NULL;
CREATE INDEX projected_contacts_search_jid_idx
ON projected_contacts (instance_id, (LOWER(preferred_jid)) text_pattern_ops)
WHERE tombstoned_at IS NULL;
CREATE INDEX projected_contacts_search_first_name_idx
ON projected_contacts (instance_id, (LOWER(COALESCE(first_name, ''))) text_pattern_ops)
WHERE tombstoned_at IS NULL;
CREATE INDEX projected_contacts_search_full_name_idx
ON projected_contacts (instance_id, (LOWER(COALESCE(full_name, ''))) text_pattern_ops)
WHERE tombstoned_at IS NULL;
CREATE INDEX projected_contacts_search_push_name_idx
ON projected_contacts (instance_id, (LOWER(COALESCE(push_name, ''))) text_pattern_ops)
WHERE tombstoned_at IS NULL;
CREATE INDEX projected_contacts_search_business_name_idx
ON projected_contacts (instance_id, (LOWER(COALESCE(business_name, ''))) text_pattern_ops)
WHERE tombstoned_at IS NULL;
CREATE INDEX projected_contacts_search_username_idx
ON projected_contacts (instance_id, (LOWER(COALESCE(username, ''))) text_pattern_ops)
WHERE tombstoned_at IS NULL;
CREATE INDEX projected_contacts_search_redacted_phone_idx
ON projected_contacts (instance_id, (LOWER(COALESCE(redacted_phone, ''))) text_pattern_ops)
WHERE tombstoned_at IS NULL;`,
	},
	{
		Version: 14,
		Name:    "index_groups_projection_search",
		SQL: `CREATE INDEX projected_groups_search_page_idx
ON projected_groups (instance_id, group_id)
WHERE tombstoned_at IS NULL;
CREATE INDEX projected_groups_search_jid_idx
ON projected_groups (instance_id, (LOWER(group_id)) text_pattern_ops)
WHERE tombstoned_at IS NULL;
CREATE INDEX projected_groups_search_name_idx
ON projected_groups (instance_id, (LOWER(LEFT(COALESCE(name, ''), 255))) text_pattern_ops)
WHERE tombstoned_at IS NULL;`,
	},
	{
		Version: 15,
		Name:    "add_projection_event_failure_metadata",
		SQL: `ALTER TABLE projection_event_inbox
    ADD COLUMN last_attempt_at TIMESTAMPTZ NULL,
    ADD COLUMN failure_class VARCHAR(32) NULL,
    ADD COLUMN retry_policy_version SMALLINT NOT NULL DEFAULT 1,
    ADD COLUMN max_attempts INTEGER NOT NULL DEFAULT 8,
    ADD COLUMN dead_lettered_at TIMESTAMPTZ NULL;

ALTER TABLE projection_event_inbox DROP CONSTRAINT projection_event_inbox_status_check;
ALTER TABLE projection_event_inbox
    ADD CONSTRAINT projection_event_inbox_status_check
    CHECK (status IN ('pending', 'processing', 'processed', 'failed', 'dead_letter'));
ALTER TABLE projection_event_inbox
    ADD CONSTRAINT projection_event_inbox_failure_class_check
    CHECK (failure_class IS NULL OR failure_class IN ('retryable', 'permanent'));
ALTER TABLE projection_event_inbox
    ADD CONSTRAINT projection_event_inbox_retry_policy_version_check
    CHECK (retry_policy_version > 0);
ALTER TABLE projection_event_inbox
    ADD CONSTRAINT projection_event_inbox_max_attempts_check
    CHECK (max_attempts > 0);
ALTER TABLE projection_event_inbox
    ADD CONSTRAINT projection_event_inbox_dead_letter_state_check
    CHECK (
        (status = 'dead_letter' AND dead_lettered_at IS NOT NULL AND failure_class IS NOT NULL AND last_error_code IS NOT NULL)
        OR (status <> 'dead_letter' AND dead_lettered_at IS NULL)
    );

CREATE INDEX projection_event_inbox_dead_letter_idx
ON projection_event_inbox (resource, dead_lettered_at DESC, instance_id, event_key)
WHERE status = 'dead_letter';
CREATE INDEX projection_event_inbox_health_idx
ON projection_event_inbox (instance_id, resource, status, available_at)
WHERE status IN ('pending', 'failed', 'dead_letter');`,
	},
	{
		Version: 16,
		Name:    "index_projection_work_health",
		SQL: `CREATE INDEX projection_event_inbox_work_health_idx
ON projection_event_inbox (instance_id, resource, ingested_at)
INCLUDE (status)
WHERE status <> 'processed';`,
	},
}

func Run(db *gorm.DB) error {
	if db == nil {
		return errors.New("migration database is required")
	}
	if db.Dialector.Name() != "postgres" {
		return fmt.Errorf("versioned migrations require PostgreSQL, got %s", db.Dialector.Name())
	}
	if err := validateRegistry(registry); err != nil {
		return err
	}

	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", advisoryLockKey).Error; err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
		if err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
    version BIGINT PRIMARY KEY,
    name TEXT NOT NULL,
    checksum TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`).Error; err != nil {
			return fmt.Errorf("create schema_migrations: %w", err)
		}

		var applied []appliedMigration
		if err := tx.Order("version ASC").Find(&applied).Error; err != nil {
			return fmt.Errorf("read applied migrations: %w", err)
		}
		byVersion := make(map[int64]appliedMigration, len(applied))
		for _, item := range applied {
			byVersion[item.Version] = item
		}

		for _, migration := range registry {
			checksum := migrationChecksum(migration)
			if existing, ok := byVersion[migration.Version]; ok {
				if existing.Name != migration.Name || existing.Checksum != checksum {
					return fmt.Errorf("migration %d was modified after application", migration.Version)
				}
				continue
			}
			if err := tx.Exec(migration.SQL).Error; err != nil {
				return fmt.Errorf("apply migration %d %s: %w", migration.Version, migration.Name, err)
			}
			record := appliedMigration{Version: migration.Version, Name: migration.Name, Checksum: checksum, AppliedAt: time.Now().UTC()}
			if err := tx.Create(&record).Error; err != nil {
				return fmt.Errorf("record migration %d: %w", migration.Version, err)
			}
		}
		return nil
	})
}

func validateRegistry(migrations []Migration) error {
	if len(migrations) == 0 {
		return errors.New("migration registry is empty")
	}
	copyOfRegistry := append([]Migration(nil), migrations...)
	sort.Slice(copyOfRegistry, func(i, j int) bool { return copyOfRegistry[i].Version < copyOfRegistry[j].Version })
	for index, migration := range copyOfRegistry {
		if migration.Version <= 0 || migration.Name == "" || migration.SQL == "" {
			return fmt.Errorf("migration at index %d is incomplete", index)
		}
		if index > 0 && copyOfRegistry[index-1].Version == migration.Version {
			return fmt.Errorf("duplicate migration version %d", migration.Version)
		}
		if migration.Version != migrations[index].Version {
			return errors.New("migration registry must be ordered by version")
		}
	}
	return nil
}

func migrationChecksum(migration Migration) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", migration.Version, migration.Name, migration.SQL)))
	return hex.EncodeToString(sum[:])
}
