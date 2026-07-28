# AGENTS.md - Observability Stack

## Purpose

This is the shared NVCF observability Helmfile stack. It owns cluster-level
observability infrastructure for self-managed control-plane and compute-plane
deployments.

## Scope

- Keep shared collector, target allocator, Prometheus Operator CRD,
  OpenTelemetry Operator, optional VictoriaMetrics, and default monitor wiring
  here.
- Keep application metrics endpoints, labels, port names, and paths in the
  application charts that expose those endpoints.
- Do not add control-plane services or compute-plane services here.
- Keep the reusable base profile `disabled`.
- Derive control-plane and compute-plane behavior from
  `observability.profile`; do not expose parallel plane booleans.
- Enabled profiles install shared components once by default. Fine-grained
  component modes are `install`, `existing`, and `disabled`.
- Derive NVCA BYOO support from compute and all profiles. Keep the NVCA
  collector disabled by default.

## Key Files

- `helmfile.d/01-observability.yaml.gotmpl`: shared observability releases
- `environments/base.yaml`: scaffold defaults and scrape contracts
- `charts/nvcf-otel-collector`: OpenTelemetryCollector resource and Target Allocator RBAC
- `charts/nvcf-default-monitors`: centrally owned concrete monitor resources
- `values/victoria-metrics.yaml.gotmpl`: values bridge for the VictoriaMetrics chart

## Profiles

- `disabled`: render no observability runtime resources.
- `control`: install control-plane metrics and autoscaler backend defaults.
- `compute`: install NVCA, DCGM, and worker metric defaults.
- `all`: install the union once.

Do not use Helm `lookup` to silently change profiles or component ownership
based on live cluster state. The consuming stack must choose the profile
explicitly.

## Development

- Render the local environment with `make template HELMFILE_ENV=local`.
- Run profile and integration assertions with `make test`.
- Run `git diff --check` before committing.

## Code Style

- Keep profile derivation and validation at the top of the Helmfile.
- Keep chart templates data-driven; use one generic template per monitor kind.
- Put stable scrape endpoints, labels, ports, and paths in the application
  chart that exposes them.

## Subtree Conventions

- Keep reusable defaults in `environments/base.yaml`; environment files should
  contain only overrides.
- Preserve explicit user values when applying profile defaults.
- Validate every new mode, dependency, and rendered monitor shape in
  `tests/profile-defaults.sh`.
