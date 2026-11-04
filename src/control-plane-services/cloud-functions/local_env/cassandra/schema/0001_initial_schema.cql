-- SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
DROP KEYSPACE IF EXISTS nvcf;

CREATE KEYSPACE IF NOT EXISTS nvcf WITH REPLICATION = {'class' : 'SimpleStrategy', 'replication_factor' : 1 };

// UDT for resource
CREATE TYPE IF NOT EXISTS nvcf.resource_udt (
    name       TEXT,
    version    TEXT,
    url        TEXT
);

CREATE TYPE IF NOT EXISTS nvcf.deployment_health_udt (
    sis_request_id    UUID,
    gpu               TEXT,
    backend           TEXT,
    instance_type     TEXT,
    error             TEXT
);

CREATE TYPE IF NOT EXISTS nvcf.health_udt (
    protocol                TEXT,
    port                    INT,
    uri                     TEXT,
    timeout                 DURATION,
    expected_status_code    INT
);

CREATE TYPE IF NOT EXISTS nvcf.ratelimit_udt_v2(
    rate             TEXT,
    exempted_nca_ids FROZEN<SET<TEXT>>,
    per_nca_id_rate  FROZEN<MAP<TEXT, TEXT>>,
    sync_check       BOOLEAN,
    per_user_rate    TEXT
);


// This user-defined type (UDT) represents telemetry identifiers for different telemetry types.
CREATE TYPE IF NOT EXISTS nvcf.telemetries_udt
(
    logs_telemetry_id          UUID,
    metrics_telemetry_id       UUID,
    traces_telemetry_id        UUID,
);

// Each row in this table represents a customer/partner account. A row is added
// to this table when NVIDIA's Super Admin provisions the account.
CREATE TABLE IF NOT EXISTS nvcf.accounts
(
    nca_id                           TEXT, // NVIDIA Cloud Account ID
    client_ids                       FROZEN<SET<TEXT>>,
    name                             TEXT,
    max_functions_allowed            INT,
    max_tasks_allowed                INT,
    max_telemetries_allowed          INT,
    max_registry_credentials_allowed INT,
    created_at                       TIMESTAMP,
    last_updated_at                  TIMESTAMP,
    PRIMARY KEY ((nca_id))
);

// This table provides the mapping between the OAuth2 Client Id and NVIDIA Cloud
// Account Id. Multiple OAuth2 Client Ids can map to the same NVIDIA Cloud Account
// Id.
CREATE TABLE IF NOT EXISTS nvcf.clients (
    client_id      TEXT,    // OAuth2 ClientID
    nca_id         TEXT,    // NVIDIA Cloud Account ID
    name           TEXT,
    created_at     TIMESTAMP,
    PRIMARY KEY ((client_id))
);

// A new row is added to this table whenever a new function is created. If
// the newly created function is different version of an existing function,
// then function_id column will have same value as the original function.
//
// Column function_level_authz_accounts can optionally contain authorized NCA Ids
// representing accounts that will automatically share all the versions of the
// function. All the versions of a specific function will have identical value
// in this column. When a new version is added, this field is automatically set
// based on the corresponding field's value in the oldest version allowing the
// new version to automatically shared with the accounts that were already
// specified at the function level.
//
// Column version_level_authz_accounts can optionally contain authorized NCA IDs
// representing accounts that will share just this specific version. This means
// that each version can be shared with different set of accounts. Adding or
// removing NCA Ids from this field does not impact sharing of other versions
// of the function.
//
// When a function becomes public, all the current and future versions automatically
// become public. We wipe out existing values in the function_level_authz_accounts
// field in all the entries/versions and update the set to contain AUTHORIZED-WILDCARD-ACCOUNT.
//
// When a version becomes public, we wipe out existing values in version_level_authz_accounts
// field for that specific entry/version and update the set to contain AUTHORIZED-WILDCARD-ACCOUNT.
//
// As each entry is self sufficient, deleting any entry does not impact any of the
// remaining or newly added entries.

