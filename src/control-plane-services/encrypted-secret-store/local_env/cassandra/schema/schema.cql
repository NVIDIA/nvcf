-- SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
-- SPDX-License-Identifier: Apache-2.0
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.
-- You may obtain a copy of the License at
--
-- http://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing, software
-- distributed under the License is distributed on an "AS IS" BASIS,
-- WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
-- See the License for the specific language governing permissions and
-- limitations under the License.
CREATE KEYSPACE IF NOT EXISTS ess WITH REPLICATION = {'class':'NetworkTopologyStrategy', 'replication_factor':1};

CREATE TYPE IF NOT EXISTS ess.authorization (
    id            text, -- sub claim for OAuth and Notary
    name          text, -- label, display name
    jwks_url      text, -- derived from issuer for OAuth and Notary
    issuer        text, -- required for agent auth
    type          text  -- notary, oauth. Hygiene only; not used for routing
);

-- UDT for extensibility of any additional fields per entity
CREATE TYPE IF NOT EXISTS ess.entity_type (
    name       text,
    deleted_at timestamp -- missing == not deleted
);

CREATE TABLE IF NOT EXISTS ess.namespaces
(
    namespace                               text,
    oauth_authorizations                    map<text, frozen<authorization>>,
    notary_authorizations                   map<text, frozen<authorization>>,
    entity_types                            map<text, frozen<entity_type>>,
    created_at                              timestamp,
    updated_at                              timestamp,
    entity_hash_size                        int, -- global for now, default 10
    previous_entity_hash_size               int,
    deleted_at                              timestamp, -- missing == not deleted
    require_lwt_for_secret_version_writes   boolean,   -- missing == false
    authorizations_version                  timeuuid,
    PRIMARY KEY                (namespace)
);

CREATE TABLE IF NOT EXISTS ess.entities
(
    namespace             text,
    hash_bucket           int,
    entity_type           text,
    entity_id             text,
    created_at            timestamp,
    PRIMARY KEY           ((namespace, entity_type, hash_bucket), entity_id)
);


CREATE TABLE IF NOT EXISTS ess.secret_paths_by_entity
(
    namespace             text,
    entity                text, -- <entityType>/<entityId>
    path                  text, -- dirA/dirB/secret1
    updated_at            timeuuid,
    entity_version        timeuuid static,
    is_dir                boolean, -- missing value == false
    PRIMARY KEY           ((namespace, entity), path)
);

CREATE TABLE IF NOT EXISTS ess.secret_versions_by_entity_and_path
(
    namespace             text,
    entity                text,
    secret_path           text,
    version               timeuuid,
    current_version       timeuuid static,
    value                 text,
    created_at            timestamp,
    encrypted_at          timestamp,
    encrypted_by_kid      text,
    PRIMARY KEY           ((namespace, entity, secret_path), version)
) WITH CLUSTERING ORDER BY (version DESC);

CREATE TABLE IF NOT EXISTS ess.encryption_keys_by_kid
(
    namespace             text,
    kid                   text,
    created_at            timeuuid,
    encrypted_at          timestamp,
    encrypted_by_kid      text,
    encrypted_key         text,
    PRIMARY KEY           ((namespace), kid)
);

CREATE TABLE IF NOT EXISTS ess.encryption_keys_by_timestamp
(
    namespace             text,
    kid                   text,
    created_at            timeuuid,
    encrypted_at          timestamp,
    encrypted_by_kid      text,
    encrypted_key         text,
    PRIMARY KEY           ((namespace), created_at)
) WITH CLUSTERING ORDER BY (created_at DESC);

CREATE TABLE IF NOT EXISTS ess.encryption_keys_by_kid_and_encrypted_at
(
    namespace             text,
    kid                   text,
    created_at            timeuuid,
    current_kid           text static,
    encrypted_at          timestamp,
    encrypted_by_kid      text,
    encrypted_key         text,
    status                text,
    PRIMARY KEY           ((namespace), kid, encrypted_at)
) WITH CLUSTERING ORDER BY (kid ASC, encrypted_at DESC);
