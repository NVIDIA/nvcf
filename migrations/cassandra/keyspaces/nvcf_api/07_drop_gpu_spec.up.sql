-- Clean up unused columns and type

ALTER TABLE IF EXISTS nvcf_api.functions_deployment_v2 DROP IF EXISTS gpu_specs;
ALTER TABLE IF EXISTS nvcf_api.functions_deployment_v2 DROP IF EXISTS autoscaling_config;
ALTER TABLE IF EXISTS nvcf_api.functions_v3 DROP IF EXISTS models;

DROP TYPE IF EXISTS nvcf_api.gpu_spec_udt;
DROP TYPE IF EXISTS nvcf_api.model_udt;
