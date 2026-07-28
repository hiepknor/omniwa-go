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
	{
		Version: 17,
		Name:    "create_projection_failure_operations",
		SQL: `ALTER TABLE projection_event_inbox
    ADD COLUMN discarded_at TIMESTAMPTZ NULL;

ALTER TABLE projection_event_inbox
    ADD CONSTRAINT projection_event_inbox_discarded_state_check
    CHECK (discarded_at IS NULL OR (status = 'processed' AND processed_at IS NULL));

CREATE TABLE projection_failure_audit (
    id UUID PRIMARY KEY,
    instance_id UUID NOT NULL,
    resource VARCHAR(64) NOT NULL,
    event_key VARCHAR(255) NOT NULL,
    action VARCHAR(32) NOT NULL,
    reason VARCHAR(500) NOT NULL,
    actor_reference_hash VARCHAR(64) NOT NULL,
    request_id VARCHAR(64) NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT projection_failure_audit_event_fk FOREIGN KEY (instance_id, resource, event_key)
        REFERENCES projection_event_inbox(instance_id, resource, event_key) ON DELETE CASCADE,
    CONSTRAINT projection_failure_audit_action_check CHECK (action IN ('replay', 'discard')),
    CONSTRAINT projection_failure_audit_reason_check CHECK (char_length(reason) BETWEEN 1 AND 500),
    CONSTRAINT projection_failure_audit_actor_hash_check CHECK (actor_reference_hash ~ '^[0-9a-f]{64}$')
);
CREATE INDEX projection_failure_audit_history_idx
ON projection_failure_audit (occurred_at DESC, id DESC);

CREATE INDEX projection_event_inbox_dead_letter_admin_idx
ON projection_event_inbox (dead_lettered_at DESC, instance_id DESC, resource DESC, event_key DESC)
WHERE status = 'dead_letter';
CREATE INDEX projection_event_inbox_instance_dead_letter_admin_idx
ON projection_event_inbox (instance_id, dead_lettered_at DESC, resource DESC, event_key DESC)
WHERE status = 'dead_letter';`,
	},
	{
		Version: 18,
		Name:    "add_instance_token_lookup_digests",
		SQL: `ALTER TABLE instances
    ADD COLUMN token_digest VARCHAR(64) NULL,
    ADD COLUMN token_key_version INTEGER NULL;

ALTER TABLE instances
    ADD CONSTRAINT instances_token_digest_pair_check
    CHECK ((token_digest IS NULL) = (token_key_version IS NULL)),
    ADD CONSTRAINT instances_token_digest_format_check
    CHECK (token_digest IS NULL OR token_digest ~ '^[0-9a-f]{64}$'),
    ADD CONSTRAINT instances_token_key_version_check
    CHECK (token_key_version IS NULL OR token_key_version > 0);

CREATE UNIQUE INDEX instances_token_digest_unique_idx
ON instances (token_key_version, token_digest)
WHERE token_digest IS NOT NULL;

CREATE INDEX instances_token_digest_backfill_idx
ON instances (id)
WHERE token_digest IS NULL;`,
	},
	{
		Version: 19,
		Name:    "create_instance_token_rotation_audit",
		SQL: `ALTER TABLE instances
    ADD COLUMN token_generation BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN token_rotated_at TIMESTAMPTZ NULL;

ALTER TABLE instances
    ADD CONSTRAINT instances_token_generation_check CHECK (token_generation > 0);

CREATE TABLE instance_token_rotation_audit (
    id UUID PRIMARY KEY,
    instance_id UUID NOT NULL,
    previous_generation BIGINT NOT NULL,
    new_generation BIGINT NOT NULL,
    reason VARCHAR(500) NOT NULL,
    actor_reference_hash VARCHAR(64) NOT NULL,
    request_id VARCHAR(64) NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT instance_token_rotation_audit_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE,
    CONSTRAINT instance_token_rotation_audit_generation_check CHECK (previous_generation > 0 AND new_generation = previous_generation + 1),
    CONSTRAINT instance_token_rotation_audit_reason_check CHECK (char_length(reason) BETWEEN 1 AND 500),
    CONSTRAINT instance_token_rotation_audit_actor_hash_check CHECK (actor_reference_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT instance_token_rotation_audit_request_unique UNIQUE (instance_id, request_id)
);
CREATE INDEX instance_token_rotation_audit_history_idx
ON instance_token_rotation_audit (instance_id, occurred_at DESC, id DESC);`,
	},
	{
		Version: 20,
		Name:    "measure_instance_token_plaintext_fallback",
		SQL: `CREATE TABLE instance_token_fallback_usage (
    instance_id UUID NOT NULL,
    key_version INTEGER NOT NULL,
    lookup_count BIGINT NOT NULL DEFAULT 1,
    first_used_at TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (instance_id, key_version),
    CONSTRAINT instance_token_fallback_usage_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE,
    CONSTRAINT instance_token_fallback_usage_key_version_check CHECK (key_version > 0),
    CONSTRAINT instance_token_fallback_usage_lookup_count_check CHECK (lookup_count > 0),
    CONSTRAINT instance_token_fallback_usage_time_check CHECK (last_used_at >= first_used_at)
);
CREATE INDEX instance_token_fallback_usage_last_used_idx
ON instance_token_fallback_usage (last_used_at DESC, instance_id);`,
	},
	{
		Version: 21,
		Name:    "create_group_lists",
		SQL: `ALTER TABLE projected_groups
    ADD COLUMN tombstone_cause VARCHAR(32) NULL;

UPDATE projected_groups
SET tombstone_cause = 'group_access_lost'
WHERE tombstoned_at IS NOT NULL;

ALTER TABLE projected_groups
    ADD CONSTRAINT projected_groups_tombstone_cause_check
    CHECK (
        (tombstoned_at IS NULL AND tombstone_cause IS NULL)
        OR (tombstoned_at IS NOT NULL AND tombstone_cause IN ('group_access_lost', 'group_dissolved'))
    );

CREATE TABLE group_lists (
    id UUID PRIMARY KEY,
    instance_id UUID NOT NULL,
    name VARCHAR(255) NOT NULL,
    normalized_name VARCHAR(255) NOT NULL,
    description TEXT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    authorization_source VARCHAR(64) NOT NULL,
    authorization_reference_hash VARCHAR(64) NOT NULL,
    authorized_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    deleted_at TIMESTAMPTZ NULL,
    CONSTRAINT group_lists_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE,
    CONSTRAINT group_lists_instance_identity_unique UNIQUE (id, instance_id),
    CONSTRAINT group_lists_name_check CHECK (char_length(name) BETWEEN 1 AND 255),
    CONSTRAINT group_lists_normalized_name_check CHECK (char_length(normalized_name) BETWEEN 1 AND 255),
    CONSTRAINT group_lists_description_check CHECK (description IS NULL OR char_length(description) <= 2000),
    CONSTRAINT group_lists_version_check CHECK (version >= 1),
    CONSTRAINT group_lists_authorization_source_check CHECK (char_length(authorization_source) BETWEEN 1 AND 64),
    CONSTRAINT group_lists_authorization_hash_check CHECK (authorization_reference_hash ~ '^[0-9a-f]{64}$')
);
CREATE UNIQUE INDEX group_lists_active_name_unique_idx
ON group_lists (instance_id, normalized_name)
WHERE deleted_at IS NULL;
CREATE INDEX group_lists_page_idx
ON group_lists (instance_id, normalized_name, id)
WHERE deleted_at IS NULL;

CREATE TABLE group_list_entries (
    group_list_id UUID NOT NULL,
    instance_id UUID NOT NULL,
    group_jid VARCHAR(255) NOT NULL,
    group_name_snapshot VARCHAR(255) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (group_list_id, group_jid),
    CONSTRAINT group_list_entries_list_fk FOREIGN KEY (group_list_id, instance_id) REFERENCES group_lists(id, instance_id) ON DELETE CASCADE,
    CONSTRAINT group_list_entries_jid_check CHECK (group_jid ~ '^[^@]+@g[.]us$'),
    CONSTRAINT group_list_entries_name_check CHECK (char_length(group_name_snapshot) <= 255)
);
CREATE INDEX group_list_entries_page_idx
ON group_list_entries (instance_id, group_list_id, group_jid);

CREATE TABLE group_list_audit_events (
    id UUID PRIMARY KEY,
    group_list_id UUID NOT NULL,
    instance_id UUID NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    actor_type VARCHAR(32) NOT NULL,
    actor_reference_hash VARCHAR(64) NOT NULL,
    from_version BIGINT NULL,
    to_version BIGINT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT group_list_audit_list_fk FOREIGN KEY (group_list_id, instance_id) REFERENCES group_lists(id, instance_id) ON DELETE CASCADE,
    CONSTRAINT group_list_audit_event_type_check CHECK (event_type IN ('created', 'updated', 'deleted')),
    CONSTRAINT group_list_audit_actor_type_check CHECK (actor_type IN ('instance', 'system')),
    CONSTRAINT group_list_audit_actor_hash_check CHECK (actor_reference_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT group_list_audit_version_check CHECK (to_version >= 1 AND (from_version IS NULL OR to_version = from_version + 1))
);
CREATE INDEX group_list_audit_history_idx
ON group_list_audit_events (instance_id, group_list_id, occurred_at ASC, id ASC);`,
	},
	{
		Version: 22,
		Name:    "add_group_campaign_contract",
		SQL: `ALTER TABLE campaigns
    ADD COLUMN target_type VARCHAR(32) NOT NULL DEFAULT 'direct',
    ADD COLUMN group_list_id UUID NULL,
    ADD COLUMN group_list_name_snapshot VARCHAR(255) NULL,
    ADD COLUMN group_list_version BIGINT NULL,
    ADD COLUMN status_reason VARCHAR(64) NULL,
    ADD COLUMN pause_reason VARCHAR(64) NULL,
    ADD COLUMN retry_at TIMESTAMPTZ NULL,
    ADD COLUMN needs_attention BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE campaigns SET target_type = 'direct' WHERE target_type IS NULL;

ALTER TABLE campaigns
    ADD CONSTRAINT campaigns_target_snapshot_check CHECK (
        (target_type = 'direct' AND group_list_id IS NULL AND group_list_name_snapshot IS NULL AND group_list_version IS NULL)
        OR (target_type = 'group_list' AND group_list_id IS NOT NULL AND group_list_name_snapshot IS NOT NULL AND group_list_version >= 1)
    ),
    ADD CONSTRAINT campaigns_status_reason_check CHECK (status_reason IS NULL OR status_reason ~ '^[a-z0-9_]{1,64}$'),
    ADD CONSTRAINT campaigns_pause_reason_check CHECK (pause_reason IS NULL OR pause_reason ~ '^[a-z0-9_]{1,64}$');

CREATE INDEX campaigns_group_list_snapshot_idx
ON campaigns (instance_id, group_list_id, created_at DESC, id DESC)
WHERE target_type = 'group_list';

ALTER TABLE campaign_recipients
    ADD COLUMN target_type VARCHAR(32) NOT NULL DEFAULT 'direct',
    ADD COLUMN target_label VARCHAR(255) NULL;

UPDATE campaign_recipients SET target_type = 'direct' WHERE target_type IS NULL;

ALTER TABLE campaign_recipients
    ADD CONSTRAINT campaign_recipients_target_check CHECK (
        (target_type = 'direct' AND target_label IS NULL)
        OR (target_type = 'group' AND target_label IS NOT NULL AND char_length(target_label) BETWEEN 1 AND 255)
    );

CREATE INDEX campaign_recipients_group_target_idx
ON campaign_recipients (instance_id, recipient_jid, campaign_id)
WHERE target_type = 'group';`,
	},
	{
		Version: 23,
		Name:    "add_group_campaign_delivery_safety",
		SQL: `ALTER TABLE campaigns
    ADD COLUMN failure_signal_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN rate_limit_signal_count INTEGER NOT NULL DEFAULT 0,
    ADD CONSTRAINT campaigns_failure_signal_count_check CHECK (failure_signal_count >= 0),
    ADD CONSTRAINT campaigns_rate_limit_signal_count_check CHECK (rate_limit_signal_count >= 0);

CREATE TABLE campaign_group_delivery_guards (
    instance_id UUID NOT NULL,
    group_jid VARCHAR(255) NOT NULL,
    owner_recipient_id UUID NULL,
    owner_campaign_id UUID NULL,
    claim_token VARCHAR(64) NULL,
    lease_until TIMESTAMPTZ NULL,
    last_acknowledged_at TIMESTAMPTZ NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (instance_id, group_jid),
    CONSTRAINT campaign_group_delivery_guards_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE,
    CONSTRAINT campaign_group_delivery_guards_recipient_fk FOREIGN KEY (owner_recipient_id, owner_campaign_id, instance_id) REFERENCES campaign_recipients(id, campaign_id, instance_id) ON DELETE SET NULL (owner_recipient_id, owner_campaign_id),
    CONSTRAINT campaign_group_delivery_guards_jid_check CHECK (group_jid ~ '^[^@]+@g[.]us$'),
    CONSTRAINT campaign_group_delivery_guards_claim_check CHECK (
        (owner_recipient_id IS NULL AND owner_campaign_id IS NULL AND claim_token IS NULL AND lease_until IS NULL)
        OR (owner_recipient_id IS NOT NULL AND owner_campaign_id IS NOT NULL AND claim_token IS NOT NULL AND lease_until IS NOT NULL)
    ),
    CONSTRAINT campaign_group_delivery_guards_claim_token_check CHECK (claim_token IS NULL OR claim_token ~ '^[0-9a-f-]{36}$')
);
CREATE INDEX campaign_group_delivery_guards_lease_idx
ON campaign_group_delivery_guards (lease_until, instance_id, group_jid)
WHERE lease_until IS NOT NULL;
CREATE INDEX campaign_group_delivery_guards_cooldown_idx
ON campaign_group_delivery_guards (instance_id, last_acknowledged_at, group_jid)
WHERE last_acknowledged_at IS NOT NULL;

INSERT INTO campaign_group_delivery_guards (instance_id, group_jid, updated_at)
SELECT DISTINCT instance_id, recipient_jid, NOW()
FROM campaign_recipients
WHERE target_type = 'group'
ON CONFLICT (instance_id, group_jid) DO NOTHING;

CREATE TABLE campaign_instance_circuits (
    instance_id UUID PRIMARY KEY,
    open_until TIMESTAMPTZ NOT NULL,
    reason VARCHAR(64) NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT campaign_instance_circuits_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE,
    CONSTRAINT campaign_instance_circuits_reason_check CHECK (reason ~ '^[a-z0-9_]{1,64}$')
);
CREATE INDEX campaign_instance_circuits_open_idx
ON campaign_instance_circuits (open_until, instance_id);`,
	},
	{
		Version: 24,
		Name:    "create_campaign_media_assets",
		SQL: `CREATE TABLE campaign_media_assets (
    id UUID PRIMARY KEY,
    instance_id UUID NOT NULL,
    object_key TEXT NOT NULL,
    media_type VARCHAR(32) NOT NULL,
    mime_type VARCHAR(128) NULL,
    size_bytes BIGINT NULL,
    width INTEGER NULL,
    height INTEGER NULL,
    sha256 VARCHAR(64) NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'uploading',
    request_reference_hash VARCHAR(64) NULL,
    cleanup_claim_token UUID NULL,
    cleanup_lease_until TIMESTAMPTZ NULL,
    ready_at TIMESTAMPTZ NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    deleted_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT campaign_media_assets_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE,
    CONSTRAINT campaign_media_assets_instance_identity_unique UNIQUE (id, instance_id),
    CONSTRAINT campaign_media_assets_object_key_unique UNIQUE (object_key),
    CONSTRAINT campaign_media_assets_type_check CHECK (media_type = 'image'),
    CONSTRAINT campaign_media_assets_status_check CHECK (status IN ('uploading', 'ready', 'failed', 'deleted')),
    CONSTRAINT campaign_media_assets_mime_check CHECK (mime_type IS NULL OR mime_type IN ('image/jpeg', 'image/png')),
    CONSTRAINT campaign_media_assets_size_check CHECK (size_bytes IS NULL OR size_bytes BETWEEN 1 AND 67108864),
    CONSTRAINT campaign_media_assets_dimensions_check CHECK (
        (width IS NULL AND height IS NULL)
        OR (width BETWEEN 1 AND 32768 AND height BETWEEN 1 AND 32768)
    ),
    CONSTRAINT campaign_media_assets_sha_check CHECK (sha256 IS NULL OR sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT campaign_media_assets_request_hash_check CHECK (request_reference_hash IS NULL OR request_reference_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT campaign_media_assets_cleanup_claim_check CHECK (
        (cleanup_claim_token IS NULL AND cleanup_lease_until IS NULL)
        OR (cleanup_claim_token IS NOT NULL AND cleanup_lease_until IS NOT NULL)
    ),
    CONSTRAINT campaign_media_assets_ready_check CHECK (
        status <> 'ready'
        OR (mime_type IS NOT NULL AND size_bytes IS NOT NULL AND width IS NOT NULL AND height IS NOT NULL AND sha256 IS NOT NULL AND ready_at IS NOT NULL AND deleted_at IS NULL)
    ),
    CONSTRAINT campaign_media_assets_deleted_check CHECK ((status = 'deleted') = (deleted_at IS NOT NULL)),
    CONSTRAINT campaign_media_assets_expiry_check CHECK (expires_at > created_at)
);
CREATE UNIQUE INDEX campaign_media_assets_request_unique_idx
ON campaign_media_assets (instance_id, request_reference_hash)
WHERE request_reference_hash IS NOT NULL;
CREATE INDEX campaign_media_assets_cleanup_idx
ON campaign_media_assets (expires_at, id)
WHERE status IN ('uploading', 'ready', 'failed') AND deleted_at IS NULL;
CREATE INDEX campaign_media_assets_instance_page_idx
ON campaign_media_assets (instance_id, created_at DESC, id DESC)
WHERE deleted_at IS NULL;`,
	},
	{
		Version: 25,
		Name:    "add_campaign_image_content_contract",
		SQL: `ALTER TABLE campaigns
    DROP CONSTRAINT campaigns_content_type_check,
    DROP CONSTRAINT campaigns_text_body_check,
    ADD COLUMN media_asset_id UUID NULL,
    ADD COLUMN media_mime_type VARCHAR(128) NULL,
    ADD COLUMN media_size_bytes BIGINT NULL,
    ADD COLUMN media_width INTEGER NULL,
    ADD COLUMN media_height INTEGER NULL,
    ADD COLUMN media_sha256 VARCHAR(64) NULL,
    ADD CONSTRAINT campaigns_content_type_check CHECK (content_type IN ('text', 'image')),
    ADD CONSTRAINT campaigns_text_body_check CHECK (
        (content_type = 'text' AND CHAR_LENGTH(text_body) BETWEEN 1 AND 4096)
        OR (content_type = 'image' AND CHAR_LENGTH(text_body) <= 1024)
    ),
    ADD CONSTRAINT campaigns_media_asset_fk FOREIGN KEY (media_asset_id, instance_id)
        REFERENCES campaign_media_assets(id, instance_id) ON DELETE RESTRICT,
    ADD CONSTRAINT campaigns_content_shape_check CHECK (
        (content_type = 'text'
            AND BTRIM(text_body) <> ''
            AND media_asset_id IS NULL
            AND media_mime_type IS NULL
            AND media_size_bytes IS NULL
            AND media_width IS NULL
            AND media_height IS NULL
            AND media_sha256 IS NULL)
        OR
        (content_type = 'image'
            AND CHAR_LENGTH(text_body) <= 1024
            AND media_asset_id IS NOT NULL
            AND media_mime_type IN ('image/jpeg', 'image/png')
            AND media_size_bytes BETWEEN 1 AND 67108864
            AND media_width BETWEEN 1 AND 32768
            AND media_height BETWEEN 1 AND 32768
            AND media_sha256 ~ '^[0-9a-f]{64}$')
    );
CREATE INDEX campaigns_media_asset_idx
ON campaigns (instance_id, media_asset_id)
WHERE media_asset_id IS NOT NULL;`,
	},
	{
		Version: 26,
		Name:    "create_shared_media_asset_foundation",
		SQL: `CREATE TABLE media_assets (
    id UUID PRIMARY KEY,
    instance_id UUID NOT NULL,
    media_type VARCHAR(32) NOT NULL,
    origin VARCHAR(32) NOT NULL,
    status VARCHAR(32) NOT NULL,
    failure_code VARCHAR(64) NULL,
    request_reference_hash VARCHAR(64) NULL,
    cleanup_claim_token UUID NULL,
    cleanup_lease_until TIMESTAMPTZ NULL,
    ready_at TIMESTAMPTZ NULL,
    expires_at TIMESTAMPTZ NULL,
    deleted_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT media_assets_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE RESTRICT,
    CONSTRAINT media_assets_instance_identity_unique UNIQUE (id, instance_id),
    CONSTRAINT media_assets_type_check CHECK (media_type = 'image'),
    CONSTRAINT media_assets_origin_check CHECK (origin IN ('device_upload', 'whatsapp_inbound')),
    CONSTRAINT media_assets_status_check CHECK (status IN ('pending', 'uploading', 'downloading', 'processing', 'ready', 'failed', 'deleting', 'deleted')),
    CONSTRAINT media_assets_failure_code_check CHECK (failure_code IS NULL OR failure_code ~ '^[a-z0-9_]{1,64}$'),
    CONSTRAINT media_assets_request_hash_check CHECK (request_reference_hash IS NULL OR request_reference_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT media_assets_cleanup_claim_check CHECK (
        (cleanup_claim_token IS NULL AND cleanup_lease_until IS NULL)
        OR (cleanup_claim_token IS NOT NULL AND cleanup_lease_until IS NOT NULL)
    ),
    CONSTRAINT media_assets_ready_check CHECK (status <> 'ready' OR (ready_at IS NOT NULL AND deleted_at IS NULL)),
    CONSTRAINT media_assets_deleted_check CHECK ((status = 'deleted') = (deleted_at IS NOT NULL)),
    CONSTRAINT media_assets_expiry_check CHECK (expires_at IS NULL OR expires_at > created_at)
);
CREATE UNIQUE INDEX media_assets_request_unique_idx
ON media_assets (instance_id, request_reference_hash)
WHERE request_reference_hash IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX media_assets_cleanup_idx
ON media_assets (expires_at, id)
WHERE expires_at IS NOT NULL AND status IN ('pending', 'uploading', 'downloading', 'processing', 'ready', 'failed', 'deleting') AND deleted_at IS NULL;
CREATE INDEX media_assets_instance_page_idx
ON media_assets (instance_id, created_at DESC, id DESC)
WHERE deleted_at IS NULL;

CREATE TABLE media_asset_variants (
    media_asset_id UUID NOT NULL,
    instance_id UUID NOT NULL,
    variant VARCHAR(32) NOT NULL,
    object_key TEXT NOT NULL,
    mime_type VARCHAR(128) NOT NULL,
    size_bytes BIGINT NOT NULL,
    width INTEGER NOT NULL,
    height INTEGER NOT NULL,
    sha256 VARCHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (media_asset_id, variant),
    CONSTRAINT media_asset_variants_asset_fk FOREIGN KEY (media_asset_id, instance_id)
        REFERENCES media_assets(id, instance_id) ON DELETE CASCADE,
    CONSTRAINT media_asset_variants_object_key_unique UNIQUE (object_key),
    CONSTRAINT media_asset_variants_kind_check CHECK (variant IN ('provider_original', 'canonical')),
    CONSTRAINT media_asset_variants_mime_check CHECK (mime_type IN ('image/jpeg', 'image/png')),
    CONSTRAINT media_asset_variants_size_check CHECK (size_bytes BETWEEN 1 AND 67108864),
    CONSTRAINT media_asset_variants_dimensions_check CHECK (width BETWEEN 1 AND 32768 AND height BETWEEN 1 AND 32768),
    CONSTRAINT media_asset_variants_sha_check CHECK (sha256 ~ '^[0-9a-f]{64}$')
);
CREATE INDEX media_asset_variants_instance_idx
ON media_asset_variants (instance_id, media_asset_id);

CREATE TABLE media_asset_references (
    instance_id UUID NOT NULL,
    media_asset_id UUID NOT NULL,
    owner_type VARCHAR(32) NOT NULL,
    owner_id VARCHAR(255) NOT NULL,
    retention_until TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (instance_id, media_asset_id, owner_type, owner_id),
    CONSTRAINT media_asset_references_asset_fk FOREIGN KEY (media_asset_id, instance_id)
        REFERENCES media_assets(id, instance_id) ON DELETE RESTRICT,
    CONSTRAINT media_asset_references_owner_check CHECK (owner_type IN ('campaign', 'message')),
    CONSTRAINT media_asset_references_owner_id_check CHECK (BTRIM(owner_id) <> '')
);
CREATE INDEX media_asset_references_owner_idx
ON media_asset_references (instance_id, owner_type, owner_id);
CREATE INDEX media_asset_references_retention_idx
ON media_asset_references (retention_until, media_asset_id)
WHERE retention_until IS NOT NULL;

CREATE TABLE media_download_jobs (
    id UUID PRIMARY KEY,
    instance_id UUID NOT NULL,
    media_asset_id UUID NOT NULL,
    message_id VARCHAR(255) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    descriptor_ciphertext BYTEA NOT NULL,
    descriptor_nonce BYTEA NOT NULL,
    descriptor_key_version INTEGER NOT NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    claim_token UUID NULL,
    lease_until TIMESTAMPTZ NULL,
    provider_expires_at TIMESTAMPTZ NULL,
    last_error_code VARCHAR(64) NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ NULL,
    CONSTRAINT media_download_jobs_asset_fk FOREIGN KEY (media_asset_id, instance_id)
        REFERENCES media_assets(id, instance_id) ON DELETE CASCADE,
    CONSTRAINT media_download_jobs_message_unique UNIQUE (instance_id, message_id),
    CONSTRAINT media_download_jobs_asset_unique UNIQUE (media_asset_id, instance_id),
    CONSTRAINT media_download_jobs_status_check CHECK (status IN ('pending', 'processing', 'retry_wait', 'completed', 'failed')),
    CONSTRAINT media_download_jobs_attempt_check CHECK (attempt_count BETWEEN 0 AND max_attempts AND max_attempts BETWEEN 1 AND 20),
    CONSTRAINT media_download_jobs_descriptor_check CHECK (OCTET_LENGTH(descriptor_ciphertext) > 0 AND OCTET_LENGTH(descriptor_nonce) = 12 AND descriptor_key_version > 0),
    CONSTRAINT media_download_jobs_claim_check CHECK ((claim_token IS NULL AND lease_until IS NULL) OR (claim_token IS NOT NULL AND lease_until IS NOT NULL)),
    CONSTRAINT media_download_jobs_error_check CHECK (last_error_code IS NULL OR last_error_code ~ '^[a-z0-9_]{1,64}$'),
    CONSTRAINT media_download_jobs_completed_check CHECK ((status = 'completed') = (completed_at IS NOT NULL))
);
CREATE INDEX media_download_jobs_claim_idx
ON media_download_jobs (next_attempt_at, id)
WHERE status IN ('pending', 'retry_wait');

CREATE TABLE media_asset_audit_events (
    id UUID PRIMARY KEY,
    instance_id UUID NOT NULL,
    media_asset_id UUID NOT NULL,
    event_type VARCHAR(64) NOT NULL,
    actor_type VARCHAR(32) NOT NULL,
    actor_reference_hash VARCHAR(64) NULL,
    request_id VARCHAR(64) NULL,
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT media_asset_audit_events_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE,
    CONSTRAINT media_asset_audit_events_asset_fk FOREIGN KEY (media_asset_id, instance_id)
        REFERENCES media_assets(id, instance_id) ON DELETE CASCADE,
    CONSTRAINT media_asset_audit_events_type_check CHECK (event_type ~ '^[a-z0-9_]{1,64}$'),
    CONSTRAINT media_asset_audit_events_actor_check CHECK (actor_type IN ('operator', 'system', 'provider')),
    CONSTRAINT media_asset_audit_events_actor_hash_check CHECK (actor_reference_hash IS NULL OR actor_reference_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT media_asset_audit_events_request_check CHECK (request_id IS NULL OR CHAR_LENGTH(request_id) BETWEEN 1 AND 64),
    CONSTRAINT media_asset_audit_events_details_object_check CHECK (jsonb_typeof(details) = 'object')
);
CREATE INDEX media_asset_audit_events_page_idx
ON media_asset_audit_events (instance_id, media_asset_id, occurred_at DESC, id DESC);`,
	},
	{
		Version: 27,
		Name:    "backfill_campaign_media_assets",
		SQL: `INSERT INTO media_assets (
    id, instance_id, media_type, origin, status, failure_code,
    request_reference_hash, cleanup_claim_token, cleanup_lease_until,
    ready_at, expires_at, deleted_at, created_at, updated_at
)
SELECT
    id, instance_id, media_type, 'device_upload', status,
    CASE WHEN status = 'failed' THEN 'legacy_campaign_media_failed' ELSE NULL END,
    request_reference_hash, cleanup_claim_token, cleanup_lease_until,
    ready_at, expires_at, deleted_at, created_at, updated_at
FROM campaign_media_assets
ON CONFLICT (id) DO NOTHING;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM campaign_media_assets AS legacy
        LEFT JOIN media_assets AS shared ON shared.id = legacy.id
        WHERE shared.id IS NULL
           OR shared.instance_id <> legacy.instance_id
           OR shared.media_type <> legacy.media_type
           OR shared.origin <> 'device_upload'
           OR shared.status <> legacy.status
           OR shared.failure_code IS DISTINCT FROM CASE WHEN legacy.status = 'failed' THEN 'legacy_campaign_media_failed' ELSE NULL END
           OR shared.request_reference_hash IS DISTINCT FROM legacy.request_reference_hash
           OR shared.cleanup_claim_token IS DISTINCT FROM legacy.cleanup_claim_token
           OR shared.cleanup_lease_until IS DISTINCT FROM legacy.cleanup_lease_until
           OR shared.ready_at IS DISTINCT FROM legacy.ready_at
           OR shared.expires_at IS DISTINCT FROM legacy.expires_at
           OR shared.deleted_at IS DISTINCT FROM legacy.deleted_at
           OR shared.created_at IS DISTINCT FROM legacy.created_at
           OR shared.updated_at IS DISTINCT FROM legacy.updated_at
    ) THEN
        RAISE EXCEPTION 'campaign media asset backfill identity mismatch';
    END IF;
END $$;

INSERT INTO media_asset_variants (
    media_asset_id, instance_id, variant, object_key, mime_type,
    size_bytes, width, height, sha256, created_at
)
SELECT
    id, instance_id, 'canonical', object_key, mime_type,
    size_bytes, width, height, sha256, COALESCE(ready_at, created_at)
FROM campaign_media_assets
WHERE status = 'ready' AND deleted_at IS NULL
ON CONFLICT (media_asset_id, variant) DO NOTHING;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM campaign_media_assets AS legacy
        LEFT JOIN media_asset_variants AS variant
          ON variant.media_asset_id = legacy.id
         AND variant.instance_id = legacy.instance_id
         AND variant.variant = 'canonical'
        WHERE legacy.status = 'ready'
          AND legacy.deleted_at IS NULL
          AND (
              variant.media_asset_id IS NULL
              OR variant.object_key <> legacy.object_key
              OR variant.mime_type <> legacy.mime_type
              OR variant.size_bytes <> legacy.size_bytes
              OR variant.width <> legacy.width
              OR variant.height <> legacy.height
              OR variant.sha256 <> legacy.sha256
              OR variant.created_at IS DISTINCT FROM COALESCE(legacy.ready_at, legacy.created_at)
          )
    ) THEN
        RAISE EXCEPTION 'campaign media canonical variant backfill mismatch';
    END IF;
END $$;

INSERT INTO media_asset_references (
    instance_id, media_asset_id, owner_type, owner_id, retention_until, created_at
)
SELECT
    instance_id, media_asset_id, 'campaign', id::text, NULL, created_at
FROM campaigns
WHERE media_asset_id IS NOT NULL
ON CONFLICT (instance_id, media_asset_id, owner_type, owner_id) DO NOTHING;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM campaigns AS campaign
        LEFT JOIN media_asset_references AS reference
          ON reference.instance_id = campaign.instance_id
         AND reference.media_asset_id = campaign.media_asset_id
         AND reference.owner_type = 'campaign'
         AND reference.owner_id = campaign.id::text
        WHERE campaign.media_asset_id IS NOT NULL
          AND (
              reference.media_asset_id IS NULL
              OR reference.retention_until IS NOT NULL
              OR reference.created_at IS DISTINCT FROM campaign.created_at
          )
    ) THEN
        RAISE EXCEPTION 'campaign media reference backfill mismatch';
    END IF;
END $$;`,
	},
	{
		Version: 28,
		Name:    "link_inbound_media_assets",
		SQL: `ALTER TABLE projected_messages
    ADD COLUMN media_asset_id UUID NULL,
    ADD CONSTRAINT projected_messages_media_asset_fk
        FOREIGN KEY (media_asset_id, instance_id)
        REFERENCES media_assets(id, instance_id)
        ON DELETE SET NULL (media_asset_id),
    ADD CONSTRAINT projected_messages_media_asset_shape_check
        CHECK (media_asset_id IS NULL OR media_type = 'image');

CREATE INDEX projected_messages_media_asset_idx
ON projected_messages (instance_id, media_asset_id)
WHERE media_asset_id IS NOT NULL AND deleted_at IS NULL;

ALTER TABLE media_download_jobs
    DROP CONSTRAINT media_download_jobs_descriptor_check,
    ALTER COLUMN descriptor_ciphertext DROP NOT NULL,
    ALTER COLUMN descriptor_nonce DROP NOT NULL,
    ALTER COLUMN descriptor_key_version DROP NOT NULL;

UPDATE media_download_jobs
SET descriptor_ciphertext = NULL,
    descriptor_nonce = NULL,
    descriptor_key_version = NULL,
    updated_at = NOW()
WHERE status IN ('completed', 'failed');

ALTER TABLE media_download_jobs
    ADD CONSTRAINT media_download_jobs_descriptor_lifecycle_check CHECK (
        (
            status IN ('pending', 'processing', 'retry_wait')
            AND descriptor_ciphertext IS NOT NULL
            AND OCTET_LENGTH(descriptor_ciphertext) > 0
            AND descriptor_nonce IS NOT NULL
            AND OCTET_LENGTH(descriptor_nonce) = 12
            AND descriptor_key_version IS NOT NULL
            AND descriptor_key_version > 0
        )
        OR (
            status IN ('completed', 'failed')
            AND descriptor_ciphertext IS NULL
            AND descriptor_nonce IS NULL
            AND descriptor_key_version IS NULL
        )
    );

UPDATE projection_states
SET schema_version = 2, updated_at = NOW()
WHERE resource = 'messages' AND schema_version < 2;`,
	},
	{
		Version: 29,
		Name:    "index_group_participant_identities",
		SQL: `CREATE INDEX projected_group_participants_participant_identity_idx
ON projected_group_participants (instance_id, participant_id, group_id)
WHERE tombstoned_at IS NULL;

CREATE INDEX projected_group_participants_phone_identity_idx
ON projected_group_participants (instance_id, phone_number_jid, group_id)
WHERE tombstoned_at IS NULL AND phone_number_jid IS NOT NULL;

CREATE INDEX projected_group_participants_lid_identity_idx
ON projected_group_participants (instance_id, lid, group_id)
WHERE tombstoned_at IS NULL AND lid IS NOT NULL;`,
	},
	{
		Version: 30,
		Name:    "create_group_management_foundation",
		SQL: `ALTER TABLE projected_group_participants
    ADD COLUMN public_id UUID NOT NULL DEFAULT gen_random_uuid();

CREATE UNIQUE INDEX projected_group_participants_public_id_idx
ON projected_group_participants (instance_id, group_id, public_id);

ALTER TABLE projected_groups
    ADD COLUMN actor_membership_state VARCHAR(32) NULL,
    ADD COLUMN actor_membership_changed_at TIMESTAMPTZ NULL,
    ADD COLUMN picture_id VARCHAR(255) NULL,
    ADD COLUMN picture_removed BOOLEAN NULL,
    ADD COLUMN picture_updated_at TIMESTAMPTZ NULL,
    ADD CONSTRAINT projected_groups_actor_membership_state_check
        CHECK (actor_membership_state IS NULL OR actor_membership_state IN ('unknown', 'joined', 'left', 'removed'));

CREATE TABLE group_management_commands (
    id UUID PRIMARY KEY,
    instance_id UUID NOT NULL,
    group_jid VARCHAR(255) NOT NULL,
    command_type VARCHAR(64) NOT NULL,
    status VARCHAR(32) NOT NULL,
    idempotency_key_hash VARCHAR(64) NULL,
    request_fingerprint VARCHAR(64) NOT NULL,
    request_id VARCHAR(255) NULL,
    actor_type VARCHAR(32) NOT NULL,
    actor_reference_hash VARCHAR(64) NOT NULL,
    safe_outcome JSONB NOT NULL DEFAULT '{}'::jsonb,
    execution_started_at TIMESTAMPTZ NULL,
    completed_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT group_management_commands_instance_fk FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE,
    CONSTRAINT group_management_commands_instance_identity_unique UNIQUE (id, instance_id),
    CONSTRAINT group_management_commands_group_jid_check CHECK (group_jid ~ '^[^@]+@g[.]us$'),
    CONSTRAINT group_management_commands_type_check CHECK (char_length(command_type) BETWEEN 1 AND 64),
    CONSTRAINT group_management_commands_status_check CHECK (status IN ('requested', 'executing', 'completed', 'partially_completed', 'failed', 'unknown')),
    CONSTRAINT group_management_commands_idempotency_hash_check CHECK (idempotency_key_hash IS NULL OR idempotency_key_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT group_management_commands_fingerprint_check CHECK (request_fingerprint ~ '^[0-9a-f]{64}$'),
    CONSTRAINT group_management_commands_actor_type_check CHECK (actor_type IN ('instance', 'system')),
    CONSTRAINT group_management_commands_actor_hash_check CHECK (actor_reference_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT group_management_commands_outcome_check CHECK (jsonb_typeof(safe_outcome) = 'object'),
    CONSTRAINT group_management_commands_time_check CHECK (
        updated_at >= created_at
        AND
        (execution_started_at IS NULL OR execution_started_at >= created_at)
        AND (completed_at IS NULL OR completed_at >= created_at)
    ),
    CONSTRAINT group_management_commands_lifecycle_check CHECK (
        (status = 'requested' AND execution_started_at IS NULL AND completed_at IS NULL)
        OR (status = 'executing' AND execution_started_at IS NOT NULL AND completed_at IS NULL)
        OR (status IN ('completed', 'partially_completed', 'failed', 'unknown') AND completed_at IS NOT NULL)
    )
);

CREATE UNIQUE INDEX group_management_commands_idempotency_idx
ON group_management_commands (instance_id, idempotency_key_hash)
WHERE idempotency_key_hash IS NOT NULL;

CREATE INDEX group_management_commands_group_history_idx
ON group_management_commands (instance_id, group_jid, created_at DESC, id DESC);

CREATE INDEX group_management_commands_recovery_idx
ON group_management_commands (updated_at ASC, id ASC)
WHERE status = 'executing';

CREATE TABLE group_management_audit_events (
    id UUID PRIMARY KEY,
    command_id UUID NOT NULL,
    instance_id UUID NOT NULL,
    group_jid VARCHAR(255) NOT NULL,
    event_type VARCHAR(32) NOT NULL,
    actor_type VARCHAR(32) NOT NULL,
    actor_reference_hash VARCHAR(64) NOT NULL,
    summary JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT group_management_audit_command_fk FOREIGN KEY (command_id, instance_id) REFERENCES group_management_commands(id, instance_id) ON DELETE CASCADE,
    CONSTRAINT group_management_audit_group_jid_check CHECK (group_jid ~ '^[^@]+@g[.]us$'),
    CONSTRAINT group_management_audit_event_type_check CHECK (event_type IN ('requested', 'executing', 'completed', 'partially_completed', 'failed', 'unknown')),
    CONSTRAINT group_management_audit_actor_type_check CHECK (actor_type IN ('instance', 'system')),
    CONSTRAINT group_management_audit_actor_hash_check CHECK (actor_reference_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT group_management_audit_summary_check CHECK (jsonb_typeof(summary) = 'object')
);

CREATE INDEX group_management_audit_history_idx
ON group_management_audit_events (instance_id, group_jid, occurred_at DESC, id DESC);`,
	},
	{
		Version: 31,
		Name:    "index_group_member_directory",
		SQL: `CREATE INDEX projected_group_participants_member_page_idx
ON projected_group_participants (instance_id, group_id, (LOWER(COALESCE(display_name, ''))), public_id)
WHERE tombstoned_at IS NULL;

CREATE INDEX projected_group_participants_member_search_idx
ON projected_group_participants (instance_id, group_id, (LOWER(COALESCE(display_name, ''))) text_pattern_ops, public_id)
WHERE tombstoned_at IS NULL;`,
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
