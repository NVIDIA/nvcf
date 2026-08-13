# Encrypted Secret Store

Encrypted Secret Store (ESS) provides a reactive REST API for storing secrets
with tenant-scoped encryption and authorization. Its API design follows the
Vault KV v2 model to support workload secret injection.

The service lives under `src/control-plane-services/encrypted-secret-store/`.

## Modules

| Module | Purpose |
|---|---|
| `ess-encryption` | Namespace encryption key management and cryptography |
| `ess-core` | Secret storage, authorization, and REST API logic |
| `ess-service` | Spring Boot executable application |

ESS is a Bazel-only component in the root `nvcf` module. See [BAZEL.md](BAZEL.md)
for build, test, NOTICE, Docker, and local startup commands.

Unit tests run without external services. Integration tests require a running
Docker daemon and use Testcontainers to start an ephemeral Cassandra 5 instance.

## Run locally

Local runs need a full JDK 25 with `JAVA_HOME` set, Docker running, and Bazel
through Bazelisk. Helper scripts live in
[local_env/scripts](local_env/scripts).

Fastest path, from `local_env/scripts`:

```bash
cd src/control-plane-services/encrypted-secret-store/local_env/scripts
./start-local.sh
```

`start-local.sh` starts Cassandra, waits until the `ess` keyspace is created,
then builds and runs the service in the foreground. Ctrl-C stops the service;
Cassandra keeps running so you can restart quickly.

To run the steps separately:

```bash
./start-cassandra.sh          # start Cassandra and apply the schema
./start-ess.sh                # build the app jar and run it
./stop-cassandra.sh           # stop Cassandra, keep data
./stop-cassandra.sh --purge   # stop Cassandra and delete the data volume
```

The service listens on port 8085 by default and reads local values from
[local_env/secrets/secrets.json](local_env/secrets/secrets.json). Override
behavior with environment variables:

```bash
SERVER_PORT=8090 ./start-ess.sh              # change the HTTP port
ESS_APP_JAR=/path/to/app.jar ./start-ess.sh  # run a prebuilt jar, skip building
```

Without the scripts, build and run the jar directly:

```bash
bazel build //src/control-plane-services/encrypted-secret-store/ess-service:app

cd src/control-plane-services/encrypted-secret-store
SPRING_PROFILES_ACTIVE=local java -Dserver.port=8085 \
  -jar ../../../bazel-bin/src/control-plane-services/encrypted-secret-store/ess-service/app.jar \
  --nv-boot.reloadable-properties.file=file:"$(pwd)/local_env/secrets/secrets.json"
```

Use an absolute `file:` path for the secrets, or run from this directory, so
the reloadable-properties source resolves regardless of the working directory.

### NCP mode

To exercise NCP flows, seed the `nvcf` namespace and start the service in the
`ncp-local` profile. Uncomment the `ncp.cql` line in
[local_env/cassandra/init.sh](local_env/cassandra/init.sh) before starting
Cassandra so the schema step loads the namespace:

```bash
cqlsh cassandra -f /cassandra_cql/ncp.cql
```

Recreate Cassandra so the seed applies, then run ESS with the NCP profile:

```bash
cd src/control-plane-services/encrypted-secret-store/local_env/scripts
./stop-cassandra.sh --purge   # drop the old data so the seed re-applies
./start-cassandra.sh
SPRING_PROFILE=ncp-local ./start-ess.sh
```

`ncp.cql` inserts into the `ess.namespaces` table, so the `ess` keyspace must
already exist (the schema step creates it). Purge and recreate only when you
need a clean seed.

See [BAZEL.md](BAZEL.md) for the full build, test, NOTICE, and Docker workflow.

## License

This component is licensed under the
[Apache License 2.0](../../../LICENSE). See [NOTICE](NOTICE) for its
third-party dependency attribution and
[CONTRIBUTING.md](../../../CONTRIBUTING.md) for contribution guidance.
