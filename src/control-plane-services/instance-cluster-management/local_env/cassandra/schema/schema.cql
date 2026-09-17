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
DROP KEYSPACE IF EXISTS test;

CREATE KEYSPACE IF NOT EXISTS test WITH REPLICATION = {'class' : 'SimpleStrategy', 'replication_factor' : 1 };

CREATE TABLE IF NOT exists test.requests (
request_id text,
create_timeuuid timeuuid,
customer text,
action text,
state text,
status_code text,
status_message text,
status_update_time timestamp,
request text,
resource_provider text,
check_batchwise_info boolean,
clusters set<text>,
regions set<text>,
attributes set<text>,
custom_attributes set<text>,
instance_count int,
task_id uuid,
max_queued_duration text,
account_name text,
function_id uuid,
function_version_id uuid,
deployment_id uuid,
gpu_specification_id uuid,
nca_id text,
reservation_id uuid,
gpu_count_per_instance int,
PRIMARY KEY (request_id)
);

CREATE CUSTOM INDEX IF NOT EXISTS idx_requests_by_reservation_id
   ON test.requests (reservation_id) USING 'StorageAttachedIndex';

CREATE CUSTOM INDEX IF NOT EXISTS idx_requests_by_deployment_id
   ON test.requests (deployment_id) USING 'StorageAttachedIndex';

CREATE CUSTOM INDEX IF NOT EXISTS idx_requests_by_nca_id
   ON test.requests (nca_id) USING 'StorageAttachedIndex';


CREATE TABLE IF NOT exists test.requests_by_day (
truncated_ts_by_day timestamp,
request_id text,
create_timeuuid timeuuid,
marked_as_deleted boolean,
PRIMARY KEY (truncated_ts_by_day, request_id)
);


CREATE TABLE IF NOT exists test.instances(
instance_id text,
request_id text,
create_timeuuid timeuuid,
customer text,
image_id text,
request_state text,
instance_update_time timestamp,
zone text,
instance_state_code int,
instance_state_name text,
request_status_code text,
request_status_message text,
request_status_update_time timestamp,
resource_provider text,
error_log text,
error_source text,
nca_id text,
instance_type text,
backend text,
gpu text,
instance_ips set<text>,
region text,
attributes set<text>,
custom_attributes set<text>,
request_raw_data text,
reservation_id uuid,
gpu_count_per_instance int,
cloud_provider text,
capacity_type text,
instance_expiration_time timestamp,
backup_to_primary_migration_scheduled boolean,
deployment_id uuid,
gpu_specification_id uuid,
PRIMARY KEY (instance_id)
);

CREATE CUSTOM INDEX IF NOT EXISTS idx_instances_by_reservation_id
    ON test.instances (reservation_id) USING 'StorageAttachedIndex';

CREATE CUSTOM INDEX IF NOT EXISTS idx_instances_by_request_id
    ON test.instances (request_id) USING 'StorageAttachedIndex';

CREATE CUSTOM INDEX IF NOT EXISTS idx_instances_by_nca_id
    ON test.instances (nca_id) USING 'StorageAttachedIndex';

CREATE CUSTOM INDEX IF NOT EXISTS idx_instances_by_zone
    ON test.instances (zone) USING 'StorageAttachedIndex';

CREATE CUSTOM INDEX IF NOT EXISTS idx_instances_by_deployment_id
    ON test.instances (deployment_id) USING 'StorageAttachedIndex';

CREATE CUSTOM INDEX IF NOT EXISTS idx_instances_by_gpu_specification_id
    ON test.instances (gpu_specification_id) USING 'StorageAttachedIndex';


CREATE TABLE IF NOT exists test.instances_by_day (
truncated_ts_by_day timestamp,
instance_id text,
request_id text,
create_timeuuid timeuuid,
marked_as_deleted boolean,
PRIMARY KEY (truncated_ts_by_day, instance_id)
);


CREATE TABLE IF NOT exists test.instances_by_request (
request_id text,
instance_id text,
customer text,
truncated_ts timestamp,
PRIMARY KEY (request_id , instance_id)
);


