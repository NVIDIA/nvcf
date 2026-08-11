# instance-cluster-management (ICMS)

NVIDIA Instance and Cluster Management Service (ICMS) exposes REST endpoints to manage
instance and cluster lifecycles, abstracting backend/cluster details from the NVCF and NVCT
APIs.

## Module layout

This is a two-module Bazel component in the root `nvcf` module. It consumes
nv-boot through direct source labels, like `cloud-tasks` and `cloud-functions`.

| Module        | Type           | Status   | Description |
|---------------|----------------|----------|-------------|
| `icms-core`   | library        | current  | Core BYOC / NVCA business logic, REST endpoints, persistence, and shared integration-test fixtures. |
| `icms-service`| app starter    | current  | Thin Spring Boot starter that depends on `icms-core` and provides the deployable application. |

Shared Bazel configuration and dependency locks live at the monorepo root. The
`local_env/` directory contains the component's developer environment.

## Build

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/nvcf-bazel-cache"

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/instance-cluster-management/...

bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  test //src/control-plane-services/instance-cluster-management/... \
  --cache_test_results=no \
  --test_output=errors \
  --test_env=PATH \
  --test_env=HOME \
  --test_env=DOCKER_HOST \
  --test_env=DOCKER_TLS_VERIFY \
  --test_env=DOCKER_TLS_CERTDIR \
  --test_env=DOCKER_CERT_PATH
```

Tests receive `local_env/...` as Bazel runfiles so both modules resolve shared
fixtures consistently. The root `MODULE.bazel` and `maven_install.json` own the
third-party dependency graph.

## Run locally

Run the following commands from the monorepo root:

```bash
export BAZEL_OUTPUT_USER_ROOT="${TMPDIR:-/tmp}/nvcf-bazel-cache"

# Start local dependencies (Cassandra, LocalStack AWS, NATS, OAuth2 mock)
docker compose \
  -f src/control-plane-services/instance-cluster-management/local_env/docker-compose.yml \
  up -d

# Build and run the application with the local profile
bazel --output_user_root="${BAZEL_OUTPUT_USER_ROOT}" \
  build //src/control-plane-services/instance-cluster-management/icms-service:app
java -Dspring.profiles.active=local \
  -jar bazel-bin/src/control-plane-services/instance-cluster-management/icms-service/app.jar
```

The app listens on port `8080` with `/actuator/health` available.

See [BAZEL.md](BAZEL.md) for complete test, coverage, NOTICE, and Docker
commands.

## Profiles

`local`, `ncp` (self-managed / air-gapped NVCF), and `test`. Runtime configuration files
live in `icms-service/src/main/resources/application-{profile}.yaml` and
`bootstrap-{profile}.yaml`; `icms-core` keeps test-only configuration under
`icms-core/src/test/resources`.

See [CLAUDE.md](CLAUDE.md) and [AGENTS.md](AGENTS.md) for deeper architecture and contribution notes.
