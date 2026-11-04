-- Add OIDC/PSAT cluster-identity columns to the canonical cluster table.
--
-- The design intentionally keeps OIDC identity on cluster_by_cluster_id
-- instead of creating dedicated cluster_oidc_* tables.
-- Fresh installs get these columns from 03_init_tables.up.sql, while this migration
-- upgrades existing keyspaces created before the OIDC columns existed.

ALTER TABLE sis_api.cluster_by_cluster_id ADD IF NOT EXISTS jwks text;
ALTER TABLE sis_api.cluster_by_cluster_id ADD IF NOT EXISTS oidc_issuer text;
ALTER TABLE sis_api.cluster_by_cluster_id ADD IF NOT EXISTS jwks_fingerprint text;
