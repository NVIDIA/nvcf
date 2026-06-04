# tests/bdd

Strict-DSL BDD suite for local self-managed NVCF workflows. Sits alongside
the legacy `tests/bdd` while feature parity is verified; once the live runs
are green this tree replaces it.

The contract is in `PLAN.md`. The rules for working in this directory are
in `AGENTS.md`.

## Directory layout

```
features/   Gherkin feature files: single-cluster CLI, multi-cluster CLI,
            single/multi-cluster Helmfile (k3d), and single/multi-cluster
            EKS Helmfile (non-local).
fixtures/   Starting environment YAML and CLI config the features copy from.
harness/    Suite lifecycle: Config, CommandRunner, Ledger, CommandCache,
            Suite. Builds nvcf-cli at suite start; exports NVCF_CLI and
            REPO_ROOT into the process env via t.Setenv.
dsl/        Pure helpers: ${VAR} interpolation, dotted-path YAML upsert
            and read, YAML subtree match/contain, base64 substitute, JSON
            row matching, kubectl manifest builders.
steps/      Godog step handlers. Every handler is a thin wrapper around a
            dsl helper or CommandRunner.Run; no domain validation.
godog_test.go
            Live entry points (TestSingleClusterUp, TestSingleClusterUpOneClick,
            TestMultiClusterUp, TestSingleClusterHelmfile, TestMultiClusterHelmfile,
            TestSingleClusterEKSHelmfile, TestMultiClusterEKSHelmfile) and
            wiring tests with a fake CommandRunner.
.golangci.yml
.goheader.tmpl
go.mod / go.sum
PLAN.md     Step catalog, contracts, implementation plan.
AGENTS.md   Rules for working in this directory.
```

## Running

Wiring tests use a fake CommandRunner and do not touch a cluster. Safe in
CI; fast.

```sh
cd tests/bdd
go test -short ./...
```

Live runs build nvcf-cli, bring up a real k3d cluster, and exercise the
feature end to end. They require an NGC API key and sample registry
coordinates.

Each `-run` argument is anchored with `^...$` so the live entry point
runs without also matching its `...FeatureFileWiresToSteps` wiring
sibling. Without the anchors the wiring test also fires; it passes in
under 100ms against a fake runner and can make a misconfigured live
run look like it succeeded when nothing actually ran.

```sh
cd tests/bdd

# Single-cluster CLI feature (install --control-plane + compute-plane primitives)
go test -run '^TestSingleClusterUp$' -timeout 30m -v

# Single-cluster CLI one-click feature (nvcf-cli self-hosted up; the quickstart).
# Shares the ncp-local topology with TestSingleClusterUp; run one at a time.
go test -run '^TestSingleClusterUpOneClick$' -timeout 30m -v

# Multi-cluster CLI feature
go test -run '^TestMultiClusterUp$' -timeout 60m -v

# Single-cluster Helmfile feature (requires NGC_API_KEY, SAMPLE_NGC_ORG,
# SAMPLE_NGC_TEAM)
NGC_API_KEY=<key> SAMPLE_NGC_ORG=<org> SAMPLE_NGC_TEAM=<team> \
  go test -run '^TestSingleClusterHelmfile$' -timeout 90m -v

# Multi-cluster Helmfile feature: control-plane install on
# k3d-ncp-local-cp followed by register-cluster + install-nvca-operator
# on k3d-ncp-local-compute-1. Same secrets as the single-cluster
# Helmfile feature.
NGC_API_KEY=<key> SAMPLE_NGC_ORG=<org> SAMPLE_NGC_TEAM=<team> \
  go test -run '^TestMultiClusterHelmfile$' -timeout 90m -v
```

### EKS Helmfile features (non-local)

These run against pre-provisioned EKS clusters instead of k3d. They install
the control plane via Helmfile and provision the gateway/ELB in the feature.
They require the NGC env vars plus the EKS kube contexts.

