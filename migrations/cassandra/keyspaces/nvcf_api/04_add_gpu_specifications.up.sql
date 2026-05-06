-- Add gpu_specifications table for nvcf-service v2.232.0+.
-- Required for rolling upgrade to stack 0.5.0.
-- GpuSpecificationMigrationTask migrates data from functions_deployment_v2.gpu_specs
-- into this dedicated table on a 30-minute schedule.

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
