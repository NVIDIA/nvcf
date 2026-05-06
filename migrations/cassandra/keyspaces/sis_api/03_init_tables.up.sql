-- Canonical schema for sis_api keyspace.

-- ============================================================
-- User-Defined Types
-- ============================================================

CREATE TYPE IF NOT EXISTS sis_api.gpucapacity (
    capacity  int,
    allocated int,
    available int
);

CREATE TYPE IF NOT EXISTS sis_api.instance_type (
    cpu_cores     int,
    gpu           text,
    system_memory text,
    gpu_memory    text,
    name          text
);

CREATE TYPE IF NOT EXISTS sis_api.instancetype (
    cpu_cores     int,
    system_memory text,
    gpu_memory    text,
    gpu_count     int,
    name          text,
    description   text,
    value         text,
    is_default    boolean
);

CREATE TYPE IF NOT EXISTS sis_api.instancetypev2 (
    cpu_cores      int,
    system_memory  text,
    gpu_memory     text,
    gpu_count      int,
    name           text,
    description    text,
    value          text,
    is_default     boolean,
    cpu_arch       text,
    os             text,
    driver_version text
);

CREATE TYPE IF NOT EXISTS sis_api.instancetypev3 (
    cpu_cores      int,
    system_memory  text,
    gpu_memory     text,
    gpu_count      int,
    name           text,
    description    text,
    value          text,
    is_default     boolean,
    cpu_arch       text,
    os             text,
    driver_version text,
    storage        text
);

CREATE TYPE IF NOT EXISTS sis_api.instancetypev5 (
    cpu_cores           int,
    system_memory       text,
    gpu_memory          text,
    gpu_count           int,
    name                text,
    description         text,
    value               text,
    is_default          boolean,
    cpu_arch            text,
    os                  text,
    driver_version      text,
    storage             text,
    instance_type_usage text
);

CREATE TYPE IF NOT EXISTS sis_api.gpu (
    name           text,
    instance_types FROZEN<SET<instancetype>>
);

CREATE TYPE IF NOT EXISTS sis_api.gpusv2 (
    name           text,
    capacity       int,
    instance_types SET<FROZEN<instancetype>>
);

CREATE TYPE IF NOT EXISTS sis_api.gpusv3 (
    name           text,
    capacity       int,
    instance_types SET<FROZEN<instancetypev2>>
);

CREATE TYPE IF NOT EXISTS sis_api.gpusv4 (
    name           text,
    capacity       int,
    instance_types SET<FROZEN<instancetypev3>>
);

CREATE TYPE IF NOT EXISTS sis_api.gpusv5 (
    name           text,
    capacity       int,
    instance_types SET<FROZEN<instancetypev5>>
);

CREATE TYPE IF NOT EXISTS sis_api.creation_queue (
    url        text,
    queue_type text
);

-- ============================================================
-- Tables
-- ============================================================

-- Active SIS requests.
CREATE TABLE IF NOT EXISTS sis_api.requests (
    request_id            text,
    create_timeuuid       timeuuid,
    customer              text,
    action                text,
    state                 text,
    status_code           text,
    status_message        text,
    status_update_time    timestamp,
    request               text,
    resource_provider     text,
    check_batchwise_info  boolean,
    clusters              set<text>,
    regions               set<text>,
    attributes            set<text>,
    custom_attributes     set<text>,
    instance_count        int,
    task_id               uuid,
    max_queued_duration   text,
    account_name          text,
    function_id           uuid,
    function_version_id   uuid,
    deployment_id         uuid,
    gpu_specification_id  uuid,
    nca_id                text,
    reservation_id        uuid,
    gpu_count_per_instance int,
    PRIMARY KEY (request_id)
);

CREATE CUSTOM INDEX IF NOT EXISTS idx_requests_by_reservation_id
    ON sis_api.requests (reservation_id) USING 'StorageAttachedIndex';
