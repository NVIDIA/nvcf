# Compatibility Matrix

The table records the stack releases documented in this edition. Umbrella
versions and docs editions identify a combination of independently released
stacks. Stack, chart and image versions do not need matching version numbers.

{/*docs-version-sync:BEGIN compatibility-matrix*/}

## Selected stack releases

| Stack | Selected version | Source tag |
| --- | --- | --- |
| [Self-managed (control plane)](../self-managed/installation.md) | `1.0.7` | `deploy/stacks/self-managed/v1.0.7` |
| [Compute plane](../compute-plane/cluster-management/index.md) | `1.0.4` | `deploy/stacks/nvcf-compute-plane/v1.0.4` |
| [Observability](../observability/observability.md) | `1.0.0` | `deploy/stacks/observability/v1.0.0` |

{/*docs-version-sync:END compatibility-matrix*/}

## Compatibility and maintenance policy

Later patch or minor releases can be used when their stack compatibility and
migration requirements are satisfied. "Or later" does not mean any later major
release, same-numbered stack versions, or an arbitrary mix of releases.

- Each stack maintains its latest minor train and the preceding train (N and
  N-1). Older trains receive no further patches.
- Patch upgrades within a train are supported. Cross-stack compatibility
  declarations still govern which stack trains can run together.
- When crossing more than one train, upgrade through the latest patch of each
  intermediate train. Complete its migration and upgrade prerequisites before
  continuing.

A product manifest fixes the stack releases and artifacts shipped in one
umbrella archive. That selection does not replace the stack compatibility
policy with an "only these versions are supported" restriction. A compatible
range does not change the contents of an existing archive.

Docs edition 1.0.2 selected Self-Managed 1.0.1, Compute Plane 1.0.0 and
Observability 1.0.0. Docs edition 1.0.3 changed documentation only and retained
that combination. Follow the [1.0.4 upgrade notes](release-notes/1.0.2-to-1.0.4-upgrade.md)
for the required ordering and [umbrella consumption guide](umbrella-releases.md)
for bundle acquisition and customer configuration.
