-- Add SIS cluster auth-client metadata columns.
--
-- Mirrors the upstream ICMS schema change that adds the cluster auth-client columns:
-- - migrations/20_alter_table_clusters_by_account.up.cql
-- - migrations/21_alter_table_cluster_by_group_id_and_cluster_id.up.cql
-- - migrations/22_alter_table_cluster_by_cluster_id.up.cql

ALTER TABLE sis_api.clusters_by_account ADD IF NOT EXISTS auth_client_id text;
ALTER TABLE sis_api.cluster_by_group_id_and_cluster_id ADD IF NOT EXISTS auth_client_id text;
ALTER TABLE sis_api.cluster_by_cluster_id ADD IF NOT EXISTS auth_client_id text;
