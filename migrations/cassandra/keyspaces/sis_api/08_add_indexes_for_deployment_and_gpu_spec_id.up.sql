-- Indexes for deployment_id and gpu_specification_id
CREATE CUSTOM INDEX IF NOT EXISTS idx_instances_by_deployment_id ON sis_api.instances (deployment_id) USING 'StorageAttachedIndex';
CREATE CUSTOM INDEX IF NOT EXISTS idx_instances_by_gpu_specification_id ON sis_api.instances (gpu_specification_id) USING 'StorageAttachedIndex';
