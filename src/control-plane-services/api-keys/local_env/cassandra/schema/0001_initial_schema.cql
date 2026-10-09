-- SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
-- SPDX-License-Identifier: Apache-2.0
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.
-- You may obtain a copy of the License at
--
--     http://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing, software
-- distributed under the License is distributed on an "AS IS" BASIS,
-- WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
-- See the License for the specific language governing permissions and
-- limitations under the License.

CREATE KEYSPACE IF NOT EXISTS nvcf_api_keys with replication = {'class':'SimpleStrategy','replication_factor':1};

USE nvcf_api_keys;

CREATE TABLE IF NOT EXISTS keys
(
    api_key_hash TEXT,
    status       TEXT,
    expires_at   TIMESTAMP,
    deletes_at   TIMESTAMP,
    key_details  TEXT,
    PRIMARY KEY ((api_key_hash))
);

CREATE TABLE IF NOT EXISTS keys_by_owner_and_service
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
CREATE TABLE IF NOT EXISTS row_update_lock
(
    table_name TEXT,
    record_key TEXT,
    updated_at TIMESTAMP,
    PRIMARY KEY ((table_name, record_key))
);

-- keys and keys_by_owner_and_service stay as legacy tables until the migration completes.
-- keys_v2 holds keys issued to an account. Hash lookup stays keyed by api_key_hash, and
-- nca_id is a regular column that the application always sets.
CREATE TABLE IF NOT EXISTS keys_v2
(
    api_key_hash TEXT,
    nca_id       TEXT,
    status       TEXT,
    expires_at   TIMESTAMP,
    deletes_at   TIMESTAMP,
    key_details  TEXT,
    PRIMARY KEY ((api_key_hash))
);

-- Account-scoped management index for keys in keys_v2.
CREATE TABLE IF NOT EXISTS keys_by_owner_and_account_and_service
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

-- SAI on the management table for admin and bulk selection that does not
-- have the full partition key. These indexes select rows only. Bulk updates
-- still write each key in keys and keys_by_owner_and_account_and_service.

CREATE CUSTOM INDEX IF NOT EXISTS keys_by_scope_nca_idx
    ON keys_by_owner_and_account_and_service (nca_id)
    USING 'StorageAttachedIndex';

CREATE CUSTOM INDEX IF NOT EXISTS keys_by_scope_owner_idx
    ON keys_by_owner_and_account_and_service (owner_id)
    USING 'StorageAttachedIndex';

CREATE CUSTOM INDEX IF NOT EXISTS keys_by_scope_owner_type_idx
    ON keys_by_owner_and_account_and_service (owner_type)
    USING 'StorageAttachedIndex';

CREATE CUSTOM INDEX IF NOT EXISTS keys_by_scope_service_idx
    ON keys_by_owner_and_account_and_service (issuer_service_id)
    USING 'StorageAttachedIndex';

CREATE CUSTOM INDEX IF NOT EXISTS keys_by_scope_status_idx
    ON keys_by_owner_and_account_and_service (key_status)
    USING 'StorageAttachedIndex';

CREATE CUSTOM INDEX IF NOT EXISTS keys_by_scope_created_at_idx
    ON keys_by_owner_and_account_and_service (created_at)
    USING 'StorageAttachedIndex';

CREATE TABLE IF NOT EXISTS owner_status_by_account
(
    nca_id       TEXT,
    owner_type   TEXT,
    owner_id     TEXT,
    owner_status TEXT,
    created_at   TIMESTAMP,
    updated_at   TIMESTAMP,
    PRIMARY KEY ((nca_id, owner_type, owner_id))
);

CREATE TABLE IF NOT EXISTS owner_status_by_account_and_service
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

CREATE TABLE IF NOT EXISTS key_operations_by_id
(
    operation_id        UUID,
    actor_type          TEXT,
    actor_id            TEXT,
    operation           TEXT,
    nca_ids             FROZEN<SET<TEXT>>,
    issuer_service_ids  FROZEN<SET<TEXT>>,
    user_ids            FROZEN<SET<TEXT>>,
    operation_status    TEXT,
    matched_count       BIGINT,
    completed_count     BIGINT,
    failed_count        BIGINT,
    selection_state     TEXT,
    paging_state        TEXT,
    reason              TEXT,
    cutoff_at           TIMESTAMP,
    created_at          TIMESTAMP,
    updated_at          TIMESTAMP,
    PRIMARY KEY ((operation_id))
);
