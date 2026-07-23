# Single-Cluster Upstream Images BDD Design

## Goal

Add a fully wired local BDD feature that proves the documented upstream
supporting-image overrides work during a single-cluster Helmfile bring-up.
The feature covers:

- `docker.io/natsio/nats-server-config-reloader:0.23.0`
- `docker.io/alpine/k8s:1.36.1` for API account bootstrap
- the current Cassandra initialization image and NATS NKey rendering behavior,
  proving that their legacy image values are not configuration-only overrides

The work is implemented on the PR 371 branch and is pushed only after the
static and destructive live checks pass.

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
as an independent live feature with one end-to-end scenario. It prepares the
same `local-bdd` environment and secret files as the existing single-cluster
Helmfile feature, copies `Makefile.dist` to the generated `Makefile` expected
by the local workflow, applies the two supported documentation-prescribed
replacements to `deploy/stacks/self-managed/global.yaml.gotmpl`, renders the
stack, captures the current Cassandra and NATS behavior, and then installs it.

Add one generic block-substitution file operation:

```gherkin
And I substitute a block in file "<path>":
  """
  <old block>
  ---
  <new block>
  """
```

The docstring contains exactly one `---` separator line. The handler
interpolates `${VAR}`, snapshots the destination through the existing suite
ledger, and delegates parsing and exact replacement to a pure `dsl` helper.
The helper fails if the separator is malformed or the old block is absent.
This keeps the global template safe and makes documentation drift fail loudly:
suite teardown restores it after success or failure. The step belongs to the
existing "substitute strings" DSL category and contains no domain logic.

The feature gets two explicit Go entry points in `tests/bdd/godog_test.go`:

- A fake-runner wiring test that runs under `go test -short`, proves every
  step resolves, and confirms the template and install commands were invoked.
- A live test that is skipped under `-short` and uses `runLiveFeature`.

## Scenario Flow

The feature has one scenario:

1. Requires `NGC_API_KEY`, `SAMPLE_NGC_ORG`, and `SAMPLE_NGC_TEAM`.
2. Checks that the conflicting `ncp-local-cp` cluster is absent. Its comment
   gives the exact remediation:
   `make -C tools/ncp-local-cluster destroy-multicluster`.
3. Copies `Makefile.dist` to `Makefile` through the file ledger.
4. Asserts the durable, gitignored kubelet credential-provider Docker config
   exists. The suite must not delete this live bind-mount source while k3d is
   running.
5. Copies and customizes the existing `self-managed-local-bdd.yaml` fixture.
6. Creates `local-bdd-secrets.yaml` using the existing base64 substitution.
7. Replaces the exact two supported image blocks in `global.yaml.gotmpl`.
8. Starts the cached single-cluster `ncp-local` cluster.
9. Creates the existing NGC image pull secret in the required namespaces.
10. Runs `make -C deploy/stacks/self-managed template HELMFILE_ENV=local-bdd`.
   The command points `HELM_REGISTRY_CONFIG` at the durable, gitignored Docker
   config so Helm can pull the remaining private charts.
11. Searches the generated `out/` manifests and asserts the expected upstream
   images are present in the NATS and API releases. It also records that
   Cassandra uses `nvcf-cassandra-migrations` and NATS renders NKeys directly
   as Secrets, so legacy Alpine image values cannot affect those workloads.
12. Runs scoped Helmfile installs for `release-group=dependencies`,
   `name=ess-api`, `name=nats-auth-callout-service`, and `name=api`. These are
   the releases that own the NATS override or are required to execute the API
   bootstrap hook and keep the API deployment ready; unrelated service images
   are outside this focused feature.
13. Confirms the expected dependency, ESS, and API Helm releases are deployed.
14. Reads the NATS StatefulSet with `kubectl` and asserts that its reloader
   container uses
   `docker.io/natsio/nats-server-config-reloader:0.23.0`.

## Verification Strategy

Rendering and live verification are both required.

The API Alpine image consumer is a Helm hook job that can be deleted after
completion, so post-install Kubernetes inspection alone cannot reliably prove
its configured image. The template assertion proves that it renders with
`docker.io/alpine/k8s:1.36.1`. A successful Helmfile install then proves that
the hook job could pull the image and finish.

Template assertions also prove the current limits of the values surface:
Cassandra renders `nvcf-cassandra-migrations`, and NATS renders NKey Secrets
without a job. Those two operations need chart changes before an `alpine-k8s`
override can be tested as a live workload.

The NATS reloader is a persistent container. The render assertion proves its
configuration, and the post-install StatefulSet readback proves the running
workload uses the upstream image.

## Failure Behavior

- A missing environment variable fails before credentials or manifests are
  authored.
- A missing `Makefile.dist` fails during ledger-backed file preparation.
- A missing or invalid durable Docker config fails before bootstrap or during
  image pulls from the remaining NGC artifacts.
- Unavailable or incompatible Helmfile tooling fails at `make template`.
- A conflicting multi-cluster topology fails before cluster bootstrap and
  prints the documented destroy command in the feature.
- Missing Docker Hub egress, a missing upstream tag, or an incompatible image
  fails one of the focused live Helmfile installs.
- Any mismatch between the documentation and rendered manifests fails an
  explicit output assertion before installation.
- The suite ledger restores every file changed through the DSL, including
  `global.yaml.gotmpl` and the generated `Makefile`. It does not own the
  durable kubelet Docker config.

## Guidance Self-Review

The design was checked against `tests/bdd/AGENTS.md`, `PLAN.md`,
`PLAN_DESTRUCTIVE_CLEANUP.md`, and `README.md`.

- The feature remains values-driven and uses the existing single-cluster
  bootstrap Given.
- The conflict precheck appears before bootstrap and names the exact
  remediation command.
- The new step is generic, belongs to the existing file-operation vocabulary,
  delegates parsing and replacement to `dsl`, and snapshots through the
  Ledger.
- `PLAN.md` is updated before handler code and the handler receives a positive
  unit test.
- The wiring test checks status plus behavior-level command substrings rather
  than deep-equality of every recorded command.
- Destructive setup uses `BDD_CLEANUP_MODE=topology-multi` for the live run
  because that mode removes every stale `ncp-local*` topology.

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
BDD_CLEANUP_MODE=topology-multi \
  go test -run '^TestSingleClusterHelmfileUpstreamImages$' -timeout 90m -v
```

Before completion, also run `git diff --check` and confirm the worktree has
only the intended documentation, BDD, design, and issue-log changes.
