-- Add llm_config column to functions_v3, matching the pinned schema version in
-- keyspaces/README.md.
-- Function-level LLM configuration, stored as JSON. Distinct from the per-model
-- configuration embedded in model_specs.

ALTER TABLE nvcf_api.functions_v3 ADD IF NOT EXISTS llm_config TEXT;
