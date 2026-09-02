# NVCF Cassandra Migrations

Container image and Cassandra DDL migrations used by NVCF deployments to bootstrap and evolve their Cassandra database schemas. The container uses the [`golang-migrate`](https://github.com/golang-migrate/migrate) driver to apply per-keyspace migrations in order.

## Overview

This repository ships:

- A multi-arch container image definition (`Dockerfile`)
- The migration entrypoint (`execute_sqls.sh`)
- Cassandra DDL migrations under `keyspaces/<service>/*.up.sql`, one keyspace per service

## Migration driver

The container builds [`golang-migrate`](https://github.com/golang-migrate/migrate) v4.19.1 from its checksum-verified release source. The build enables only the Cassandra database driver. This keeps unrelated database and cloud-provider clients out of the runtime binary.

`execute_sqls.sh` uses the standard `golang-migrate` Cassandra driver query parameters: `x-multi-statement`, `x-migrations-table`, `username`, and `password`.

## Prerequisites

- A reachable Cassandra cluster (the container connects via `cqlsh` and the migrate driver)
- Docker or another OCI-compatible builder
- Network access to download the pinned build inputs

## Building the container

The `Dockerfile` uses the official `cassandra:5.0.9` base image and builds both supported architectures from the same pinned sources.

```bash
docker build \
  --build-arg TARGETARCH=amd64 \
  -t <your-registry>/<your-org>/nvcf-cassandra-migrations:<version> .
```

## Running the migrations

The container reads the following environment variables:

| Variable | Purpose |
|---|---|
| `CASSANDRA_HOSTS` | Cassandra contact host (single hostname or IP) |
| `CASSANDRA_USER` | Cassandra superuser used to apply DDL |
| `CASSANDRA_PASSWORD` | Cassandra superuser password |
| `SERVICE_ROLE_PASSWORD` | Substituted into the per-keyspace login role passwords (`02_init_roles.up.sql`) |
| `REPLICA_COUNT` | Replication factor for service keyspaces (defaults to `3`) |

For each keyspace under `keyspaces/`, the container:

1. Pre-processes any `*.sql` and `*.cql` files via `envsubst` so that `${SERVICE_ROLE_PASSWORD}` and `${REPLICA_COUNT}` are substituted (other environment variables are intentionally not substituted).
2. Runs `migrate up` with the `cassandra://` driver, using the keyspace name as the migrations table name.

A failure in any keyspace's migration set stops the run.

### Recovering from a failed run

If a migration fails partway through, `golang-migrate` marks the row in `schema_migrations` as `dirty=true`. The next `migrate up` refuses to proceed and exits with:

```
Dirty database version <N>. Fix and force version.
```

Recovery is a three-step procedure: diagnose, reconcile, then clear the flag and re-run.

#### 1. Diagnose

The version number `<N>` in the error matches the numeric prefix of a file under `keyspaces/<service>/`. Inspect that file to see what statements were attempted, and confirm the current schema state:

```bash
cqlsh -u "$CASSANDRA_USER" -p "$CASSANDRA_PASSWORD" "$CASSANDRA_HOSTS" \
  -e "SELECT version, dirty FROM schema_migrations.<service>;"
```

#### 2. Reconcile partial state

The migration may have applied some statements before the failure. Decide whether to roll the schema back to `<N-1>` or roll it forward to `<N>`:

- Re-apply migration `<N>` from `<N-1>` (recommended when the migration is fully idempotent). Most bundled migrations use `CREATE TABLE IF NOT EXISTS`, `CREATE TYPE IF NOT EXISTS`, and similar idempotent forms; if the failing file is fully idempotent, fix the root cause of the failure (network, capacity, malformed CQL) and re-running migration `<N>` will be safe. Plan to `force <N-1>` in step 3.
- Roll back manually to `<N-1>`. If the migration is not idempotent, drop any tables / types / columns the partial run created so the schema matches the post-`<N-1>` state, then `force <N-1>` in step 3.
- Roll forward manually to `<N>`. Run the remaining CQL statements from `<N>` by hand so the schema matches the post-`<N>` state, then `force <N>` in step 3.

#### 3. Clear the dirty flag and re-run

Once the schema is in a state that matches either `<N-1>` or `<N>`, clear the dirty flag:

```bash
migrate -path keyspaces/<service> \
  -database "cassandra://${CASSANDRA_HOSTS}:9042/schema_migrations?x-migrations-table=<service>&username=...&password=..." \
  force <N-1>     # or <N>, matching the reconciled schema state
```

Then re-apply the migration Job. `migrate up` will pick up from the version recorded by `force` and proceed.

### Running as a Kubernetes Job

A minimal Job definition:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: cassandra-migrations
spec:
  template:
    spec:
      restartPolicy: OnFailure
      containers:
        - name: migrations
          image: <your-registry>/<your-org>/nvcf-cassandra-migrations:<version>
          env:
            - name: CASSANDRA_HOSTS
              value: "<your cassandra host>"
            - name: CASSANDRA_USER
              valueFrom:
                secretKeyRef: { name: cassandra-credentials, key: username }
            - name: CASSANDRA_PASSWORD
              valueFrom:
                secretKeyRef: { name: cassandra-credentials, key: password }
            - name: SERVICE_ROLE_PASSWORD
              valueFrom:
                secretKeyRef: { name: cassandra-credentials, key: service-role-password }
```

## Authoring migrations

Place new files under the appropriate `keyspaces/<service>/` directory using the naming convention:

```text
NN_description.up.sql
```

For a brand-new keyspace, the conventional sequence is:

1. `01_init_keyspace.up.sql` - creates the keyspace with `NetworkTopologyStrategy` and substitutes `${REPLICA_COUNT}` before migration.
2. `02_init_roles.up.sql` - creates the application role and grants, with the login password supplied by `${SERVICE_ROLE_PASSWORD}`.
3. `03_init_tables.up.sql` - the canonical schema (UDTs, tables, indexes) for the keyspace.

Subsequent files (`04_*` and later) are incremental migrations applied as the schema evolves. Most are DDL; `ess_api/04_*` is a data seed.

The `03_init_tables.up.sql` follows a clean-slate model: it is updated in place when the canonical schema changes rather than accumulating `ALTER TABLE` history. Existing clusters apply only the deltas that postdate their last applied migration.