CREATE CUSTOM INDEX IF NOT EXISTS idx_requests_by_deployment_id
    ON sis_api.requests (deployment_id) USING 'StorageAttachedIndex';
CREATE CUSTOM INDEX IF NOT EXISTS idx_requests_by_nca_id
    ON sis_api.requests (nca_id) USING 'StorageAttachedIndex';

-- Time-bucketed request tombstone tracker (for cleanup jobs).
CREATE TABLE IF NOT EXISTS sis_api.requests_by_day (
    truncated_ts_by_day timestamp,
    request_id          text,
    create_timeuuid     timeuuid,
    marked_as_deleted   boolean,
    PRIMARY KEY (truncated_ts_by_day, request_id)
);

-- Active instances.
CREATE TABLE IF NOT EXISTS sis_api.instances (
    instance_id                         text,
    request_id                          text,
    create_timeuuid                     timeuuid,
    customer                            text,
    image_id                            text,
    request_state                       text,
    instance_update_time                timestamp,
    zone                                text,
    instance_state_code                 int,
    instance_state_name                 text,
    request_status_code                 text,
    request_status_message              text,
    request_status_update_time          timestamp,
    resource_provider                   text,
    error_log                           text,
    error_source                        text,
    nca_id                              text,
    instance_type                       text,
    backend                             text,
    gpu                                 text,
    instance_ips                        set<text>,
    region                              text,
    attributes                          set<text>,
    custom_attributes                   set<text>,
    request_raw_data                    text,
    reservation_id                      uuid,
    gpu_count_per_instance              int,
    cloud_provider                      text,
    capacity_type                       text,
    instance_expiration_time            timestamp,
    backup_to_primary_migration_scheduled boolean,
    PRIMARY KEY (instance_id)
);

CREATE CUSTOM INDEX IF NOT EXISTS idx_instances_by_reservation_id
    ON sis_api.instances (reservation_id) USING 'StorageAttachedIndex';
CREATE CUSTOM INDEX IF NOT EXISTS idx_instances_by_request_id
    ON sis_api.instances (request_id) USING 'StorageAttachedIndex';
CREATE CUSTOM INDEX IF NOT EXISTS idx_instances_by_nca_id
    ON sis_api.instances (nca_id) USING 'StorageAttachedIndex';
CREATE CUSTOM INDEX IF NOT EXISTS idx_instances_by_zone
    ON sis_api.instances (zone) USING 'StorageAttachedIndex';

-- Time-bucketed instance tombstone tracker (for cleanup jobs).
CREATE TABLE IF NOT EXISTS sis_api.instances_by_day (
    truncated_ts_by_day timestamp,
    instance_id         text,
    request_id          text,
    create_timeuuid     timeuuid,
    marked_as_deleted   boolean,
    PRIMARY KEY (truncated_ts_by_day, instance_id)
);

-- Instance lookup by request (used for join-like queries).
CREATE TABLE IF NOT EXISTS sis_api.instances_by_request (
    request_id   text,
    instance_id  text,
    customer     text,
    truncated_ts timestamp,
    PRIMARY KEY (request_id, instance_id)
);

-- Denormalized instance view bucketed by zone/timestamp (for zone-scoped queries).
CREATE TABLE IF NOT EXISTS sis_api.instances_by_zone (
    nca_id                     text,
    instance_type              text,
    backend                    text,
    gpu                        text,
    customer                   text,
    truncated_ts               timestamp,
    request_id                 text,
    instance_id                text,
    image_id                   text,
    request_state              text,
    update_timestamp           timestamp,
    zone                       text,
    instance_state_code        int,
    instance_state_name        text,
    resource_provider          text,
    error_log                  text,
    error_source               text,
    request_status_code        text,
    request_status_message     text,
    request_status_update_time timestamp,
    instance_ips               FROZEN<SET<text>>,
    region                     text,
    attributes                 set<text>,
    custom_attributes          set<text>,
    request                    text,
    PRIMARY KEY ((truncated_ts, zone), instance_id)
);

