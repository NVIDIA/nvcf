-- Indexes used to serve cluster reads from cluster_by_cluster_id.
CREATE CUSTOM INDEX IF NOT EXISTS idx_cluster_by_authorized_nca_ids
    ON sis_api.cluster_by_cluster_id (authorized_nca_ids) USING 'StorageAttachedIndex';
CREATE CUSTOM INDEX IF NOT EXISTS idx_cluster_by_nca_id
    ON sis_api.cluster_by_cluster_id (nca_id) USING 'StorageAttachedIndex';
CREATE CUSTOM INDEX IF NOT EXISTS idx_cluster_by_cluster_group_id
    ON sis_api.cluster_by_cluster_id (cluster_group_id) USING 'StorageAttachedIndex';
