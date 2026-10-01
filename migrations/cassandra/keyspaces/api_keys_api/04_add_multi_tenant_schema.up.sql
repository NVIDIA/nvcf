-- Multi-tenant API Keys tables. 03_init_tables.up.sql stays the original schema.
-- New and existing clusters apply this file. Statements are idempotent.
-- Keep keys_by_owner_and_service until the dual-write migration stops using it.
-- Hash lookup stays keyed by api_key_hash. nca_id is a regular column.

ALTER TABLE api_keys_api.keys ADD IF NOT EXISTS nca_id TEXT;

CREATE TABLE IF NOT EXISTS api_keys_api.keys_by_account_owner_and_service
(
    nca_id            TEXT,
    owner_type        TEXT,
    owner_id          TEXT,
    issuer_service_id TEXT,
    key_id            TEXT,
    key_status        TEXT,
    created_at        TIMESTAMP,
    expires_at        TIMESTAMP,
    deletes_at        TIMESTAMP,
    key_details       TEXT,
    PRIMARY KEY ((nca_id, owner_type, owner_id), issuer_service_id, key_id)
);

CREATE CUSTOM INDEX IF NOT EXISTS keys_by_scope_nca_idx
    ON api_keys_api.keys_by_account_owner_and_service (nca_id)
    USING 'StorageAttachedIndex';

CREATE CUSTOM INDEX IF NOT EXISTS keys_by_scope_owner_idx
    ON api_keys_api.keys_by_account_owner_and_service (owner_id)
    USING 'StorageAttachedIndex';

CREATE CUSTOM INDEX IF NOT EXISTS keys_by_scope_owner_type_idx
    ON api_keys_api.keys_by_account_owner_and_service (owner_type)
    USING 'StorageAttachedIndex';

CREATE CUSTOM INDEX IF NOT EXISTS keys_by_scope_service_idx
    ON api_keys_api.keys_by_account_owner_and_service (issuer_service_id)
    USING 'StorageAttachedIndex';

CREATE CUSTOM INDEX IF NOT EXISTS keys_by_scope_status_idx
    ON api_keys_api.keys_by_account_owner_and_service (key_status)
    USING 'StorageAttachedIndex';

CREATE CUSTOM INDEX IF NOT EXISTS keys_by_scope_created_at_idx
    ON api_keys_api.keys_by_account_owner_and_service (created_at)
    USING 'StorageAttachedIndex';

CREATE TABLE IF NOT EXISTS api_keys_api.owner_status_by_account
(
    nca_id       TEXT,
    owner_type   TEXT,
    owner_id     TEXT,
    owner_status TEXT,
    created_at   TIMESTAMP,
    updated_at   TIMESTAMP,
    PRIMARY KEY ((nca_id, owner_type, owner_id))
);

CREATE TABLE IF NOT EXISTS api_keys_api.owner_status_by_account_and_service
(
    nca_id            TEXT,
    owner_type        TEXT,
    owner_id          TEXT,
    issuer_service_id TEXT,
    owner_status      TEXT,
    created_at        TIMESTAMP,
    updated_at        TIMESTAMP,
    PRIMARY KEY ((nca_id, owner_type, owner_id, issuer_service_id))
);

-- Discover owner status across accounts independently of key records.
CREATE CUSTOM INDEX IF NOT EXISTS owner_status_by_account_owner_idx
    ON api_keys_api.owner_status_by_account (owner_id)
    USING 'StorageAttachedIndex';

CREATE CUSTOM INDEX IF NOT EXISTS owner_status_by_account_and_service_owner_idx
    ON api_keys_api.owner_status_by_account_and_service (owner_id)
    USING 'StorageAttachedIndex';

CREATE TABLE IF NOT EXISTS api_keys_api.key_operations_by_id
(
    operation_id       UUID,
    actor_type         TEXT,
    actor_id           TEXT,
    operation          TEXT,
    nca_ids            FROZEN<SET<TEXT>>,
    issuer_service_ids FROZEN<SET<TEXT>>,
    user_ids           FROZEN<SET<TEXT>>,
    operation_status   TEXT,
    matched_count      BIGINT,
    completed_count    BIGINT,
    failed_count       BIGINT,
    selection_state    TEXT,
    paging_state       TEXT,
    reason             TEXT,
    cutoff_at          TIMESTAMP,
    created_at         TIMESTAMP,
    updated_at         TIMESTAMP,
    PRIMARY KEY ((operation_id))
);
