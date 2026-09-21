# Compatibility Matrix

NVCF ships as three independently versioned Helm stacks: the self-managed
control plane, the compute plane, and the observability stack. Each stack
releases from its own release train and publishes its own documentation
version. Use this page to pick stack versions that are qualified to run
together.

Releases are listed as trains (`X.Y`); any patch release on a train counts.
A requirement such as `1.0 or later` means that train and every later
maintained train. Only the latest train and the one before it are maintained;
upgrade to a maintained train before moving further.

{/*docs-version-sync:BEGIN compatibility-matrix*/}

## Current stack releases

| Stack | Latest release | Source tag | Documentation |
| --- | --- | --- | --- |
| Self-managed (control plane) | `0.20.7` | `deploy/stacks/self-managed/v0.20.7` | [dev](/nvcf/self-managed/) |
| Compute plane | `0.4.4` | `deploy/stacks/nvcf-compute-plane/v0.4.4` | [dev](/nvcf/compute-plane/) |
| Observability | `0.2.2` | `deploy/stacks/observability/v0.2.2` | [dev](/nvcf/observability/) |

## Compatible stack versions

| Stack | Release | Works with |
| --- | --- | --- |
| Self-managed (control plane) | `1.0` | Compute plane `1.0` or later, Observability `1.0` or later |
| Compute plane | `1.0` | Self-managed (control plane) `1.0` or later, Observability `1.0` or later |
| Observability | `1.0` | Self-managed (control plane) `1.0` or later, Compute plane `1.0` or later |

{/*docs-version-sync:END compatibility-matrix*/}

Stack versions above are read from the latest published GitHub release of
each stack. Documentation for each stack version is available from the
version menu on that stack's tab.