-- GPU reservations.
CREATE TABLE IF NOT EXISTS sis_api.reservations (
    reservation_id     uuid,
    nca_id             text,
    cluster_id         text,
    gpu_type           text,
    reserved_gpu_count int,
    available_gpu_count double,
    start_time         timestamp,
    end_time           timestamp,
    name               text,
    last_updated_time  timestamp,
    PRIMARY KEY (reservation_id)
);

CREATE CUSTOM INDEX IF NOT EXISTS idx_reservations_by_nca_id
    ON sis_api.reservations (nca_id) USING 'StorageAttachedIndex';
CREATE CUSTOM INDEX IF NOT EXISTS idx_reservations_by_cluster_id
    ON sis_api.reservations (cluster_id) USING 'StorageAttachedIndex';

-- Tenant registration records used by the GDN registration flow.
CREATE TABLE IF NOT EXISTS sis_api.tenant_registration_by_registration_id (
    registration_id         uuid,
    deployment_id           uuid,
    tenant_registration_data map<text, text>,
    tenant                  text,
    nca_id                  text,
    function_version_id     uuid,
    function_id             uuid,
    create_time             timestamp,
    PRIMARY KEY (registration_id)
);

CREATE CUSTOM INDEX IF NOT EXISTS idx_tenant_registration_by_deployment_id
    ON sis_api.tenant_registration_by_registration_id (deployment_id) USING 'StorageAttachedIndex';

-- Distributed lock table for scheduled tasks.
CREATE TABLE IF NOT EXISTS sis_api.lock (
    name      text,
    lockuntil timestamp,
    lockedat  timestamp,
    lockedby  text,
    PRIMARY KEY (name)
);

-- TTL-based distributed lock table.
CREATE TABLE IF NOT EXISTS sis_api.lock_by_ttl (
    lock_name  text,
    locked_at  timestamp,
    locked_by  text,
    lock_ttl   int,
    PRIMARY KEY (lock_name)
);

-- Cloud provider zone-level GPU health metrics.
CREATE TABLE IF NOT EXISTS sis_api.cloud_health (
    cloud_provider         text,
    zone                   text,
    status                 text,
    cluster_upgrade_status text,
    gpu_usage              map<text, FROZEN<gpucapacity>>,
    PRIMARY KEY ((cloud_provider, zone))
);

-- Full cluster state, keyed by (group, cluster) for group-scoped queries.
CREATE TABLE IF NOT EXISTS sis_api.cluster_by_group_id_and_cluster_id (
    cluster_name                       text,
    cluster_id                         text,
    nca_id                             text,
    termination_queue_url              text,
    termination_queue_type             text,
    cluster_description                text,
    cluster_provider                   text,
    cluster_status                     text,
    cluster_source                     text,
    k8s_version                        text,
    cluster_group_name                 text,
    cluster_group_id                   text,
    creation_queue_url                 text,
    creation_queue_type                text,
    gpus                               FROZEN<SET<gpu>>,
    authorized_nca_ids                 set<text>,
    capabilities                       set<text>,
    attributes                         set<text>,
    gpus_v2                            set<FROZEN<gpusv2>>,
    gpus_v3                            set<FROZEN<gpusv3>>,
    gpus_v4                            set<FROZEN<gpusv4>>,
    gpus_v5                            set<FROZEN<gpusv5>>,
    nvca_version                       text,
    ssa_client_id                      text,
    region                             text,
    nvca_last_connected                timestamp,
    creation_queues                    map<text, FROZEN<creation_queue>>,
    cluster_creation_queues            map<text, FROZEN<creation_queue>>,
    cluster_creation_queues_for_tasks  map<text, FROZEN<creation_queue>>,
    request_dump                       text,
    custom_attributes                  set<text>,
    allow_cluster_targeting            boolean,
    allow_task_cluster_creation_queues boolean,
    cluster_key_id                     text,
    PRIMARY KEY (cluster_group_id, cluster_id)
) WITH CLUSTERING ORDER BY (cluster_id ASC);