CREATE TABLE IF NOT exists test.reservations (
reservation_id uuid,
nca_id text,
cluster_id text,
gpu_type text,
reserved_gpu_count int,
available_gpu_count double,
start_time timestamp,
end_time timestamp,
name text,
last_updated_time timestamp,
reservation_backup_disabled boolean,
PRIMARY KEY (reservation_id)
);

CREATE CUSTOM INDEX IF NOT EXISTS idx_reservations_by_nca_id
    ON test.reservations (nca_id) USING 'StorageAttachedIndex';

CREATE CUSTOM INDEX IF NOT EXISTS idx_reservations_by_cluster_id
    ON test.reservations (cluster_id) USING 'StorageAttachedIndex';


CREATE TABLE IF NOT EXISTS test.tenant_registration_by_registration_id (
    registration_id uuid PRIMARY KEY,
    deployment_id uuid,
    tenant_registration_data map<text,text>,
    tenant text,
    nca_id text,
    function_version_id uuid,
    function_id uuid,
    create_time timestamp
);

CREATE CUSTOM INDEX IF NOT EXISTS idx_tenant_registration_by_deployment_id
    ON test.tenant_registration_by_registration_id (deployment_id) USING 'StorageAttachedIndex';


CREATE TABLE IF NOT EXISTS test.lock
(
name      text,
lockUntil timestamp,
lockedAt  timestamp,
lockedBy  text,
PRIMARY KEY (name)
);

CREATE TABLE IF NOT EXISTS test.lock_by_ttl
(
lock_name      text,
locked_at      timestamp,
locked_by      text,
lock_ttl       int,
PRIMARY KEY (lock_name)
);


CREATE TYPE IF NOT EXISTS test.gpucapacity
(
    capacity int,
    allocated int,
    available int
);

CREATE TABLE IF NOT exists test.cloud_health (
cloud_provider text,
zone text,
status text,
cluster_upgrade_status text,
gpu_usage map<text, FROZEN<gpucapacity>>,
PRIMARY KEY ((cloud_provider, zone))
);

CREATE TYPE IF NOT EXISTS test.instance_type
(
    cpu_cores int,
    gpu text,
    system_memory text,
    gpu_memory text,
    name text
);

CREATE TYPE IF NOT EXISTS test.instancetype
(
    cpu_cores int,
    system_memory text,
    gpu_memory text,
    gpu_count int,
    name text,
    description text,
    value text,
    is_default boolean
);

CREATE TYPE IF NOT EXISTS test.instancetypev2
(
    cpu_cores int,
    system_memory text,
    gpu_memory text,
    gpu_count int,
    name text,
    description text,
    value text,
    is_default boolean,
    cpu_arch text,
    os text,
    driver_version text
);

CREATE TYPE IF NOT EXISTS test.instancetypev3
    (
        cpu_cores int,
        system_memory text,
        gpu_memory text,
        gpu_count int,
        name text,
        description text,
        value text,
        is_default boolean,
        cpu_arch text,
        os text,
        driver_version text,
        storage text
    );

CREATE TYPE IF NOT EXISTS test.gpu
(
    name text,
    instance_types FROZEN<set<instancetype>>
);

CREATE TYPE IF NOT EXISTS test.gpusv2
(
    name text,
    capacity int,
    instance_types set<FROZEN<instancetype>>
);

CREATE TYPE IF NOT EXISTS test.gpusv3
(
    name text,
    capacity int,
    instance_types set<FROZEN<instancetypev2>>
);

CREATE TYPE IF NOT EXISTS test.gpusv4
(
    name text,
    capacity int,
    instance_types set<FROZEN<instancetypev3>>
);


CREATE TYPE IF NOT EXISTS test.instancetypev5
(
    cpu_cores int,
    system_memory text,
    gpu_memory text,
    gpu_count int,
    name text,
    description text,
    value text,
    is_default boolean,
    cpu_arch text,
    os text,
    driver_version text,
    storage text,
    instance_type_usage text
);


CREATE TYPE IF NOT EXISTS test.gpusv5
(
    name text,
    capacity int,
    instance_types set<FROZEN<instancetypev5>>
);

CREATE TYPE IF NOT EXISTS test.creation_queue
(
    url text,
    queue_type text
);