CREATE TABLE IF NOT EXISTS nvcf.functions_v3
(
    function_id                   UUID,
    function_version_id           UUID,
    nca_id                        TEXT,
    function_name                 TEXT,
    function_status               TEXT,
    inference_url                 TEXT,
    inference_port                INT,
    api_body_format               TEXT,
    container_image               TEXT,
    utils_container_image         TEXT,
    // -- Embedded per-model JSON keyed by model name.
    model_specs                   MAP<TEXT, TEXT>,
    // -- Function-level LLM config JSON (priority and future settings).
    llm_config                    TEXT,
    container_args                TEXT,
    container_environment         TEXT,
    helm_chart                    TEXT,
    helm_chart_service_name       TEXT,
    created_at                    TIMESTAMP,
    resources                     FROZEN<SET<resource_udt>>,
    function_type                 TEXT,
    ratelimit                     ratelimit_udt_v2,
    tags                          FROZEN<SET<TEXT>>,
    description                   TEXT,
    health                        FROZEN<health_udt>,
    telemetries                   FROZEN<telemetries_udt>,
    has_secrets                   BOOLEAN,
    function_level_authz_accounts SET<TEXT>, // contains authorized ncaIds for the whole function
    version_level_authz_accounts  SET<TEXT>, // contains authorized ncaIds for only this version
    PRIMARY KEY ((function_version_id))
);

CREATE CUSTOM INDEX IF NOT EXISTS functions_v3_by_function_id_sai_idx
    ON nvcf.functions_v3 (function_id) USING 'StorageAttachedIndex';
CREATE CUSTOM INDEX IF NOT EXISTS functions_v3_by_nca_id_sai_idx
    ON nvcf.functions_v3 (nca_id) USING 'StorageAttachedIndex';
CREATE INDEX IF NOT EXISTS function_level_authz_accounts_idx ON nvcf.functions_v3 (function_level_authz_accounts);
CREATE INDEX IF NOT EXISTS version_level_authz_accounts_idx ON nvcf.functions_v3 (version_level_authz_accounts);

CREATE TABLE IF NOT EXISTS nvcf.functions_deployment_v2
(
    function_version_id     UUID,
    deployment_id           UUID,
    function_id             UUID,
    nca_id                  TEXT,
    health_info             FROZEN<SET<deployment_health_udt>>,
    created_at              TIMESTAMP,
    last_updated_at         TIMESTAMP,
    PRIMARY KEY ((function_version_id))
);
CREATE CUSTOM INDEX IF NOT EXISTS deployment_v2_by_deployment_id_sai_idx
    ON nvcf.functions_deployment_v2 (deployment_id) USING 'StorageAttachedIndex';
CREATE CUSTOM INDEX IF NOT EXISTS deployment_v2_by_nca_id_sai_idx
    ON nvcf.functions_deployment_v2 (nca_id) USING 'StorageAttachedIndex';

CREATE TABLE IF NOT EXISTS nvcf.gpu_specifications
(
    gpu_specification_id      UUID,
    deployment_id             UUID,
    nca_id                    TEXT,
    backend                   TEXT,
    gpu                       TEXT,
    min_instances             INT,
    max_instances             INT,
    instance_type             TEXT,
    availability_zones        SET<TEXT>,
    max_request_concurrency   INT,
    configuration             TEXT,
    clusters                  SET<TEXT>,
    regions                   SET<TEXT>,
    attributes                SET<TEXT>,
    preferred_order           INT,
    autoscaling_configuration TEXT,
    helm_validation_policy    TEXT,
    PRIMARY KEY ((nca_id), deployment_id, gpu_specification_id)
);

CREATE TABLE IF NOT EXISTS nvcf.telemetries_by_account (
   nca_id             TEXT,
   telemetry_id       UUID,
   name               TEXT,
   endpoint           TEXT,
   protocol           TEXT,
   provider           TEXT,
   types              SET<TEXT>,
   created_at         TIMESTAMP,
   PRIMARY KEY ((nca_id), telemetry_id)
);

CREATE TABLE IF NOT EXISTS nvcf.registry_credentials_by_account (
   nca_id                      TEXT,
   registry_credential_id      UUID,
   registry_name               TEXT,
   registry_hostname           TEXT,
   registry_credential_name    TEXT,
   artifact_types              FROZEN<SET<TEXT>>,
   tags                        FROZEN<SET<TEXT>>,
   description                 TEXT,
   provisioned_by              TEXT,
   created_at                  TIMESTAMP,
   last_updated_at             TIMESTAMP,
   PRIMARY KEY ((nca_id), registry_credential_id)
);

// Table for managing distributed locks for scheduled tasks.
// Primary key must be 'name',
CREATE TABLE IF NOT EXISTS nvcf.lock
(
    name      text,
    lockUntil timestamp,
    lockedAt  timestamp,
    lockedBy  text,
    PRIMARY KEY ((name))
);
