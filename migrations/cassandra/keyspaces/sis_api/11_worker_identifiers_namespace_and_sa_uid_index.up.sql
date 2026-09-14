-- Worker identity anchoring for the fixed "nvcf-worker" ServiceAccount.
--
-- namespace: the instance namespace the worker SA and pods live in; matched against the
--            token's kubernetes.io.namespace claim.
-- sa_uid index: introspection resolves the instance from the token's
--            kubernetes.io.serviceaccount.uid within the caller cluster's partition.
--
-- Row lifetime is bounded by a per-write TTL (24h, refreshed on each accepted status
-- update) applied by the application; no table-level default TTL.

ALTER TABLE sis_api.worker_identifiers ADD namespace text;

CREATE CUSTOM INDEX IF NOT EXISTS idx_worker_identifiers_by_sa_uid
    ON sis_api.worker_identifiers (sa_uid) USING 'StorageAttachedIndex';
