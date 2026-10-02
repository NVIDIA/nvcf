# Compatibility Matrix

NVCF ships as three independently versioned Helm stacks: the self-managed
control plane, the compute plane, and the observability stack. Each stack
releases from its own release train and publishes its own documentation
version. Use this page to pick stack versions that are qualified to run
together.

Releases are listed as exact semantic versions (`X.Y.Z`). A requirement such
as `1.0.0 or later` includes later compatible patch and minor releases. Only
the latest minor release train and the one before it are maintained. Upgrade
to a maintained train before moving further.

{/*docs-version-sync:BEGIN compatibility-matrix*/}

## Current stack releases

| Stack | Latest release | Source tag |
| --- | --- | --- |
| [Self-managed (control plane)](/nvcf/self-managed/) | `1.0.1` | `deploy/stacks/self-managed/v1.0.1` |
| [Compute plane](/nvcf/compute-plane/) | `1.0.0` | `deploy/stacks/nvcf-compute-plane/v1.0.0` |
| [Observability](/nvcf/observability/) | `1.0.0` | `deploy/stacks/observability/v1.0.0` |

## Compatible stack versions

| Stack | Release | Works with |
| --- | --- | --- |
| Self-managed (control plane) | `1.0.1` | Compute plane `1.0.0` or later, Observability `1.0.0` or later |
| Self-managed (control plane) | `1.0.0` | Compute plane `1.0.0` or later, Observability `1.0.0` or later |
| Compute plane | `1.0.0` | Self-managed (control plane) `1.0.0` or later, Observability `1.0.0` or later |
| Observability | `1.0.0` | Self-managed (control plane) `1.0.0` or later, Compute plane `1.0.0` or later |

{/*docs-version-sync:END compatibility-matrix*/}

Stack versions above are read from the latest published GitHub release of
each stack. Documentation for each stack version is available from the
version menu on that stack's tab.