-- Full cluster state, keyed by cluster_id for direct lookups.
CREATE TABLE IF NOT EXISTS sis_api.cluster_by_cluster_id (
    cluster_name                       text,
    registration_time                  timestamp,
    cluster_id                         text,
    nca_id                             text,
    termination_queue_url              text,
    termination_queue_type             text,
    cluster_description                text,
    cluster_provider                   text,
    cluster_status                     text,
    cluster_source                     text,
    k8s_version                        text,
    cluster_group_name                 text,
    cluster_group_id                   text,
    creation_queue_url                 text,
    creation_queue_type                text,
    gpus                               FROZEN<SET<gpu>>,
    authorized_nca_ids                 set<text>,
    capabilities                       set<text>,
    attributes                         set<text>,
    gpus_v2                            set<FROZEN<gpusv2>>,
    gpus_v3                            set<FROZEN<gpusv3>>,
    gpus_v4                            set<FROZEN<gpusv4>>,
    gpus_v5                            set<FROZEN<gpusv5>>,
    nvca_version                       text,
    ssa_client_id                      text,
    region                             text,
    nvca_last_connected                timestamp,
    creation_queues                    map<text, FROZEN<creation_queue>>,
    cluster_creation_queues            map<text, FROZEN<creation_queue>>,
    cluster_creation_queues_for_tasks  map<text, FROZEN<creation_queue>>,
    request_dump                       text,
    custom_attributes                  set<text>,
    allow_cluster_targeting            boolean,
    allow_task_cluster_creation_queues boolean,
    cluster_key_id                     text,
    healthy_heartbeat_report_time      timestamp,
    PRIMARY KEY (cluster_id)
);

-- Per-cluster advanced configuration blobs (BYOC).
CREATE TABLE IF NOT EXISTS sis_api.cluster_configuration_by_cluster_id (
    cluster_id                   text,
    cluster_configurations       map<text, text>,
    cluster_configuration_files  map<text, text>,
    PRIMARY KEY (cluster_id)
);

-- Cluster group metadata, keyed by group id.
CREATE TABLE IF NOT EXISTS sis_api.cluster_group_by_cluster_group_id (
    cluster_group_name text,
    cluster_group_id   text,
    creation_queue_url text,
    creation_queue_type text,
    gpus               FROZEN<SET<gpu>>,
    nca_id             text,
    authorized_nca_ids set<text>,
    PRIMARY KEY (cluster_group_id)
);

-- Per-cluster heartbeat health tracking.
CREATE TABLE IF NOT EXISTS sis_api.cluster_health (
    health_updated_ts timestamp,
    cluster_id        text,
    PRIMARY KEY (cluster_id)
);

-- Clusters visible to an account (primary lookup path for account-scoped queries).
CREATE TABLE IF NOT EXISTS sis_api.clusters_by_account (
    cluster_name                       text,
    registration_time                  timestamp,
    cluster_id                         text,
    nca_id                             text,
    termination_queue_url              text,
    termination_queue_type             text,
    cluster_description                text,
    cluster_provider                   text,
    cluster_status                     text,
    cluster_source                     text,
    k8s_version                        text,
    cluster_group_name                 text,
    cluster_group_id                   text,
    creation_queue_url                 text,
    creation_queue_type                text,
    gpus                               FROZEN<SET<gpu>>,
    authorized_nca_ids                 set<text>,
    capabilities                       set<text>,
    attributes                         set<text>,
    gpus_v2                            set<FROZEN<gpusv2>>,
    gpus_v3                            set<FROZEN<gpusv3>>,
    gpus_v4                            set<FROZEN<gpusv4>>,
    gpus_v5                            set<FROZEN<gpusv5>>,
    nvca_version                       text,
    ssa_client_id                      text,
    region                             text,
    nvca_last_connected                timestamp,
    creation_queues                    map<text, FROZEN<creation_queue>>,
    cluster_creation_queues            map<text, FROZEN<creation_queue>>,
    cluster_creation_queues_for_tasks  map<text, FROZEN<creation_queue>>,
    request_dump                       text,
    custom_attributes                  set<text>,
    allow_cluster_targeting            boolean,
    allow_task_cluster_creation_queues boolean,
    cluster_key_id                     text,
    PRIMARY KEY ((nca_id), cluster_name)
) WITH CLUSTERING ORDER BY (cluster_name ASC);

