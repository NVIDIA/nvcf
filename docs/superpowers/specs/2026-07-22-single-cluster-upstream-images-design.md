# Single-Cluster Upstream Images BDD Design

## Goal

Add a fully wired local BDD feature that proves the documented upstream
supporting-image overrides work during a single-cluster Helmfile bring-up.
The feature covers:

- `docker.io/natsio/nats-server-config-reloader:0.23.0`
- `docker.io/alpine/k8s:1.36.1` for Cassandra initialization
- `docker.io/alpine/k8s:1.36.1` for the NATS nkey job
- `docker.io/alpine/k8s:1.36.1` for API account bootstrap

The work remains local to the PR 371 worktree and is not pushed.

## Scope

The feature uses the existing local k3d single-cluster Helmfile workflow.
It does not cover multi-cluster, EKS, the CLI one-click workflow, or upstream
overrides for any other image.

The existing NGC pull secret remains necessary because the rest of the stack
still pulls mirrored images from NGC. The two upstream images are public, so
they do not require an additional pull secret, but the cluster must have
outbound access to Docker Hub.

## Architecture

Create `tests/bdd/features/single-cluster-helmfile-upstream-images.feature`
as an independent live feature. It prepares the same `local-bdd` environment
and secret files as the existing single-cluster Helmfile feature, applies the
four documentation-prescribed replacements to
`deploy/stacks/self-managed/global.yaml.gotmpl`, renders the stack, and then
installs it.

Add one generic file-operation step:

```gherkin
And I substitute "<old>" in file "<path>" with "<new>"
```

The handler interpolates `${VAR}` in all three arguments, snapshots the
destination through the existing suite ledger, and delegates literal
replacement to `dsl.SubstituteFile`. This keeps the global template safe:
suite teardown restores it after success or failure. The step belongs to the
existing "substitute strings" DSL category and contains no domain logic.

The feature gets two explicit Go entry points in `tests/bdd/godog_test.go`:

- A fake-runner wiring test that runs under `go test -short`, proves every
  step resolves, and confirms the template and install commands were invoked.
- A live test that is skipped under `-short` and uses `runLiveFeature`.

## Scenario Flow

The feature has two ordered rules.

The authoring and rendering rule:

1. Requires `NGC_API_KEY`, `SAMPLE_NGC_ORG`, and `SAMPLE_NGC_TEAM`.
2. Copies and customizes the existing `self-managed-local-bdd.yaml` fixture.
3. Creates `local-bdd-secrets.yaml` using the existing base64 substitution.
4. Replaces the exact four image blocks in `global.yaml.gotmpl`.
5. Runs `make -C deploy/stacks/self-managed template HELMFILE_ENV=local-bdd`.
6. Asserts successful rendering and the expected upstream image references.

The live installation rule:

1. Checks that the conflicting `ncp-local-cp` cluster is absent. Its comment
   gives the exact remediation:
   `make -C tools/ncp-local-cluster destroy-multicluster`.
2. Starts the cached single-cluster `ncp-local` cluster.
3. Creates the existing NGC image pull secret in the required namespaces.
4. Runs `make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd`.
5. Confirms the expected Helm releases are deployed.
6. Reads the NATS StatefulSet with `kubectl` and asserts that its reloader
   container uses
   `docker.io/natsio/nats-server-config-reloader:0.23.0`.

## Verification Strategy

Rendering and live verification are both required.

The Alpine image consumers are Helm jobs. Some are hooks that can be deleted
after completion, so post-install Kubernetes inspection alone cannot reliably
prove their configured images. The template assertions prove that all three
jobs render with `docker.io/alpine/k8s:1.36.1`. A successful Helmfile install
then proves those hook jobs could pull the image and finish.

The NATS reloader is a persistent container. The render assertion proves its
configuration, and the post-install StatefulSet readback proves the running
workload uses the upstream image.

## Failure Behavior

- A missing environment variable fails before credentials or manifests are
  authored.
- A missing stack `Makefile` or unavailable Helmfile tooling fails at the
  existing `make template` step.
- A conflicting multi-cluster topology fails before cluster bootstrap and
  prints the documented destroy command in the feature.
- Missing Docker Hub egress, a missing upstream tag, or an incompatible image
  fails the live Helmfile install.
- Any mismatch between the documentation and rendered manifests fails an
  explicit output assertion before installation.
- The suite ledger restores every file changed through the DSL, including
  `global.yaml.gotmpl`.

## Validation

Run:

```bash
cd tests/bdd
go test -short ./...
scripts/lint.sh
```

The live test is invoked explicitly when the local cluster and credentials are
available:

```bash
cd tests/bdd
go test -run '^TestSingleClusterHelmfileUpstreamImages$' -v
```

Before completion, also run `git diff --check` and confirm the worktree has
only the intended local test and planning changes.
