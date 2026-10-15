# instance-cluster-management (ICMS)

NVIDIA Instance and Cluster Management Service (ICMS) — exposes REST endpoints to manage
instance and cluster lifecycles, abstracting backend/cluster details from the NVCF and NVCT
APIs.

## Module layout

This is a multi-module Maven project. The root `pom.xml` is an aggregator that inherits from
`com.nvidia.boot:nv-boot-parent` (the NV-native Spring Boot parent, which itself extends
`spring-boot-starter-parent`), mirroring the layout used by `cloud-tasks` and `cloud-functions`.

| Module        | Type           | Status   | Description |
|---------------|----------------|----------|-------------|
| `icms-core`   | library        | current  | Core BYOC / NVCA business logic, REST endpoints, persistence, and shared integration-test fixtures. |
| `icms-service`| app starter    | current  | Thin Spring Boot starter that depends on `icms-core` and provides the deployable application. |

Shared, repo-wide configuration lives at the root: `pom.xml`, `settings.xml`, `lombok.config`,
`.gitlab-ci.yml`, `.nspect-vuln-allowlist.toml`, `.gitignore`, `AGENTS.md`, `CLAUDE.md`, and the
`local_env/` developer environment.

## Build

```bash
# Build everything from the repo root
mvn clean install

# Build just the core module (with its dependencies)
mvn -pl icms-core -am clean install

# Build the runnable service module (with its dependencies)
mvn -pl icms-service -am clean install

# Run tests
mvn test
```

Tests run with the Surefire `workingDirectory` pinned to the repo root (`root.dir`), so module
test code can resolve shared relative paths such as `local_env/...` consistently.

Artifact resolution uses the NVIDIA Artifactory repos declared in the root `pom.xml`
(`nvcf`, etc.). CI authenticates via the root `settings.xml`, which reads masked CI/CD
variables from the environment.

## Run locally

```bash
# Start local dependencies (Cassandra, LocalStack AWS, NATS, OAuth2 mock)
cd local_env
docker compose up -d
cd ..

# Run the application (local profile)
mvn -pl icms-service spring-boot:run
```

The app listens on port `8080` with `/actuator/health` available.

## Profiles

`local`, `ncp` (self-managed / air-gapped NVCF), and `test`. Runtime configuration files
live in `icms-service/src/main/resources/application-{profile}.yaml` and
`bootstrap-{profile}.yaml`; `icms-core` keeps test-only configuration under
`icms-core/src/test/resources`.

See [CLAUDE.md](CLAUDE.md) and [AGENTS.md](AGENTS.md) for deeper architecture and contribution notes.
