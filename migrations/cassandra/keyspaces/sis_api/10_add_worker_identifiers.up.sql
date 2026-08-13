-- Add worker-identifier storage for the delegated worker token feature.
--
-- worker_identifier (UDT): a single pod-identity or SPIFFE-identity binding.
--   name = pod name (PSAT flow) or full SPIFFE ID (SPIFFE flow)
--   uid  = pod UID (PSAT flow) or worker UUID (SPIFFE flow)
--
-- worker_identifiers (table): per-instance authoritative identity set.
--   Partition key: cluster_id — all identifiers for a cluster co-located.
--   Clustering key: instance_id — O(1) lookup by instance.
--   Upsert (full replace) semantics: each write from NVCA overwrites the list.
--   Deleted on terminal instance state by ICMS InstanceUpdateService.

CREATE TYPE IF NOT EXISTS sis_api.worker_identifier (
    name text,
    uid  text
);

CREATE TABLE IF NOT EXISTS sis_api.worker_identifiers (
    cluster_id  text,
    instance_id text,
    sub         text,
    sa_uid      text,
    identifiers list<frozen<sis_api.worker_identifier>>,
    PRIMARY KEY (cluster_id, instance_id)
);
