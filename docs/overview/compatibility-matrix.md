# Compatibility Matrix

<Warning>
NVCF 1.0.4 candidate, pending QA. The generated table records exact bundle
membership. It is not a declaration that every combination of those stack
trains has passed QA, or that other compatible patch/minor releases are excluded.
</Warning>

Umbrella versions and docs editions identify a documented combination. Stack,
chart and image versions are independent; they do not need matching version
numbers. The selected [product manifest](manifest.md#candidate-identity) fixes
the three source releases, inventory hashes and artifact pins for this candidate.

{/*docs-version-sync:BEGIN compatibility-matrix*/}

## Selected stack releases

| Stack | Selected version | Source tag |
| --- | --- | --- |
| [Self-managed (control plane)](../self-managed/installation.md) | `1.0.7` | `deploy/stacks/self-managed/v1.0.7` |
| [Compute plane](../compute-plane/cluster-management/index.md) | `1.0.4` | `deploy/stacks/nvcf-compute-plane/v1.0.4` |
| [Observability](../observability/observability.md) | `1.0.0` | `deploy/stacks/observability/v1.0.0` |

{/*docs-version-sync:END compatibility-matrix*/}

## Compatibility and maintenance policy

A compatibility declaration such as `X.Y.Z+` or "X.Y.Z or later" covers
compatible later patch and minor releases subject to the supported stack train
policy and upgrade prerequisites. It does not mean any later major release,
same-numbered stack versions, or every arbitrary mixed combination is qualified.

- Each stack maintains its latest minor train and the preceding train (N and
  N-1). Older trains receive no further patches.
- Patch upgrades within a train are supported. Cross-stack compatibility
  declarations still govern which stack trains can run together.
- When crossing more than one train, upgrade through the latest patch of each
  intermediate train. Complete its migration and upgrade prerequisites before
  continuing.

The exact peer pins in this candidate's product manifest lock the combination
under test. They do not replace the broader compatibility policy with an
"only these versions are supported" restriction. Conversely, a compatible
range does not change the contents or qualification of an immutable bundle.
Use a separately reviewed release selection when changing that combination.

Docs edition 1.0.2 selected Self-Managed 1.0.1, Compute Plane 1.0.0 and
Observability 1.0.0. Docs edition 1.0.3 changed documentation only and retained
that combination. Follow the [1.0.4 upgrade notes](release-notes/1.0.2-to-1.0.4-upgrade.md)
for the required ordering and [umbrella consumption guide](umbrella-releases.md)
for bundle acquisition and customer configuration.
