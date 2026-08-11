# BYOO Telemetry Compatibility

NVIDIA Cloud Functions (NVCF) Bring Your Own Observability (BYOO) preserves
valid telemetry as its upstream components emit it. Collector upgrades can
change metric names, types, labels, label values, or histogram buckets. Treat
these changes as part of the upstream component's interface.

## Compatibility policy

NVCF follows these rules for upstream telemetry:

- Preserve valid upstream metric names, types, labels, and label values.
- Do not rename, filter, or modify a valid metric only to retain an earlier
  NVCF output shape.
- Treat OpenTelemetry Collector and cAdvisor telemetry as authoritative unless
  NVCF documents a user-facing reason for a transformation.
- Update generated metric references and validation fixtures after a verified
  upstream behavior change.

This policy does not prevent documented filters that limit telemetry to an
NVCF workload, remove sensitive metadata, or apply a customer-configured metric
subset. It prevents compatibility rules that hide valid upstream changes only
to make new output match an old validation baseline.

## What to expect during upgrades

Review dashboards, alerts, recording rules, and automation when the BYOO
OpenTelemetry Collector version changes. A valid upstream change can require
updates even when telemetry collection continues to work.

For example, an OpenTelemetry Collector release changed some `otelcol_*`
counter names so they no longer included the `_total` suffix. NVCF preserves
the names emitted by the collector instead of adding the suffix to retain the
earlier shape.

cAdvisor also emits pod-sandbox metrics with the label `container="POD"`.
These are valid upstream series and can provide pod network, CPU, and memory
telemetry. NVCF preserves these series instead of dropping them because the
container label does not name an application container.

## Validation behavior

NVCF validation compares observed telemetry with generated metric references
and golden fixtures. A difference from an older fixture is not automatically a
collector regression.

When an upstream component changes its telemetry, maintainers:

1. Verify the observed metric against the upstream component behavior.
2. Confirm that the metric is valid and belongs to the supported BYOO scope.
3. Update generated metric references and golden fixtures to the new output.
4. Add a transformation only when a documented user-facing requirement needs
   one.

Do not ignore a valid metric, alias its name, synthesize labels, or rewrite
label values only to make it pass an older fixture. Document any intentional
transformation and its user-visible reason.

For BYOO collector options, refer to
[NVCA Configuration](./configuration.md#agent-config-merging).
