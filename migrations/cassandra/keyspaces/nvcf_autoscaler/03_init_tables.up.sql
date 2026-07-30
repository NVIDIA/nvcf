-- Canonical schema for nvcf_autoscaler keyspace.
-- Pinned to the upstream nvcf_autoscaler schema at migration version 9.
-- account_id (TEXT) consolidates upstream nca_id_string from migrations 02-09. nca_id UUID dropped (NCA IDs are not UUIDs and the column was unused).

-- ============================================================
-- Tables
-- ============================================================

-- Functions invoked recently. Rows expire via TTL to track a rolling window.
CREATE TABLE IF NOT EXISTS nvcf_autoscaler.recently_invoked_functions (
    function_id         UUID,
    function_version_id UUID,
    last_updated_at     TIMESTAMP,
    account_id          TEXT,
    PRIMARY KEY ((function_id, function_version_id))
) WITH default_time_to_live = 600
  AND compaction = {'class': 'UnifiedCompactionStrategy', 'scaling_parameters': 'T4', 'target_sstable_size': '50MiB', 'base_shard_count': '4', 'expired_sstable_check_frequency_seconds': '300'}
  AND read_repair = 'NONE';

-- Historical scaling decisions for recently invoked functions, clustered by time.
CREATE TABLE IF NOT EXISTS nvcf_autoscaler.recently_invoked_functions_history (
    function_id                           UUID,
    function_version_id                   UUID,
    last_updated_at                       TIMESTAMP,
    account_id                            TEXT STATIC,
    num_workers                           INT STATIC,
    last_predicted_desired_instance_count INT,
    last_predicted_error_code             TEXT,
    PRIMARY KEY ((function_id, function_version_id), last_updated_at)
) WITH CLUSTERING ORDER BY (last_updated_at DESC)
  AND default_time_to_live = 172800
  AND compaction = {'class': 'UnifiedCompactionStrategy', 'scaling_parameters': 'T4', 'target_sstable_size': '50MiB', 'base_shard_count': '4', 'expired_sstable_check_frequency_seconds': '300'}
  AND read_repair = 'NONE';

-- Functions with running workers but no recent invocations.
CREATE TABLE IF NOT EXISTS nvcf_autoscaler.running_functions_without_invocations (
    function_id         UUID,
    function_version_id UUID,
    last_updated_at     TIMESTAMP,
    account_id          TEXT,
    PRIMARY KEY ((function_id, function_version_id))
) WITH default_time_to_live = 600
  AND compaction = {'class': 'UnifiedCompactionStrategy', 'scaling_parameters': 'T4', 'target_sstable_size': '50MiB', 'base_shard_count': '4', 'expired_sstable_check_frequency_seconds': '300'}
  AND read_repair = 'NONE';

-- Historical scaling decisions for running functions without invocations, clustered by time.
CREATE TABLE IF NOT EXISTS nvcf_autoscaler.running_functions_without_invocations_history (
    function_id                           UUID,
    function_version_id                   UUID,
    last_updated_at                       TIMESTAMP,
    account_id                            TEXT STATIC,
    num_workers                           INT STATIC,
    last_predicted_desired_instance_count INT,
    last_predicted_error_code             TEXT,
    PRIMARY KEY ((function_id, function_version_id), last_updated_at)
) WITH CLUSTERING ORDER BY (last_updated_at DESC)
  AND default_time_to_live = 172800
  AND compaction = {'class': 'UnifiedCompactionStrategy', 'scaling_parameters': 'T4', 'target_sstable_size': '50MiB', 'base_shard_count': '4', 'expired_sstable_check_frequency_seconds': '300'}
  AND read_repair = 'NONE';

-- Distributed lock table for autoscaler leader coordination.
CREATE TABLE IF NOT EXISTS nvcf_autoscaler.locks (
    lock_name   TEXT PRIMARY KEY,
    node_id     TEXT,
    acquired_at TIMESTAMP
) WITH default_time_to_live = 3600
  AND compaction = {'class': 'UnifiedCompactionStrategy', 'scaling_parameters': 'T4', 'target_sstable_size': '50MiB', 'base_shard_count': '4', 'expired_sstable_check_frequency_seconds': '300'}
  AND read_repair = 'NONE';

-- Liveness registry of autoscaler nodes. Rows expire quickly to reflect health.
CREATE TABLE IF NOT EXISTS nvcf_autoscaler.healthy_nodes (
    node_id         TEXT PRIMARY KEY,
    last_updated_at TIMESTAMP
) WITH default_time_to_live = 180
  AND compaction = {'class': 'UnifiedCompactionStrategy', 'scaling_parameters': 'T4', 'target_sstable_size': '50MiB', 'base_shard_count': '4', 'expired_sstable_check_frequency_seconds': '300'}
  AND read_repair = 'NONE';
