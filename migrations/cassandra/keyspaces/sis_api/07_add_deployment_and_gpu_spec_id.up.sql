-- Add deployment_id and gpu_specification_id UUID to instances table
ALTER TABLE sis_api.instances ADD IF NOT EXISTS (deployment_id uuid, gpu_specification_id uuid);
