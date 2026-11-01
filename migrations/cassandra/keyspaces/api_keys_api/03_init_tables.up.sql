-- Canonical schema for api_keys_api keyspace.

CREATE TABLE IF NOT EXISTS api_keys_api.keys
(
    api_key_hash TEXT,
    status       TEXT,
    expires_at   TIMESTAMP,
    deletes_at   TIMESTAMP,
    key_details  TEXT,
    PRIMARY KEY ((api_key_hash))
);

CREATE TABLE IF NOT EXISTS api_keys_api.keys_by_owner_and_service
(
    owner_type              TEXT,
    owner_id                TEXT,
    issuer_service_id       TEXT,
    key_id                  TEXT,
    owner_status            TEXT STATIC,
    owner_status_updated_at TIMESTAMP STATIC,
    expires_at              TIMESTAMP,
    deletes_at              TIMESTAMP,
    key_status              TEXT,
    key_details             TEXT,
    PRIMARY KEY ((owner_type, owner_id), issuer_service_id, key_id)
);

--
-- This table helps enforce updates frequency in other tables. The primary key consist of the
-- table tobe updated and record key is combination of descriptive partition key name plus key
-- value. updated_at is when record was last written and TTL on the row signals when record can be
-- written again.
-- This check happens in this table to avoid LWTs in tables which engaged in critical flows.
-- For example, we don't want to lock KEYS table with LWT. When we get to millions of keys we can
-- distribute locking to more tables.
--
CREATE TABLE IF NOT EXISTS api_keys_api.row_update_lock
(
    table_name TEXT,
    record_key TEXT,
    updated_at TIMESTAMP,
    PRIMARY KEY ((table_name, record_key))
);
