# Keyspace Migrations

## Overview

Each keyspace directory contains the Cassandra DDL migrations for a single keyspace.
Migrations are applied in filename order and follow the naming convention:

```text
NN_description.up.sql
```

The schemas in this directory follow a **clean-slate** model — there are no delta
`ALTER TABLE` migrations. Each `03_init_tables.up.sql` represents the complete,
canonical schema for its keyspace at the pinned upstream service version. When the
upstream schema changes, `03_init_tables.up.sql` is updated in place and the
pinned version reference is bumped accordingly.

---

## Schema Sources

| Keyspace       | Source Repository                                                                                                                                                                                           | Pinned Version | Commit     |
|----------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|----------------|------------|
| `api_keys_api` | [local_env/cassandra/schema.cql](https://github.com/NVIDIA/kaizen/auth/ncp-auth/nv-api-keys-service/-/blob/01eae99b48c2d1235ff4662055824c3037565bce/local_env/cassandra/schema.cql)                  | `main`         | `01eae99b` |
| `ess_api`      | [src/main/resources/models/schema.cql](https://github.com/NVIDIA/ngc/cloud/secrets/ess-api-service/-/blob/200fd74d7543c4aad5973676525cbe0ec4bcf62b/src/main/resources/models/schema.cql)             | `v0.48.26`     | `200fd74d` |
| `nvcf_api`     | [local_env/cassandra/schema/0001_initial_schema.cql](https://github.com/NVIDIA/nvcf/nvcf-service/-/blob/273d54325ec131b746fbeace764303113b82c0f7/local_env/cassandra/schema/0001_initial_schema.cql) | `v2.234.0`     | `273d5432` |
| `sis_api`      | [spot_local_env/cassandra/schema/schema.cql](https://github.com/NVIDIA/nvcf/nvcf-spot/spot/-/blob/8a492a2e3b38f9c3c4f2c2ee7ac680788954b49d/spot_local_env/cassandra/schema/schema.cql)               | `v1.531.2`     | `8a492a2e` |

> **Note — `sis_api`:** The upstream SoT is under active clarification. The schema
> was sourced from `nvcf/nvcf-spot/spot@v1.517.0`. A competing source
> (`kaizen-data/helenus/schemas/gfn-core/spot`) was identified during analysis but
> has not been confirmed as authoritative. Review before the next schema update.

---

## Schema Files Per Keyspace

| File                      | Purpose                                                                                                                                                                                                            |
|---------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `01_init_keyspace.up.sql` | Creates the keyspace with `NetworkTopologyStrategy` replication. Uses `{{ $replicaCount }}` template variable.                                                                                                     |
| `02_init_roles.up.sql`    | Creates the application role, grants privileges, and sets the service login password via `${SERVICE_ROLE_PASSWORD}`.                                                                                               |
| `03_init_tables.up.sql`   | Complete canonical schema — all UDTs, tables, and indexes at the pinned upstream version.                                                                                                                          |
| `04_*`, `05_*`            | Incremental deltas for rolling upgrades. These add tables/columns that are not in `03_init_tables.up.sql` at the version that was applied on existing clusters. `ess_api/04_*` is a data seed (deployment-specific values). `sis_api/04_*` and `nvcf_api/04_*`-`05_*` are DDL deltas. |

---

## Conventions and Rules

### Clean-Slate Model

This repo does **not** use incremental `ALTER TABLE` migrations for fresh
installations. The `03_init_tables.up.sql` file always reflects the full desired
schema. This avoids the complexity of replaying a long chain of deltas on new
clusters.

### Upstream vs. Our Values

We are consumers/specializations of upstream services. When updating schemas from
upstream:

- **DDL (table/type definitions)** — follow the upstream SoT exactly.
- **Data seeds and configuration values** — use **our** values (service endpoints,
  client IDs, etc.), not the upstream's local dev defaults. The upstream's seed
  files (e.g. `ncp.cql` in ess-api-service) are for their own test environments
  and are not authoritative for our deployment.

### Updating a Schema

1. Identify the target upstream service version/tag.
2. Fetch the canonical schema file from the upstream repo at that tag via the
   GitLab API:

   ```bash
   curl --header "PRIVATE-TOKEN: $GITLAB_TOKEN" \
     "https://github.com/NVIDIA/api/v4/projects/<encoded-path>/repository/files/<encoded-file-path>/raw?ref=<tag>" \
     -o /tmp/upstream_schema.cql
   ```

3. Diff against the current `03_init_tables.up.sql`. Identify:
   - Net-new tables or columns
   - Dropped tables or columns
   - Any data/config values that must use our deployment's values
4. Update `03_init_tables.up.sql` in place.
5. Update the **Schema Sources** table in this README with the new version and
   commit SHA.
