# AGENTS.md - Observability Stack

## Purpose

This is the shared NVCF observability Helmfile stack scaffold. It owns optional
cluster-level observability infrastructure intended for self-managed
control-plane and compute-plane deployments once consuming stack wiring lands.

## Scope

- Keep shared collector, target allocator, Prometheus Operator CRD,
  OpenTelemetry Operator, optional VictoriaMetrics, and default monitor wiring
  here.
- Keep application metrics endpoints, labels, port names, and paths in the
  application charts that expose those endpoints.
- Do not add control-plane services or compute-plane services here.
- Keep base runtime gates off until a consuming stack owns chart distribution,
  mirrored images, and environment overlays.
- Consumer overlays for self-managed control-plane deployments may enable the
  control-plane collector, OTel Operator, Prometheus Operator CRDs, and bundled
  TSDB contract together.
- Keep BYOO and NVCA collectors default off unless a consumer explicitly
  enables them through values.

## Key Files

- `helmfile.d/01-observability.yaml.gotmpl`: shared observability releases
- `environments/base.yaml`: scaffold defaults and scrape contracts
- `charts/nvcf-otel-collector`: OpenTelemetryCollector resource and Target Allocator RBAC
- `charts/nvcf-default-monitors`: centrally owned concrete monitor resources
- `values/victoria-metrics.yaml.gotmpl`: values bridge for the VictoriaMetrics chart

## Modes

- `install`: this stack installs and owns enabled shared infrastructure.
- `existing`: this stack may render default monitors, but shared infrastructure
  is owned by another stack instance.
- `disabled`: this stack renders no observability runtime resources. This is
  not a supported self-managed control-plane autoscaler mode, but is valid for
  compute-plane deployments that do not need NVCF-managed scrape coverage.

Do not use Helm `lookup` to silently change modes based on live cluster state.
The consuming stack must choose the mode explicitly.