```sh
cd tests/bdd

# Single-cluster EKS Helmfile feature: control plane + NVCA on one EKS
# cluster.
EKS_CONTEXT=<ctx> EKS_CLUSTER_NAME=<name> EKS_REGION=<region> \
  NGC_API_KEY=<key> SAMPLE_NGC_ORG=<org> SAMPLE_NGC_TEAM=<team> \
  go test -run '^TestSingleClusterEKSHelmfile$' -timeout 120m -v

# Multi-cluster EKS Helmfile feature: control plane on EKS_CONTEXT, then
# register + NVCA + function smoke on a separate compute EKS cluster
# (EKS_COMPUTE_CONTEXT). Uses HELMFILE_ENV=eks-bdd-multi to isolate its
# environment, secrets, and CLI-config files from the single-cluster EKS
# feature (eks-bdd).
EKS_CONTEXT=<cp-ctx> EKS_COMPUTE_CONTEXT=<compute-ctx> \
  EKS_COMPUTE_CLUSTER_NAME=<compute-name> EKS_REGION=<region> \
  NGC_API_KEY=<key> SAMPLE_NGC_ORG=<org> SAMPLE_NGC_TEAM=<team> \
  go test -run '^TestMultiClusterEKSHelmfile$' -timeout 150m -v
```

Pre-suite cleanup for the EKS features is operator-run (there is no
`BDD_CLEANUP_MODE` entry for EKS); see the Cleanup section.

Live runs install golangci-lint pass; the wiring tests cover handler
registration and the strict-DSL contract.

```sh
cd tests/bdd
golangci-lint run --config .golangci.yml ./...
```

## Adding a feature

1. Read `PLAN.md` so you know the catalog. Almost every scenario can be
   written entirely with existing steps.
2. Write the feature file in `features/`. Stay in the user-mimicking
   vocabulary: file ops, raw commands, output assertions.
3. Pre-load any handoff artifacts in the matching wiring test in
   `godog_test.go` so the assertion path has real bytes to compare.
4. If the catalog cannot express a scenario, prefer `When I run command`
   plus an output assertion over adding a new step. New step regexes
   should land in `PLAN.md` before any handler code.

## Adding a step

If a new step is genuinely needed:

1. Add the row to `PLAN.md` with the regex, the docstring/table shape, and
   one sentence of behavior.
2. Implement the handler in the appropriate `steps/*_steps.go` file. It
   should be a thin wrapper: interpolate inputs, snapshot the Ledger if
   writing, delegate to a `dsl/` helper or `Suite.Runner`, store the
   result on `ScenarioContext`.
3. Add at least one positive unit test in `steps/steps_test.go` driving
   the handler against a fake CommandRunner.
4. If the handler depends on a new pure helper, put that helper in `dsl/`
   with its own unit test.

## Live-run prerequisites

- Docker + k3d on `PATH`. The Make target the suite invokes does the
  cluster create.
- `kubectl` and `helm` on `PATH` for the assertion-side commands.
- `make` in the repo root so the `make -C deploy/stacks/self-managed`
  invocations resolve.
- For the Helmfile feature: `NGC_API_KEY`, `SAMPLE_NGC_ORG`,
  `SAMPLE_NGC_TEAM` env vars set.

Live runs write every command's argv, stdout, and stderr to
`tests/bdd/out/<run-id>/logs/` for post-mortem inspection.

## Cleanup between runs

Switching topologies or install methods between live runs leaves
state on the host that the next run cannot tolerate: a stale
single-cluster grabs the host ports the multi-cluster topology needs,
or a CLI-installed control plane is still on the cluster when the
Helmfile feature tries to install. The harness has two opt-in
cleanup modes plus a manual Make-target surface.

Pick one of five values for `BDD_CLEANUP_MODE`. Unknown values fail
the suite immediately.

| Mode | What it destroys | Command equivalent |
|------|------------------|--------------------|
| unset (default) | Nothing | (no cleanup) |
| `stack-single` | Stack-owned helm releases + namespaces on `k3d-ncp-local`; `out/*.yaml` artifacts | `tests/bdd/scripts/destroy-stack.sh single` |
| `stack-multi` | Stack-owned releases on every `ncp-local-compute-*` then `ncp-local-cp`; `out/*.yaml` artifacts | `tests/bdd/scripts/destroy-stack.sh multi` |
| `topology-single` | The `ncp-local` k3d cluster (and everything in it) | `make -C tools/ncp-local-cluster destroy CLUSTER_NAME=ncp-local` |
| `topology-multi` | Every `ncp-local*` k3d cluster on the host | `make -C tools/ncp-local-cluster destroy-all-ncp-local` |

