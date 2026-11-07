# NVCF 1.0.4

NVCF 1.0.4 combines the following qualified stack releases:

| Stack | Version |
| --- | --- |
| Self-Managed | 1.0.10 |
| Compute Plane | 1.0.5 |
| Observability | 1.0.0 |

## Changes

- Updates selected service, worker, Java runtime, and telemetry dependencies
  for security maintenance. Includes NICLLS 2.118.2, Samba 1.0.9, and the
  compute-plane collectors at 0.160.0-nv-0.2.9.
- Updates OpenBao server and agent to 2.6.3 and includes both JWT plugin
  versions for staged upgrades. Cassandra remains on Apache 5.0.9 with
  updated trust-store and migration dependencies.
- Corrects Samba share ownership setup for non-root Helm task results and
  preserves readable secret-file ownership during non-root secret refresh.
- Fixes NVCA stream restart handling, NATS authentication reconnect and
  account selection, and ICMS reconnect and instance-event processing.
- Replaces legacy task-worker references with published init and task images.
  Adds upstream image overrides for NATS, cert-manager, and the Vault injector.

See the [detailed release notes](https://github.com/dsx-ai-factory/nvcf/blob/e68f796892a2392d1b681e2a16812650e2f1e1be/fern/changelog/2026-10-26.mdx)
for the changes relative to documentation edition 1.0.2.

## Download and install

Download the attached `nvcf-1.0.4.tar.gz`, `manifest.json`, and
`release-assets.json`. The asset index records SHA256 digests. Use the packaged
umbrella archive rather than GitHub's automatically generated source archives.

Extract into a new persistent directory and retain the sibling directories
under `deploy/stacks/`. Use their existing Helmfile/Make installation entrypoints
with separate environment files, secrets, and explicit Kubernetes contexts.
The recorded toolchain is Helm 3.21.4 and Helmfile 1.1.9.

Read the [release consumption guide](https://github.com/dsx-ai-factory/nvcf/blob/e68f796892a2392d1b681e2a16812650e2f1e1be/docs/overview/umbrella-releases.md)
and [upgrade procedure](https://github.com/dsx-ai-factory/nvcf/blob/e68f796892a2392d1b681e2a16812650e2f1e1be/docs/overview/release-notes/1.0.2-to-1.0.4-upgrade.md)
before deployment. Upgrades require the documented hostname-policy prerequisite,
NVCA-first ordering, task-image preparation, queue draining, and staged OpenBao
bridge/member transition. Preserve backups and follow the documented rollback
limits; replacing the archive does not reverse database migrations.

The maintained 1.0.4 documentation edition is activated separately. Until then,
use the source-pinned guides above for this release's procedures.
