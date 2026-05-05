-- Add model_specs column to functions_v3 for nvcf-service v2.234.0+.
-- Required for rolling upgrade to stack 0.5.0.
-- MigrateModelSpecsTask migrates data from the legacy models UDT column
-- into this MAP column on a 30-minute schedule.

ALTER TABLE nvcf_api.functions_v3 ADD IF NOT EXISTS model_specs MAP<TEXT, TEXT>;