-- Cluster groups accessible to authorized (non-owner) accounts.
CREATE TABLE IF NOT EXISTS sis_api.cluster_groups_by_authorized_accounts (
    nca_id_key          text,
    cluster_group_name  text,
    cluster_group_id    text,
    nca_id              text,
    creation_queue_url  text,
    creation_queue_type text,
    gpus                FROZEN<SET<gpu>>,
    authorized_nca_ids  set<text>,
    PRIMARY KEY ((nca_id_key), cluster_group_name, cluster_group_id)
) WITH CLUSTERING ORDER BY (cluster_group_name ASC, cluster_group_id ASC);

-- Clusters accessible to authorized (non-owner) accounts.
CREATE TABLE IF NOT EXISTS sis_api.clusters_by_authorized_accounts (
    nca_id_key                         text,
    cluster_name                       text,
    cluster_id                         text,
    cluster_group_name                 text,
    cluster_group_id                   text,
    nca_id                             text,
    creation_queues                    map<text, FROZEN<creation_queue>>,
    cluster_creation_queues            map<text, FROZEN<creation_queue>>,
    cluster_creation_queues_for_tasks  map<text, FROZEN<creation_queue>>,
    gpus_v2                            set<FROZEN<gpusv2>>,
    gpus_v3                            set<FROZEN<gpusv3>>,
    gpus_v4                            set<FROZEN<gpusv4>>,
    gpus_v5                            set<FROZEN<gpusv5>>,
    nvca_last_connected                timestamp,
    authorized_nca_ids                 set<text>,
    cluster_key_id                     text,
    PRIMARY KEY ((nca_id_key), cluster_id)
) WITH CLUSTERING ORDER BY (cluster_id ASC);

-- Cluster groups owned by an account.
CREATE TABLE IF NOT EXISTS sis_api.cluster_groups_by_account (
    cluster_group_name  text,
    cluster_group_id    text,
    nca_id              text,
    creation_queue_url  text,
    creation_queue_type text,
    gpus                FROZEN<SET<gpu>>,
    authorized_nca_ids  set<text>,
    PRIMARY KEY ((nca_id), cluster_group_name)
) WITH CLUSTERING ORDER BY (cluster_group_name ASC);

-- SQS batch message tracking per request.
CREATE TABLE IF NOT EXISTS sis_api.sqs_message_by_request_and_batch_id (
    request_id            text,
    zone                  text,
    status                text,
    message_batch_id      text,
    acknowledged_instances int,
    creation_time         timestamp,
    cloud_provider        text,
    reservation_id        uuid,
    capacity_type         text,
    PRIMARY KEY (request_id, message_batch_id)
) WITH CLUSTERING ORDER BY (message_batch_id ASC);

CREATE CUSTOM INDEX IF NOT EXISTS idx_sqs_message_by_reservation_id
    ON sis_api.sqs_message_by_request_and_batch_id (reservation_id) USING 'StorageAttachedIndex';

-- Function-to-billing account mapping.
CREATE TABLE IF NOT EXISTS sis_api.function_billing_mapping (
    function_id         uuid,
    function_version_id uuid,
    owner_nca_id        text,
    billing_nca_id      text,
    PRIMARY KEY ((function_id, function_version_id))
);
