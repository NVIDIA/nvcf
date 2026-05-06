-- Create the OIDC/PSAT cluster-identity tables:
--
--   cluster_oidc_by_cluster_id           (auth-path read, register/rotate write)
--   cluster_oidc_by_jwks_fingerprint     (O(1) fingerprint-uniqueness index, closes NVCF-9990)
--
-- Only cluster_by_cluster_id was ever read on the OIDC auth path, so
-- splitting the OIDC payload into its own tables keeps the wide cluster
-- rows clean and replaces the O(n) getAllClusters() scan at register
-- and rotation with an O(1) denorm lookup.

CREATE TABLE IF NOT EXISTS sis_api.cluster_oidc_by_cluster_id (
    cluster_id text,
    jwks text,
    oidc_issuer text,
    jwks_fingerprint text,
    updated_at timestamp,
    PRIMARY KEY (cluster_id)
);

CREATE TABLE IF NOT EXISTS sis_api.cluster_oidc_by_jwks_fingerprint (
    jwks_fingerprint text,
    cluster_id text,
    registered_at timestamp,
    PRIMARY KEY (jwks_fingerprint)
);
