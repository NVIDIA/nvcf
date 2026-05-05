# AGENTS.md

## Repo Role
- Repo: `rs-autoscaler`
- Language: Rust
- Purpose: Autoscaling service for NVCF monitors function utilization via a timeseries database and adjusts instance counts through the NVCF API.

## Architecture
- **Scaling loop**: Queries utilization metrics, applies per-function scaling policies (static or custom via gRPC), decides desired instance count.
- **Discovery**: Finds active functions from the timeseries DB (invocations + workers), writes them to Cassandra.
- **Policy cache**: Fetches per-function scaling configs from a gRPC service, caches with configurable TTL.
- **Distributed locking**: Cassandra-based locks for coordinating discovery and scaling across replicas.

## Key Paths
- `crates/server/src/scaling/` — scaling logic, policy client/cache, thresholds, stickiness
- `crates/server/src/work/` — scaling loop, discovery, utilization queries
- `crates/server/src/cassandra/` — Cassandra client, statements, distributed locks
- `crates/server/src/timeseries_db/` — timeseries DB client (Thanos/VictoriaMetrics compatible)
- `crates/server/src/nvcf_api/` — NVCF API client, OAuth2 token client
- `crates/server/resources/` — YAML config files per environment

## Config
- All operational parameters are externalized to YAML settings files with `#[serde(default)]`.
- Environment variable overrides use `__` as separator (e.g., `SCALING__UTILIZATION_WINDOW_SECONDS=70`).
- Secrets are loaded from a JSON file at the path specified by `secrets_path` in config.

## Local Development
```bash
# Start dependencies
docker compose -f crates/server/nvcf-service/local_env/docker-compose.yml up -d cassandra victoriametrics

# Copy and fill secrets
cp local_env/secrets/secrets.json.tmpl local_env/vault/secrets.json

# Run with local config
CONFIG=crates/server/resources/settings-local.yaml \
SECRETS_PATH=local_env/vault/secrets.json \
cargo run --bin server -p rs-autoscaler
```

## Build & Test
```bash
cargo fmt -p rs-autoscaler
cargo clippy -p rs-autoscaler --all-targets -- -D warnings
cargo test -p rs-autoscaler
cargo deny check advisories
```