CREATE TABLE IF NOT exists test.cluster_by_group_id_and_cluster_id (
cluster_name text,
cluster_id text,
nca_id text,
termination_queue_url text,
termination_queue_type text,
cluster_description text,
cluster_provider text,
cluster_status text,
cluster_source text,
k8s_version text,
cluster_group_name text,
cluster_group_id text,
creation_queue_url text,
creation_queue_type text,
gpus FROZEN<set<gpu>>,
authorized_nca_ids set<text>,
capabilities set<text>,
attributes set<text>,
gpus_v2 set<FROZEN<gpusv2>>,
gpus_v3 set<FROZEN<gpusv3>>,
gpus_v4 set<FROZEN<gpusv4>>,
gpus_v5 set<FROZEN<gpusv5>>,
nvca_version text,
auth_client_id text,
region text,
nvca_last_connected timestamp,
creation_queues map<text, FROZEN<creation_queue>>,
cluster_creation_queues map<text, FROZEN<creation_queue>>,
cluster_creation_queues_for_tasks map<text, FROZEN<creation_queue>>,
request_dump text,
custom_attributes set<text>,
allow_cluster_targeting boolean,
allow_task_cluster_creation_queues boolean,
cluster_key_id text,
PRIMARY KEY (cluster_group_id, cluster_id)
);

CREATE TABLE IF NOT exists test.cluster_by_cluster_id (
cluster_name text,
registration_time timestamp,
cluster_id text,
nca_id text,
termination_queue_url text,
termination_queue_type text,
cluster_description text,
cluster_provider text,
cluster_status text,
cluster_source text,
k8s_version text,
cluster_group_name text,
cluster_group_id text,
creation_queue_url text,
creation_queue_type text,
gpus FROZEN<set<gpu>>,
authorized_nca_ids set<text>,
capabilities set<text>,
attributes set<text>,
gpus_v2 set<FROZEN<gpusv2>>,
gpus_v3 set<FROZEN<gpusv3>>,
gpus_v4 set<FROZEN<gpusv4>>,
gpus_v5 set<FROZEN<gpusv5>>,
nvca_version text,
auth_client_id text,
region text,
nvca_last_connected timestamp,
creation_queues map<text, FROZEN<creation_queue>>,
cluster_creation_queues map<text, FROZEN<creation_queue>>,
cluster_creation_queues_for_tasks map<text, FROZEN<creation_queue>>,
request_dump text,
custom_attributes set<text>,
allow_cluster_targeting boolean,
allow_task_cluster_creation_queues boolean,
cluster_key_id text,
healthy_heartbeat_report_time timestamp,
jwks text,
oidc_issuer text,
jwks_fingerprint text,
PRIMARY KEY (cluster_id)
);

CREATE CUSTOM INDEX IF NOT EXISTS idx_cluster_by_authorized_nca_ids ON test.cluster_by_cluster_id (authorized_nca_ids) USING 'StorageAttachedIndex';
CREATE CUSTOM INDEX IF NOT EXISTS idx_cluster_by_nca_id ON test.cluster_by_cluster_id (nca_id) USING 'StorageAttachedIndex';
CREATE CUSTOM INDEX IF NOT EXISTS idx_cluster_by_cluster_group_id ON test.cluster_by_cluster_id (cluster_group_id) USING 'StorageAttachedIndex';

CREATE TABLE IF NOT exists test.cluster_configuration_by_cluster_id (
cluster_id text,
cluster_configurations map<text, text>,
cluster_configuration_files map<text, text>,
PRIMARY KEY (cluster_id)
);

CREATE TABLE IF NOT exists test.cluster_group_by_cluster_group_id (
cluster_group_name text,
cluster_group_id text,
creation_queue_url text,
creation_queue_type text,
gpus FROZEN<set<gpu>>,
nca_id text,
authorized_nca_ids set<text>,
PRIMARY KEY (cluster_group_id)
);

CREATE TABLE IF NOT exists test.cluster_health (
health_updated_ts timestamp,
cluster_id text,
PRIMARY KEY (cluster_id)
);

