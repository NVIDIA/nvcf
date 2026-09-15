-- Add tenant registration persistence required by SIS GDN registration flow.
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