Stack cleanup lives in `tests/bdd/scripts/destroy-stack.sh` so the
BDD-specific allow-lists, namespace lists, and kubectl/helm context
plumbing stay co-located with the harness that owns them. Topology
cleanup stays in `tools/ncp-local-cluster/Makefile` because k3d
cluster lifecycle is a property of the cluster-build tooling, not
the BDD suite.

Common workflows:

```sh
# Switch CLI install -> Helmfile install on the same single cluster
BDD_CLEANUP_MODE=stack-single \
  NGC_API_KEY=<key> SAMPLE_NGC_ORG=<org> SAMPLE_NGC_TEAM=<team> \
  go test -run '^TestSingleClusterHelmfile$' -timeout 90m -v

# Switch single-cluster -> multi-cluster topology
BDD_CLEANUP_MODE=topology-multi \
  go test -run '^TestMultiClusterUp$' -timeout 60m -v

# Switch CLI install -> Helmfile install on the same multi-cluster topology
BDD_CLEANUP_MODE=stack-multi \
  NGC_API_KEY=<key> SAMPLE_NGC_ORG=<org> SAMPLE_NGC_TEAM=<team> \
  go test -run '^TestMultiClusterHelmfile$' -timeout 90m -v
```

Governing rule:

- Topology cleanup may delete topology resources. The whole k3d
  cluster is gone.
- Stack cleanup may only delete stack-owned resources. For the
  local (k3d) cleanup script `tests/bdd/scripts/destroy-stack.sh`,
  topology-infrastructure releases and namespaces (`eg` and
  `envoy-gateway-system` installed by
  `tools/ncp-local-cluster/scripts/setup-gateway-api.sh`,
  `cert-manager`, the local fake GPU operator) are intentionally
  off-limits and survive every stack cleanup. `out/*.yaml` handoff
  files are removed so later `file ... should exist` assertions
  cannot pass against stale artifacts.
- For the nonlocal (EKS) cleanup script
  `tests/bdd/scripts/destroy-nonlocal-stack.sh`, the gateway
  ownership boundary is different: the EKS Helmfile feature
  installs the envoy-gateway controller, GatewayClass, and the
  nvcf-gateway Gateway resource itself in `@gateway-setup`, so
  the nonlocal script tears those back down when
  `--control-plane-context` is set. The EKS cluster itself, node
  groups, EBS CSI driver, fake GPU operator, and cert-manager are
  still off-limits because they belong to cluster provisioning,
  not to the BDD suite.

Non-local (EKS) cleanup is operator-run, not wired into `BDD_CLEANUP_MODE`.
`tests/bdd/scripts/destroy-nonlocal-stack.sh` takes one
`--control-plane-context` and one or more `--compute-context` flags, so it
covers both the single-cluster (same context for both) and multi-cluster
(distinct control-plane and compute contexts) EKS features:

```sh
# Single-cluster EKS: control plane and compute share one context
tests/bdd/scripts/destroy-nonlocal-stack.sh \
  --control-plane-context <ctx> --compute-context <ctx> --clean-out

# Multi-cluster EKS: control plane on one cluster, compute on another
tests/bdd/scripts/destroy-nonlocal-stack.sh \
  --control-plane-context <cp-ctx> --compute-context <compute-ctx> --clean-out
```

Operator notes:

- `stack-multi` and `topology-multi` require `jq` on `PATH`.
- CRDs survive every mode. If a future install schema-incompatibly
  changes a CRD, run `kubectl --context <ctx> delete crd <name>`
  manually before the next install.
- With `topology-*`, your pre-suite CLI state (admin JWT) is not
  preserved across the run. It was invalidated by the cluster
  destroy anyway.
- Do not run BDD concurrently against the same k3d host. The cleanup
  modes assume one operator at a time.
