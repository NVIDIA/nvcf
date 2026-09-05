-- Preserve compatibility with the latest published function-autoscaler image.
-- Remove these tables only after every supported autoscaler image stops using
-- them and the self-managed stack pins that compatible image.

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

CREATE TABLE IF NOT EXISTS nvcf_autoscaler.running_functions_without_invocations (
    function_id         UUID,
    function_version_id UUID,
    last_updated_at     TIMESTAMP,
    account_id          TEXT,
    PRIMARY KEY ((function_id, function_version_id))
) WITH default_time_to_live = 600
  AND compaction = {'class': 'UnifiedCompactionStrategy', 'scaling_parameters': 'T4', 'target_sstable_size': '50MiB', 'base_shard_count': '4', 'expired_sstable_check_frequency_seconds': '300'}
  AND read_repair = 'NONE';

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
