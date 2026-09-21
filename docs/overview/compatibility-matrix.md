# Compatibility Matrix

NVCF ships as three independently versioned Helm stacks: the self-managed
control plane, the compute plane, and the observability stack. Each stack
releases from its own release train and publishes its own documentation
version. Use this page to pick stack versions that are qualified to run
together.

Versions are listed as trains (`X.Y`). Any patch release on a train is
compatible with any patch release on the trains listed alongside it. Only the
latest train and the one before it are maintained; upgrade to a maintained
train before moving further.

{/*docs-version-sync:BEGIN compatibility-matrix*/}

## Current stack releases

| Stack | Latest release | Source tag | Documentation |
| --- | --- | --- | --- |
| Self-managed (control plane) | `0.20.7` | `deploy/stacks/self-managed/v0.20.7` | [dev](/nvcf/self-managed/) |
| Compute plane | `0.4.4` | `deploy/stacks/nvcf-compute-plane/v0.4.4` | [dev](/nvcf/compute-plane/) |
| Observability | `0.2.2` | `deploy/stacks/observability/v0.2.2` | [dev](/nvcf/observability/) |

## Qualified trains

| Stack | Train | Self-managed trains | Compute plane trains | Observability trains |
| --- | --- | --- | --- | --- |
| Self-managed (control plane) | `1.1` | this stack | `1.1`, `1.0` | `1.1`, `1.0` |
| Self-managed (control plane) | `1.0` | this stack | `1.1`, `1.0` | `1.1`, `1.0` |
| Compute plane | `1.1` | `1.1`, `1.0` | this stack | `1.1`, `1.0` |
| Compute plane | `1.0` | `1.1`, `1.0` | this stack | `1.1`, `1.0` |
| Observability | `1.1` | `1.1`, `1.0` | `1.1`, `1.0` | this stack |
| Observability | `1.0` | `1.1`, `1.0` | `1.1`, `1.0` | this stack |

{/*docs-version-sync:END compatibility-matrix*/}

Stack versions above are read from the latest published GitHub release of
each stack. Documentation for each stack version is available from the
version menu on that stack's tab.
