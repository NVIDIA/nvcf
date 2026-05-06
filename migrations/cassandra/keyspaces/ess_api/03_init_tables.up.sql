-- Canonical schema for ess_api keyspace.

-- ============================================================
-- User-Defined Types
-- ============================================================

-- Authorization entry for SSA or Notary clients.
CREATE TYPE IF NOT EXISTS ess_api.authorization (
    id       text, -- sub claim for SSA and Notary
    name     text, -- label, display name
    jwks_url text, -- derived from issuer for SSA and Notary
    issuer   text, -- required for agent auth
    type     text  -- NOTARY or SSA
);

-- Extensible metadata per entity type within a namespace.
CREATE TYPE IF NOT EXISTS ess_api.entity_type (
    name       text,
    deleted_at timestamp -- null == not deleted
);

-- ============================================================
-- Tables
-- ============================================================

-- Top-level namespace registry. Each namespace scopes all secrets and entities.
CREATE TABLE IF NOT EXISTS ess_api.namespaces (
    namespace                             text,
    ssa_authorizations                    map<text, frozen<authorization>>,
    notary_authorizations                 map<text, frozen<authorization>>,
    entity_types                          map<text, frozen<entity_type>>,
    created_at                            timestamp,
    updated_at                            timestamp,
    entity_hash_size                      int,
    previous_entity_hash_size             int,
    deleted_at                            timestamp,
    require_lwt_for_secret_version_writes boolean,
    PRIMARY KEY (namespace)
);

-- Entity registry within a namespace, hash-bucketed for even distribution.
CREATE TABLE IF NOT EXISTS ess_api.entities (
    namespace   text,
    hash_bucket int,
    entity_type text,
    entity_id   text,
    created_at  timestamp,
    PRIMARY KEY ((namespace, entity_type, hash_bucket), entity_id)
);

-- Directory-style path index for secrets owned by an entity.
CREATE TABLE IF NOT EXISTS ess_api.secret_paths_by_entity (
    namespace      text,
    entity         text,
    path           text,
    updated_at     timeuuid,
    entity_version timeuuid static,
    is_dir         boolean,
    PRIMARY KEY ((namespace, entity), path)
);

-- Versioned secret values, ordered newest-first.
CREATE TABLE IF NOT EXISTS ess_api.secret_versions_by_entity_and_path (
    namespace        text,
    entity           text,
    secret_path      text,
    version          timeuuid,
    current_version  timeuuid static,
    value            text,
    created_at       timestamp,
    encrypted_at     timestamp,
    encrypted_by_kid text,
    PRIMARY KEY ((namespace, entity, secret_path), version)
) WITH CLUSTERING ORDER BY (version DESC);

-- Current encryption key lookup by namespace + kid.
CREATE TABLE IF NOT EXISTS ess_api.encryption_keys_by_kid (
    namespace        text,
    kid              text,
    created_at       timeuuid,
    encrypted_at     timestamp,
    encrypted_by_kid text,
    encrypted_key    text,
    PRIMARY KEY ((namespace), kid)
);

-- Encryption key lookup by namespace + creation time (for rotation queries).
CREATE TABLE IF NOT EXISTS ess_api.encryption_keys_by_timestamp (
    namespace        text,
    kid              text,
    created_at       timeuuid,
    encrypted_at     timestamp,
    encrypted_by_kid text,
    encrypted_key    text,
    PRIMARY KEY ((namespace), created_at)
) WITH CLUSTERING ORDER BY (created_at DESC);

-- Full encryption key history by kid + encrypted_at (for audit/rotation).
CREATE TABLE IF NOT EXISTS ess_api.encryption_keys_by_kid_and_encrypted_at (
    namespace        text,
    kid              text,
    created_at       timeuuid,
    current_kid      text static,
    encrypted_at     timestamp,
    encrypted_by_kid text,
    encrypted_key    text,
    status           text,
    PRIMARY KEY ((namespace), kid, encrypted_at)
) WITH CLUSTERING ORDER BY (kid ASC, encrypted_at DESC);
