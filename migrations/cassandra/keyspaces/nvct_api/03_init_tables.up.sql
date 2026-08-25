-- Canonical schema for nvct_api keyspace.
-- Reference: nvcf/nvct-api v1.5.2 local_env/cassandra/schema/0001_initial_schema.cql

-- ============================================================
-- User-Defined Types
-- ============================================================

CREATE TYPE IF NOT EXISTS nvct_api.model_udt (
    name    TEXT,
    version TEXT,
    url     TEXT
);

CREATE TYPE IF NOT EXISTS nvct_api.resource_udt (
    name    TEXT,
    version TEXT,
    url     TEXT
);

CREATE TYPE IF NOT EXISTS nvct_api.gpu_spec_udt (
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

CREATE TYPE IF NOT EXISTS nvct_api.health_udt (
    sis_request_id UUID,
    gpu            TEXT,
    backend        TEXT,
    instance_type  TEXT,
    error          TEXT
);

CREATE TYPE IF NOT EXISTS nvct_api.telemetries_udt (
    logs_telemetry_id    UUID,
    metrics_telemetry_id UUID,
    traces_telemetry_id  UUID
);

-- ============================================================
-- Tables
-- ============================================================

-- Primary task store. Each row is a unique task instance.
CREATE TABLE IF NOT EXISTS nvct_api.tasks_v2 (
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

CREATE CUSTOM INDEX IF NOT EXISTS tasks_v2_by_nca_id_sai_idx
    ON nvct_api.tasks_v2 (nca_id) USING 'StorageAttachedIndex';

-- Tracks active SIS requests per task.
CREATE TABLE IF NOT EXISTS nvct_api.sis_requests_by_task (
    task_id            UUID,
    sis_request_id     UUID,
    gpu_spec           gpu_spec_udt,
    total_request_size INT,
    created_at         TIMESTAMP,
    PRIMARY KEY ((task_id), sis_request_id)
);

-- Events emitted during task lifecycle, keyed by task.
CREATE TABLE IF NOT EXISTS nvct_api.events_by_task (
    task_id    UUID,
    event_id   UUID,
    nca_id     TEXT,
    message    TEXT,
    created_at TIMESTAMP,
    PRIMARY KEY ((task_id), event_id)
);

-- Results produced by a task, keyed by task.
CREATE TABLE IF NOT EXISTS nvct_api.results_by_task (
    task_id    UUID,
    result_id  UUID,
    nca_id     TEXT,
    name       TEXT,
    metadata   TEXT,
    created_at TIMESTAMP,
    PRIMARY KEY ((task_id), result_id)
);

-- Distributed lock table for scheduled background tasks.
-- Primary key must be 'name'.
CREATE TABLE IF NOT EXISTS nvct_api.lock (
    name      TEXT,
    lockuntil TIMESTAMP,
    lockedat  TIMESTAMP,
    lockedby  TEXT,
    PRIMARY KEY ((name))
);
