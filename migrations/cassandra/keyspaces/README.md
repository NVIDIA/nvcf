# Keyspace Migrations

## Overview

Each keyspace directory contains the Cassandra DDL migrations for a single keyspace.
Migrations are applied in filename order and follow the naming convention:

```text
NN_description.up.sql
```

The schemas in this directory follow a clean-slate model. There are no delta
`ALTER TABLE` migrations. Each `03_init_tables.up.sql` represents the complete,
canonical schema for its keyspace at the pinned upstream service version. When the
upstream schema changes, `03_init_tables.up.sql` is updated in place and the
pinned version reference is bumped accordingly.

---

## Schema Sources

| Keyspace          | Local Schema                                                              | Pinned Version | Commit     |
|-------------------|---------------------------------------------------------------------------|----------------|------------|
| `api_keys_api`    | [api_keys_api/03_init_tables.up.sql](api_keys_api/03_init_tables.up.sql)       | `main`         | `01eae99b` |
| `ess_api`         | [ess_api/03_init_tables.up.sql](ess_api/03_init_tables.up.sql)                 | `v0.48.26`     | `200fd74d` |
| `event_ledger`    | [event_ledger/03_init_tables.up.sql](event_ledger/03_init_tables.up.sql)       | `0.10.0`       | `adc2ff44` |
| `nvcf_autoscaler` | [nvcf_autoscaler/03_init_tables.up.sql](nvcf_autoscaler/03_init_tables.up.sql) | `v1.15.0`      | `bff903c`  |
| `nvcf_api`        | [nvcf_api/03_init_tables.up.sql](nvcf_api/03_init_tables.up.sql)               | `v1.5.1`       | `7a422ff1` |
| `nvct_api`        | [nvct_api/03_init_tables.up.sql](nvct_api/03_init_tables.up.sql)               | `v1.5.2`       | `a0247478` |
| `sis_api`         | [sis_api/03_init_tables.up.sql](sis_api/03_init_tables.up.sql)                 | `v1.531.2`     | `8a492a2e` |

> Note: The upstream source of truth for `sis_api` is under active clarification. The schema
> was sourced from `nvcf/nvcf-spot/spot@v1.517.0`. A competing source
> (`kaizen-data/helenus/schemas/gfn-core/spot`) was identified during analysis but
> has not been confirmed as authoritative. Review before the next schema update.

---

## Schema Files Per Keyspace

| File                      | Purpose                                                                                                                                                                                                            |
|---------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `01_init_keyspace.up.sql` | Creates the keyspace with `NetworkTopologyStrategy` replication. Uses `${REPLICA_COUNT}`, which the entrypoint substitutes before migration.                                                                    |
| `02_init_roles.up.sql`    | Creates the application role, grants privileges, and sets the service login password via `${SERVICE_ROLE_PASSWORD}`.                                                                                               |
| `03_init_tables.up.sql`   | Complete canonical schema with all UDTs, tables, and indexes at the pinned upstream version.                                                                                                                       |
| `04_*`, `05_*`, `06_*`    | Incremental deltas for rolling upgrades. These add tables/columns that are not in `03_init_tables.up.sql` at the version that was applied on existing clusters. `ess_api/04_*` is a data seed (deployment-specific values). `sis_api/04_*`-`06_*` and `nvcf_api/04_*`-`05_*` are DDL deltas. |

---

## Conventions and Rules

### Clean-Slate Model

This repo does not use incremental `ALTER TABLE` migrations for fresh
installations. The `03_init_tables.up.sql` file always reflects the full desired
schema. This avoids the complexity of replaying a long chain of deltas on new
clusters.

### Upstream vs. Our Values

We are consumers/specializations of upstream services. When updating schemas from
upstream:

- DDL (table/type definitions): follow the upstream source of truth exactly.
- Data seeds and configuration values: use our values (service endpoints,
  client IDs, etc.), not the upstream's local dev defaults. The upstream's seed
  files (e.g. `ncp.cql` in ess-api-service) are for their own test environments
  and are not authoritative for our deployment.

### Updating a Schema

1. Identify the target upstream service version/tag.
2. Fetch the canonical schema file from the upstream repo at that tag via the
   repository API:

   ```bash
   curl --header "PRIVATE-TOKEN: $GITLAB_TOKEN" \
     "$SCHEMA_RAW_URL" \
     -o /tmp/upstream_schema.cql
   ```

3. Diff against the current `03_init_tables.up.sql`. Identify:
   - Net-new tables or columns
   - Dropped tables or columns
   - Any data/config values that must use our deployment's values
4. Update `03_init_tables.up.sql` in place.
5. Update the Schema Sources table in this README with the new version and
   commit SHA.
