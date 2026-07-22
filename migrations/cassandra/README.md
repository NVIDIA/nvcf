# NVCF Cassandra Migrations

Container image and Cassandra DDL migrations used by NVCF deployments to bootstrap and evolve their Cassandra database schemas. The container uses the [`golang-migrate`](https://github.com/golang-migrate/migrate) driver to apply per-keyspace migrations in order.

## Overview

This repository ships:

- A multi-arch container image definition (`Dockerfile`)
- The migration entrypoint (`execute_sqls.sh`)
- Cassandra DDL migrations under `keyspaces/<service>/*.up.sql`, one keyspace per service

## Driver binaries

The container ENTRYPOINT runs the [`golang-migrate`](https://github.com/golang-migrate/migrate) CLI. A pre-built binary for each target architecture is required at build time, placed at:

- `files/migrate-amd64` (for `--platform linux/amd64`)
- `files/migrate-arm64` (for `--platform linux/arm64`)

Download the release matching your platform (v4.18.2 or compatible) from https://github.com/golang-migrate/migrate/releases, extract the `migrate` binary, rename it to `migrate-amd64` or `migrate-arm64`, place it in `files/`, and ensure it is executable. For example:

```bash
# amd64
curl -L https://github.com/golang-migrate/migrate/releases/download/v4.18.2/migrate.linux-amd64.tar.gz \
  | tar xz -C files/
mv files/migrate files/migrate-amd64
chmod +x files/migrate-amd64

# arm64 (run on / for an arm64 build)
curl -L https://github.com/golang-migrate/migrate/releases/download/v4.18.2/migrate.linux-arm64.tar.gz \
  | tar xz -C files/
mv files/migrate files/migrate-arm64
chmod +x files/migrate-arm64
```

`execute_sqls.sh` uses the standard `golang-migrate` cassandra driver query parameters: `x-multi-statement`, `x-migrations-table`, `username`, and `password`.

## Prerequisites

- A reachable Cassandra cluster (the container connects via `cqlsh` and the migrate driver)
- Docker or another OCI-compatible builder
- The driver binaries listed above, placed in `files/`

## Building the container

The `Dockerfile` defaults to the official `cassandra:5.0.8` base image. Override the FROM via your own build pipeline if you need a different base.

```bash
docker build \
  --build-arg TARGETARCH=amd64 \
  --build-arg KUBECTL_VERSION=1.31.0 \
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

For each keyspace under `keyspaces/`, the container:

1. Pre-processes any `*.up.sql` and `*.cql` files via `envsubst` so that `${SERVICE_ROLE_PASSWORD}` is substituted (other environment variables are intentionally not substituted).
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

- **Re-apply migration `<N>` from `<N-1>`** (recommended when the migration is fully idempotent). Most bundled migrations use `CREATE TABLE IF NOT EXISTS`, `CREATE TYPE IF NOT EXISTS`, and similar idempotent forms; if the failing file is fully idempotent, fix the root cause of the failure (network, capacity, malformed CQL) and re-running migration `<N>` will be safe. Plan to `force <N-1>` in step 3.
- **Roll back manually** to `<N-1>`. If the migration is not idempotent, drop any tables / types / columns the partial run created so the schema matches the post-`<N-1>` state, then `force <N-1>` in step 3.
- **Roll forward manually** to `<N>`. Run the remaining CQL statements from `<N>` by hand so the schema matches the post-`<N>` state, then `force <N>` in step 3.

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

1. `01_init_keyspace.up.sql` - creates the keyspace with `NetworkTopologyStrategy` (uses `{{ $replicaCount }}` template variable, substituted upstream).
2. `02_init_roles.up.sql` - creates the application role and grants, with the login password supplied by `${SERVICE_ROLE_PASSWORD}`.
3. `03_init_tables.up.sql` - the canonical schema (UDTs, tables, indexes) for the keyspace.

Subsequent files (`04_*`, `05_*`, `06_*`) are incremental DDL deltas applied as the schema evolves.

The `03_init_tables.up.sql` follows a clean-slate model: it is updated in place when the canonical schema changes rather than accumulating `ALTER TABLE` history. Existing clusters apply only the deltas that postdate their last applied migration.

## Notes

- The `golang-migrate` driver binary at `files/migrate-${TARGETARCH}` must be supplied locally before building. See "Driver binaries" above for the download steps.
