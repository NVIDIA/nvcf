# Release process

NVCF components release independently. An umbrella product release selects one
qualified combination of Self-Managed, Compute Plane and Observability; it does
not assign their versions or release every service together.

## Choose a release workflow

| Task | Guide |
| --- | --- |
| Component versioning, tags, maintenance branches and RCs | [GitHub release automation](docs/dev/github-release-process.md) |
| Select stacks, assemble a QA candidate and publish the product | [Umbrella releases](docs/dev/umbrella-releases.md) |
| Correct or activate maintained product docs | [Umbrella docs workflow](docs/dev/umbrella-releases.md#prepare-and-activate-maintained-docs) |
| Prepare a legacy immutable docs edition | [Docs editions](docs/dev/docs-editions.md) |
| Inventory, Development catalog and manifest tooling | [docs-version-sync](tools/docs-version-sync/README.md) |

The workflows and helpers linked by those guides are the implementation
reference. Check the workflow at the actual release ref and live repository
variables before execution, especially on an older maintenance branch.

## Component releases

Services and charts normally use path-scoped semantic-release on `main`.
Release-worthy commits create component tags and GitHub Releases. The scheduled
artifact publisher discovers published Releases after their tags reach its
source mirror. A source release is not proof of registry availability.

Ordinary service/chart maintenance merges do not auto-tag. Reuse a verified
fixed release in the required minor or backport and explicitly release the next
unused patch. Main pin-update PRs do not update maintenance branches. Preserve
newer main selections and immutable historical tags/assets.

The three stacks declare `version_file` and `release_branch_only`. They release
from `release-deploy/stacks/<stack>/vX.Y`, not `main`. A new train uses
`branch-cut`; patches use the existing train. Once a stable release exists,
a change to the stack tree excluding `VERSION` creates the next patch. An
unrelated CI or version-file-only change does not. The tag workflow attaches
the original dependency inventory.

Stack source releases may proceed before umbrella QA. Their stable versions
and inventories become candidate inputs. Customer resource publication is a
separate operation and does not publish a stack's referenced images/charts.

## Umbrella qualification and publication

Select three exact stable released stacks in
`deploy/releases/X.Y.Z/manifest.json`. Candidate CI validates their public
source identities and original inventories, then assembles a QA artifact.
It does not publish a stable umbrella or run deployment qualification.

QA approves the selected combination, tested configuration and final assets.
Changing pins or artifact bytes requires renewed acceptance. Product metadata
and notes must land on `main` before the explicit `Publish Umbrella Release`
workflow runs with a fixed reviewed target SHA through validate, draft and
promote. The immutable product tag is `nvcf/X.Y.Z`.

Product publication does not change the docs default. Maintained umbrella docs
and legacy immutable editions have distinct workflows; follow the supported
manifest-schema requirements in the owning guide. Preserve frozen editions.
