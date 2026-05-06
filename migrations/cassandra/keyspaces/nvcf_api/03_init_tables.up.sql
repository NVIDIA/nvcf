-- Canonical schema for nvcf_api keyspace.

-- ============================================================
-- User-Defined Types
-- ============================================================

CREATE TYPE IF NOT EXISTS nvcf_api.model_udt (
    name    TEXT,
    version TEXT,
    url     TEXT
);

CREATE TYPE IF NOT EXISTS nvcf_api.resource_udt (
    name    TEXT,
    version TEXT,
    url     TEXT
);

CREATE TYPE IF NOT EXISTS nvcf_api.gpu_spec_udt (
    gpu_specification_id    UUID,
    backend                 TEXT,
    gpu                     TEXT,
    min_instances           INT,
    max_instances           INT,
    instance_type           TEXT,
    availability_zones      FROZEN<SET<TEXT>>,
    max_request_concurrency INT,
    configuration           TEXT,
    clusters                FROZEN<SET<TEXT>>,
    regions                 FROZEN<SET<TEXT>>,
    attributes              FROZEN<SET<TEXT>>,
    preferred_order         INT
);

CREATE TYPE IF NOT EXISTS nvcf_api.deployment_health_udt (
    sis_request_id UUID,
    gpu            TEXT,
    backend        TEXT,
    instance_type  TEXT,
    error          TEXT
);

CREATE TYPE IF NOT EXISTS nvcf_api.health_udt (
    protocol             TEXT,
    port                 INT,
    uri                  TEXT,
    timeout              DURATION,
    expected_status_code INT
);

CREATE TYPE IF NOT EXISTS nvcf_api.ratelimit_udt_v2 (
    rate             TEXT,
    exempted_nca_ids FROZEN<SET<TEXT>>,
    per_nca_id_rate  FROZEN<MAP<TEXT, TEXT>>,
    sync_check       BOOLEAN
);

CREATE TYPE IF NOT EXISTS nvcf_api.telemetries_udt (
    logs_telemetry_id    UUID,
    metrics_telemetry_id UUID,
    traces_telemetry_id  UUID
);

-- ============================================================
-- Tables
-- ============================================================

-- Each row represents a customer/partner account provisioned by NVIDIA Super Admin.
CREATE TABLE IF NOT EXISTS nvcf_api.accounts (
    nca_id                           TEXT,
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

-- Maps SSA Client IDs to NVIDIA Cloud Account IDs.
CREATE TABLE IF NOT EXISTS nvcf_api.clients (
    client_id  TEXT,
    nca_id     TEXT,
    name       TEXT,
    created_at TIMESTAMP,
    PRIMARY KEY ((client_id))
);

-- Primary function/version store. Each row is a unique function version.
-- function_level_authz_accounts: NCA IDs authorized for all versions of the function.
-- version_level_authz_accounts:  NCA IDs authorized for this specific version only.
CREATE TABLE IF NOT EXISTS nvcf_api.functions_v3 (
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
    model_specs                   MAP<TEXT, TEXT>,
    models                        FROZEN<SET<model_udt>>,
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
    function_level_authz_accounts SET<TEXT>,
    version_level_authz_accounts  SET<TEXT>,
    PRIMARY KEY ((function_version_id))
);

CREATE CUSTOM INDEX IF NOT EXISTS functions_v3_by_function_id_sai_idx
    ON nvcf_api.functions_v3 (function_id) USING 'StorageAttachedIndex';
CREATE CUSTOM INDEX IF NOT EXISTS functions_v3_by_nca_id_sai_idx
    ON nvcf_api.functions_v3 (nca_id) USING 'StorageAttachedIndex';
CREATE INDEX IF NOT EXISTS function_level_authz_accounts_idx
    ON nvcf_api.functions_v3 (function_level_authz_accounts);
CREATE INDEX IF NOT EXISTS version_level_authz_accounts_idx
    ON nvcf_api.functions_v3 (version_level_authz_accounts);

-- Deployment state per function version, keyed by gpu spec id.
CREATE TABLE IF NOT EXISTS nvcf_api.functions_deployment_v2 (
    function_version_id UUID,
    deployment_id       UUID,
    function_id         UUID,
    nca_id              TEXT,
    gpu_specs           MAP<UUID, FROZEN<gpu_spec_udt>>,
    health_info         FROZEN<SET<deployment_health_udt>>,
    created_at          TIMESTAMP,
    last_updated_at     TIMESTAMP,
    autoscaling_config  MAP<UUID, blob>,
    PRIMARY KEY ((function_version_id))
);

CREATE CUSTOM INDEX IF NOT EXISTS deployment_v2_by_deployment_id_sai_idx
    ON nvcf_api.functions_deployment_v2 (deployment_id) USING 'StorageAttachedIndex';
CREATE CUSTOM INDEX IF NOT EXISTS deployment_v2_by_nca_id_sai_idx
    ON nvcf_api.functions_deployment_v2 (nca_id) USING 'StorageAttachedIndex';

-- Tracks active SIS requests per function version.
-- Tuned for high-churn write/delete workload: UCS + short gc_grace.
CREATE TABLE IF NOT EXISTS nvcf_api.sis_requests_by_function_v2 (
    function_version_id UUID,
    sis_request_id      UUID,
    created_at          TIMESTAMP,
    total_request_size  INT,
    PRIMARY KEY ((function_version_id), sis_request_id)
) WITH CLUSTERING ORDER BY (sis_request_id ASC)
    AND read_repair = 'NONE'
    AND gc_grace_seconds = 129600
    AND compaction = {
        'class': 'UnifiedCompactionStrategy',
        'scaling_parameters': 'L10',
        'target_sstable_size': '100MiB',
        'base_shard_count': '4',
        'only_purge_repaired_tombstones': 'true'
    };

-- Telemetry endpoint registrations per account.
CREATE TABLE IF NOT EXISTS nvcf_api.telemetries_by_account (
    nca_id       TEXT,
    telemetry_id UUID,
    name         TEXT,
    endpoint     TEXT,
    protocol     TEXT,
    provider     TEXT,
    types        SET<TEXT>,
    created_at   TIMESTAMP,
    PRIMARY KEY ((nca_id), telemetry_id)
) WITH CLUSTERING ORDER BY (telemetry_id ASC);

-- Registry credentials per account.
CREATE TABLE IF NOT EXISTS nvcf_api.registry_credentials_by_account (
    nca_id                   TEXT,
    registry_credential_id   UUID,
    registry_name            TEXT,
    registry_hostname        TEXT,
    registry_credential_name TEXT,
    artifact_types           FROZEN<SET<TEXT>>,
    tags                     FROZEN<SET<TEXT>>,
    description              TEXT,
    provisioned_by           TEXT,
    created_at               TIMESTAMP,
    last_updated_at          TIMESTAMP,
    PRIMARY KEY ((nca_id), registry_credential_id)
);

-- GPU specifications per deployment, migrated from functions_deployment_v2.gpu_specs.
CREATE TABLE IF NOT EXISTS nvcf_api.gpu_specifications (
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

-- Distributed lock table for scheduled tasks.
CREATE TABLE IF NOT EXISTS nvcf_api.lock (
    name      TEXT,
    lockuntil TIMESTAMP,
    lockedat  TIMESTAMP,
    lockedby  TEXT,
    PRIMARY KEY ((name))
);
