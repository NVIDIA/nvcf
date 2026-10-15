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
DROP KEYSPACE IF EXISTS nvct;

CREATE KEYSPACE IF NOT EXISTS nvct WITH REPLICATION = {'class' : 'SimpleStrategy', 'replication_factor' : 1 };

USE nvct;

// UDT for model.
CREATE TYPE IF NOT EXISTS model_udt (
    name       TEXT,
    version    TEXT,
    url        TEXT
);

// UDT for resource
CREATE TYPE IF NOT EXISTS resource_udt (
    name       TEXT,
    version    TEXT,
    url        TEXT
);

CREATE TYPE IF NOT EXISTS gpu_spec_udt(
    instance_type           TEXT,
    gpu                     TEXT,
    backend                 TEXT,
    configuration           TEXT,
    clusters                FROZEN<SET<TEXT>>,
    regions                 FROZEN<SET<TEXT>>,
    attributes              FROZEN<SET<TEXT>>,
    max_request_concurrency INT,
    helm_validation_policy  TEXT
);

CREATE TYPE IF NOT EXISTS health_udt (
    sis_request_id    UUID,
    gpu               TEXT,
    backend           TEXT,
    instance_type     TEXT,
    error             TEXT
);

CREATE TYPE IF NOT EXISTS telemetries_udt
(
    logs_telemetry_id          UUID,
    metrics_telemetry_id       UUID,
    traces_telemetry_id        UUID,
);

CREATE TABLE IF NOT EXISTS tasks_v2
(
    nca_id                         TEXT,
    task_id                        UUID,
    name                           TEXT,
    description                    TEXT,
    tags                           FROZEN<SET<TEXT>>,
    container_image                TEXT,
    container_args                 TEXT,
    container_environment          TEXT,
    models                         FROZEN<SET<model_udt>>,
    resources                      FROZEN<SET<resource_udt>>,
    gpu_spec                       gpu_spec_udt,
    max_runtime_duration           DURATION,
    max_queued_duration            DURATION,
    terminal_grace_period_duration DURATION,
    result_handling_strategy       TEXT,
    helm_chart                     TEXT,
    results_location               TEXT,
    status                         TEXT,
    telemetries                    FROZEN<telemetries_udt>,
    health                         TEXT,
    health_info                    FROZEN<health_udt>,
    percent_complete               INT,
    last_updated_at                TIMESTAMP,
    last_heartbeat_at              TIMESTAMP,
    created_at                     TIMESTAMP,
    has_secrets                    BOOLEAN,
    PRIMARY KEY ((task_id))
);

CREATE CUSTOM INDEX IF NOT EXISTS tasks_v2_by_nca_id_sai_idx ON tasks_v2 (nca_id) USING 'StorageAttachedIndex';

CREATE TABLE IF NOT EXISTS events_by_task
(
    task_id       UUID,
    event_id      UUID,
    nca_id        TEXT,
    message       TEXT,
    created_at    TIMESTAMP,
    PRIMARY KEY ((task_id), event_id)
);

CREATE TABLE IF NOT EXISTS results_by_task
(
    task_id       UUID,
    result_id     UUID,
    nca_id        TEXT,
    name          TEXT,
    metadata      TEXT,
    created_at    TIMESTAMP,
    PRIMARY KEY ((task_id), result_id)
);

// Table for managing distributed locks for scheduled background threads.
// Primary key must be 'name',
CREATE TABLE IF NOT EXISTS lock
(
    name        TEXT,
    lockUntil   TIMESTAMP,
    lockedAt    TIMESTAMP,
    lockedBy    TEXT,
    PRIMARY KEY ((name))
);
