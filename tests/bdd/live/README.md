# Portable live BDD

The portable live suite runs the same smoke feature against an operator-selected
target. A target can be an existing local single-cluster or multi-cluster
installation, a remote self-managed cluster, or an NVCF API endpoint with no
Kubernetes coordinates.

This suite is separate from `godog_test.go`. Local install features own k3d and
Helmfile setup. Portable smoke features own product workflows after a target is
ready. Both suites reuse the strict step catalog, command runner, file ledger,
and CLI lifecycle adapters.

## Execution model

```text
load target and feature plan
          |
          v
optional provider feature, once
          |
          v
restore provider file and environment mutations
          |
          +--> smoke feature A --> compensations --> restore feature state
          |
          +--> smoke feature B --> compensations --> restore feature state
          |
          v
restore the original nvcf-cli state file
```

Each feature is a separate Godog run. The built CLI and successful bootstrap
command cache are suite-scoped. Last-command state, deferred compensation,
exported environment variables, and ledger-backed file changes cannot become
implicit inputs to the next smoke.

The optional provider phase exists for a future provisioning-only feature. It
does not make an arbitrary install scenario reusable. Existing install features
can be selected for experiments, but they still contain their current smoke
scenarios and are not the intended long-term provider contract.

## Target contract

`BDD_TARGET_FILE` selects a versioned YAML document. Target files contain only
non-secret execution coordinates and workload inputs. They cannot select
features or grant destructive consent.

```yaml
version: 1
name: example
nvcf:
  cliConfig: path/to/nvcf-cli.yaml
  cliState: ${HOME}/.nvcf-cli.example.state
compute:
  kubeconfig: ${KUBECONFIG}
  context: compute-context
  cluster: compute-cluster-name
  backendNamespace: nvca-operator
  systemNamespace: nvca-system
  region: us-west-1
workload:
  gpu: H100
  instanceType: NCP.GPU.H100_1x
  functionImage: nvcr.io/example/team/sample:tag
```

The loader rejects unknown fields and unresolved variables in the target name,
CLI config, and CLI state path. Optional compute and workload fields are
exported only when non-empty. Each feature declares the target fields it needs
with the existing environment-variable precondition step.

Credentials stay in the operator environment or in the selected CLI config and
state file. Do not commit API keys, tokens, registry credentials, or kubeconfig
contents to a target file.

## Feature selection

- `BDD_PROVIDER_FEATURE` optionally names one feature relative to
  `tests/bdd/features/`.
- `BDD_SMOKE_FEATURES` names one or more comma-separated features relative to
  `tests/bdd/features/`.
- `BDD_TARGET_FILE` names the target YAML file.

Relative target paths are resolved from the repository root, independent of the
directory where `go test` starts.

Target data never chooses executable specifications. Provider and smoke paths
are resolved under `tests/bdd/features/`; absolute paths, traversal, missing
files, and duplicate selections fail before any feature runs.

Portable smokes must be independently selectable. They must not depend on a
scenario or feature running first. Use the target contract for stable inputs
and scenario-local exported variables for identities created by the scenario.

## Safety contract

- Kubernetes commands spell out both kubeconfig and context. Portable features
  never rely on the ambient current context.
- A cluster-wide mutation uses a separate invocation-time consent variable and
  a product-level identity guard. Target data alone is not authorization.
- Compensation is registered before its matching destructive action, carries a
  visible timeout, runs in reverse order, and continues after a failure.
- `BDD_CLEANUP_MODE` is rejected. Its commands are specific to ncp-local and do
  not belong in a target-neutral suite.
- Smoke files use generic command and output assertions for uncommon product
  behavior. Shared steps hide execution mechanics only, not cluster identity,
  context, action, or product validation.

The maintenance POC requires explicit consent through
`BDD_ALLOW_CLUSTER_MAINTENANCE`. Set it to the cluster name or ID expected by
`nvcf-cli --expect-cluster-id`. It also requires `kubectl`, `jq`, and `grep` on
the machine running the suite.

## Run against an existing local target

The committed local targets interpolate `KUBECONFIG`, `SAMPLE_NGC_ORG`, and
`SAMPLE_NGC_TEAM`. Use a kubeconfig that contains the named context.

```sh
cd tests/bdd

KUBECONFIG=/path/to/kubeconfig \
SAMPLE_NGC_ORG=<org> \
SAMPLE_NGC_TEAM=<team> \
BDD_TARGET_FILE=tests/bdd/targets/local-multi.yaml \
BDD_SMOKE_FEATURES=smoke/cluster-maintenance.feature \
BDD_ALLOW_CLUSTER_MAINTENANCE=ncp-local-compute-1 \
go test ./live -run '^TestComposableLive$' -timeout 60m -v
```

Use `tests/bdd/targets/local-single.yaml` and consent to `ncp-local` for the
single-cluster topology.

For a remote target, create an untracked target file with the same schema and
point it at an explicit kubeconfig, context, CLI config, and CLI state path. An
API-only smoke can omit the entire `compute` and `workload` sections when its
feature does not require them.

## Run with a provider POC

To provision once and then execute multiple portable smokes, add one provider
feature and a comma-separated smoke list:

```sh
BDD_TARGET_FILE=/path/to/target.yaml \
BDD_PROVIDER_FEATURE=providers/local-multi.feature \
BDD_SMOKE_FEATURES=smoke/function-lifecycle.feature,smoke/cluster-maintenance.feature \
go test ./live -run '^TestComposableLive$' -timeout 120m -v
```

The provider and additional smoke paths above illustrate the contract; they are
not added by this POC. A provisioning-only provider should assert target
readiness and stop. It must not embed the portable smoke workflows.

## Local checks

```sh
cd tests/bdd
go test -short ./...
scripts/lint.sh
```
