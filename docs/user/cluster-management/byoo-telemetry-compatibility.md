# BYOO Telemetry Compatibility

## Summary

NVIDIA Cloud Functions (NVCF) Bring Your Own Observability (BYOO) uses
telemetry from upstream components such as the OpenTelemetry Collector and
cAdvisor. Their output changes over time as those projects move forward. We
preserve valid upstream telemetry instead of rewriting new output to look like
an earlier version. This keeps the telemetry true to its source, but a valid
upgrade can require changes to dashboards, alerts, or automation.

The upstream components are the source of truth for metric semantics. Our
generated metric references and validation fixtures follow that output.

## Key decisions

1. We preserve valid upstream metric names, types, labels, label values, and
   histogram buckets.
1. We do not rename, filter, or modify a valid metric only to retain an earlier
   NVCF output shape.
1. We transform upstream telemetry only when we have a documented user-facing
   reason.
1. We update generated metric references and validation fixtures after we
   verify an upstream behavior change.

This policy does not prevent documented filters that limit telemetry to an
NVCF workload, remove sensitive metadata, or apply a customer-configured metric
subset. It prevents compatibility rules that hide valid upstream changes only
to make new output match an old validation baseline.

## What changes during upgrades

Review dashboards, alerts, recording rules, and automation when the BYOO
OpenTelemetry Collector version changes. A valid upstream change can require
updates even when telemetry collection continues to work.

For example, an OpenTelemetry Collector release exposed some `otelcol_*`
counter metrics without the `_total` suffix used by an earlier version. We keep
the names emitted by the collector instead of adding the suffix back.

cAdvisor also emits pod-sandbox metrics with the label `container="POD"`.
These are valid upstream series and can provide pod network, CPU, and memory
telemetry. We keep these series instead of dropping them because the
container label does not name an application container.

## How we validate changes

We use generated metric references and golden fixtures to catch differences in
observed telemetry. These checks tell us that the output changed. They do not
make an older fixture the permanent telemetry contract.

When an upstream component changes its telemetry, maintainers:

1. Verify the observed metric against the upstream component behavior.
1. Confirm that the metric is valid and belongs to the supported BYOO scope.
1. Update generated metric references and golden fixtures to the new output.
1. Add a transformation only when a documented user-facing requirement needs
   one.

Do not ignore a valid metric, alias its name, synthesize labels, or rewrite
label values only to make it pass an older fixture. Document any intentional
transformation and its user-visible reason.

## Further reading

For BYOO collector options, refer to
[NVCA Configuration](./configuration.md#agent-config-merging).
