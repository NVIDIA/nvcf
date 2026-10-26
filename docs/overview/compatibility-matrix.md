# Compatibility Matrix

<Warning>
NVCF 1.0.4 candidate, pending QA. The tables lock the selected combination for
evaluation; they do not declare whole-combination qualification. "Latest
release" below means the version selected for this candidate, not the latest
version on main. Use the [upgrade notes](release-notes/1.0.2-to-1.0.4-upgrade.md)
for the comparison to docs edition 1.0.2 and required ordering.
</Warning>

{/*docs-version-sync:BEGIN compatibility-matrix*/}

## Current stack releases

| Stack | Latest release | Source tag |
| --- | --- | --- |
| [Self-managed (control plane)](../self-managed/installation.md) | `1.0.7` | `deploy/stacks/self-managed/v1.0.7` |
| [Compute plane](../compute-plane/cluster-management/index.md) | `1.0.3` | `deploy/stacks/nvcf-compute-plane/v1.0.3` |
| [Observability](../observability/observability.md) | `1.0.0` | `deploy/stacks/observability/v1.0.0` |

## Compatible stack versions

| Stack | Release | Works with |
| --- | --- | --- |
| Self-managed (control plane) | `1.0.7` | Compute plane `1.0.3` only, Observability `1.0.0` only |
| Compute plane | `1.0.3` | Self-managed (control plane) `1.0.7` only, Observability `1.0.0` only |
| Observability | `1.0.0` | Self-managed (control plane) `1.0.7` only, Compute plane `1.0.3` only |

{/*docs-version-sync:END compatibility-matrix*/}

The tables show the stack versions documented by the selected release.