CREATE TABLE IF NOT exists test.clusters_by_account (
cluster_name text,
registration_time timestamp,
cluster_id text,
nca_id text,
termination_queue_url text,
termination_queue_type text,
cluster_description text,
cluster_provider text,
cluster_status text,
cluster_source text,
k8s_version text,
cluster_group_name text,
cluster_group_id text,
creation_queue_url text,
creation_queue_type text,
gpus FROZEN<set<gpu>>,
authorized_nca_ids set<text>,
capabilities set<text>,
attributes set<text>,
gpus_v2 set<FROZEN<gpusv2>>,
gpus_v3 set<FROZEN<gpusv3>>,
gpus_v4 set<FROZEN<gpusv4>>,
gpus_v5 set<FROZEN<gpusv5>>,
nvca_version text,
auth_client_id text,
region text,
nvca_last_connected timestamp,
creation_queues map<text, FROZEN<creation_queue>>,
cluster_creation_queues map<text, FROZEN<creation_queue>>,
cluster_creation_queues_for_tasks map<text, FROZEN<creation_queue>>,
request_dump text,
custom_attributes set<text>,
allow_cluster_targeting boolean,
allow_task_cluster_creation_queues boolean,
cluster_key_id text,
PRIMARY KEY ((nca_id), cluster_name)
);

CREATE TABLE IF NOT exists test.cluster_groups_by_authorized_accounts (
nca_id_key text,
cluster_group_name text,
cluster_group_id text,
nca_id text,
creation_queue_url text,
creation_queue_type text,
gpus FROZEN<set<gpu>>,
authorized_nca_ids set<text>,
PRIMARY KEY ((nca_id_key), cluster_group_name, cluster_group_id)
);

CREATE TABLE IF NOT exists test.clusters_by_authorized_accounts (
nca_id_key text,
cluster_name text,
cluster_id text,
cluster_group_name text,
cluster_group_id text,
nca_id text,
creation_queues map<text, FROZEN<creation_queue>>,
cluster_creation_queues map<text, FROZEN<creation_queue>>,
cluster_creation_queues_for_tasks map<text, FROZEN<creation_queue>>,
gpus_v2 set<FROZEN<gpusv2>>,
gpus_v3 set<FROZEN<gpusv3>>,
gpus_v4 set<FROZEN<gpusv4>>,
gpus_v5 set<FROZEN<gpusv5>>,
nvca_last_connected timestamp,
authorized_nca_ids set<text>,
cluster_key_id text,
PRIMARY KEY ((nca_id_key), cluster_id)
);

CREATE TABLE IF NOT exists test.cluster_groups_by_account (
cluster_group_name text,
cluster_group_id text,
nca_id text,
creation_queue_url text,
creation_queue_type text,
gpus FROZEN<set<gpu>>,
authorized_nca_ids set<text>,
PRIMARY KEY ((nca_id), cluster_group_name)
);

CREATE TABLE IF NOT exists test.sqs_message_by_request_and_batch_id (
request_id text,
zone text,
status text,
message_batch_id text,
acknowledged_instances int,
creation_time timestamp,
cloud_provider text,
reservation_id uuid,
capacity_type text,
PRIMARY KEY (request_id, message_batch_id)
);

CREATE CUSTOM INDEX IF NOT EXISTS idx_sqs_message_by_reservation_id
   ON test.sqs_message_by_request_and_batch_id (reservation_id) USING 'StorageAttachedIndex';

CREATE TABLE IF NOT exists test.function_billing_mapping (
function_id UUID,
function_version_id UUID,
owner_nca_id text,
billing_nca_id text,
PRIMARY KEY ((function_id, function_version_id))
);

CREATE TABLE IF NOT exists test.instances_by_zone(
nca_id text,
instance_type text,
backend text,
gpu text,
customer text,
truncated_ts timestamp,
request_id text,
instance_id text,
image_id text,
request_state text,
update_timestamp timestamp,
zone text,
instance_state_code int,
instance_state_name text,
resource_provider text,
error_log text,
error_source text,
request_status_code text,
request_status_message text,
request_status_update_time timestamp,
instance_ips FROZEN<set<text>>,
region text,
attributes set<text>,
custom_attributes set<text>,
request text,
PRIMARY KEY ((truncated_ts, zone), instance_id)
);
