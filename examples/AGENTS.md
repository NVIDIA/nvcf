# AGENTS.md - examples/

Guide for AI agents working inside `examples/`.

## What lives here

Runnable NVCF sample containers, Helm charts, and load tests.

Top-level groups:

- `function-samples/`: long-running services (FastAPI echo, streaming, multi-endpoint, gRPC echo, secrets, vLLM with OTLP, load tester).
- `function-samples/helmchart-samples/`: Helm charts that wrap the FastAPI and multi-node samples.
- `load-tests/`: k6 scripts for `functions/` (HTTP, gRPC, SSE, streaming).
- `cluster-monitoring-sample/`: Prometheus ServiceMonitor and OTEL collector configs for NVCA.
- `self-hosted-local-development/`: k3d config and setup/teardown scripts for local self-hosted NVCF.

Per-example `README.md` files hold the run instructions. Read them before modifying a sample.

## Building and running

Each example is self-contained. Common flows:

### Python / FastAPI / gRPC containers

```
cd examples/function-samples/<sample>
docker build -t <sample> .
docker run --rm -p 8000:8000 <sample>
```

Smoke-test with `curl`. See each sample's `README.md` for endpoint paths and payloads.

### Helm charts

```
cd examples/function-samples/helmchart-samples/<chart>
helm lint <chart-subdir>
helm install <release> <chart-subdir> -f <override>.yaml
```

Multi-node helm charts use `override.yaml` for cluster-specific tuning; keep example overrides generic and parameter-driven.

### Go binaries (load tester)

```
cd examples/function-samples/load-tester-supreme/http-server
go build ./...
```

Stay on Go versions listed in each `go.mod`. Do not tie load-tester build configuration to the monorepo-wide Go toolchain; these run out-of-tree.

### k6 load tests

```
cd examples/load-tests
k6 run functions/<test>.js
```

k6 scripts accept endpoint URLs and keys through environment variables. Do not hardcode NGC keys, auth tokens, or customer-facing endpoints. Every test must read credentials from env.

### Local self-hosted NVCF

```
cd examples/self-hosted-local-development
./setup.sh
./teardown.sh
```

Follow `README.md` for prerequisites (k3d, Docker, Helm). This script targets a developer laptop, not CI.

## Code style

- No Markdown bold, no emojis, no em-dash (U+2014), ASCII only. See `.cursor/skills/documentation-style/SKILL.md`.
- Python: format with the conventions in each sample; no repo-wide linter is wired up to `examples/`.
- Shell: `set -euo pipefail` at the top of every script; prefer `bash` explicitly when using bashisms.
- YAML: 2-space indent; quote values only when needed.
- Do not introduce new frameworks or dependencies that are not already in use by a neighbouring sample.

## Tests

Each sample is its own unit. There is no shared test runner for `examples/`. When changing a sample:

- Rebuild its container or chart.
- Run its documented smoke test.
- Note the verification you ran in the MR `## Testing` section.

If a change does not lend itself to an automated test (docs only, configuration-only tuning), say so in the MR.

## Adding a new example

1. Pick a directory under the matching group (`function-samples/`, etc.) or create a new group if none fits.
2. Write a `README.md` describing what the sample does, its prerequisites, and how to run it.
3. Add SPDX headers to every source file.
4. Update the top-level `examples/README.md` table for the new sample.
5. If the sample produces images that must ship, reference the public-facing registry path (`nvcr.io/...` with a public org) and keep secrets out of repo.

## Commits and MRs

Follow the repo root `AGENTS.md` conventions: Conventional Commits v1.0.0, DCO sign-off (`git commit -s`), JIRA footer, structured MR description. Scope commits to `(examples)` or a sub-path when a single sample changes.
