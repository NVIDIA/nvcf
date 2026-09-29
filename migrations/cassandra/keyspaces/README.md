# Keyspace Migrations

## Overview

Each keyspace directory contains the Cassandra DDL migrations for a single keyspace.
Migrations are applied in filename order and follow the naming convention:

```text
NN_description.up.sql
```

Each `03_init_tables.up.sql` is the baseline schema for its keyspace at the
pinned version in the Schema Sources table. Fresh installations apply that file,
then every later migration in filename order. Existing clusters apply only the
migrations newer than their recorded version.

Do not edit `03_init_tables.up.sql` when the schema changes. Add the next
numbered migration instead.

---

## Schema Sources

| Keyspace          | Local Schema                                                              | Pinned Version | Commit     |
|-------------------|---------------------------------------------------------------------------|----------------|------------|
| `api_keys_api`    | [api_keys_api/03_init_tables.up.sql](api_keys_api/03_init_tables.up.sql)       | `main`         | `01eae99b` |
| `ess_api`         | [ess_api/03_init_tables.up.sql](ess_api/03_init_tables.up.sql)                 | `v0.48.26`     | `200fd74d` |
| `event_ledger`    | [event_ledger/03_init_tables.up.sql](event_ledger/03_init_tables.up.sql)       | `0.10.0`       | `adc2ff44` |
| `nvcf_autoscaler` | [nvcf_autoscaler/03_init_tables.up.sql](nvcf_autoscaler/03_init_tables.up.sql) | `v1.15.0`      | `bff903c`  |
| `nvcf_api`        | [nvcf_api/03_init_tables.up.sql](nvcf_api/03_init_tables.up.sql)               | `v1.10.0`      | `fcaea0c1` |
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
| `03_init_tables.up.sql`   | Baseline schema (UDTs, tables, and indexes) at the pinned version. Later schema changes belong in a new migration file, not in this file.                                                                          |
| `04_*` and later          | Incremental deltas for rolling upgrades. These add tables/columns that are not in `03_init_tables.up.sql` at the version that was applied on existing clusters. `ess_api/04_*` is a data seed (deployment-specific values). The `api_keys_api`, `sis_api`, and `nvcf_api` deltas are DDL. |

---

## Conventions and Rules

### Baseline and later migrations

`03_init_tables.up.sql` is the baseline. Fresh installations run it, then apply
`04_*` and later in filename order. Existing clusters skip migrations they have
already applied.

Put each later schema change in a new migration. Leave `03_init_tables.up.sql`
unchanged so a cluster already at version 3 still receives the change.

DDL migrations create or alter tables, types, and indexes. The `api_keys_api`,
`sis_api`, and `nvcf_api` deltas are DDL. Deployment-specific data seeds, such
as `ess_api/04_*`, load values for this deployment and stay in their own files.

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

3. Diff against `03_init_tables.up.sql` and the migrations that follow it.
   Identify:
   - Net-new tables or columns
   - Dropped tables or columns
   - Data or config values that must use this deployment's values
4. Add the next `NN_description.up.sql`. Do not edit `03_init_tables.up.sql`
   in place.
   - DDL (tables, types, columns, indexes) goes in a DDL migration.
   - Deployment-specific data stays in a separate seed migration. Do not mix
     seed values into a DDL file.
5. Update the Schema Sources table in this README only when the baseline
   `03_init_tables.up.sql` pin changes. A later migration does not change that
   pin by itself.
