CREATE CUSTOM INDEX IF NOT EXISTS idx_requests_by_creation_day
    ON sis_api.requests (creation_bucket) USING 'StorageAttachedIndex';
CREATE CUSTOM INDEX IF NOT EXISTS idx_instances_by_creation_day
    ON sis_api.instances (creation_bucket) USING 'StorageAttachedIndex';
