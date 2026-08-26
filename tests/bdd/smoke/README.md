# Smoke BDD

The smoke suite runs selected product features against an operator-selected
target. The target can be an existing local installation, a remote
self-managed cluster, or an NVCF API endpoint without Kubernetes coordinates.

This suite is separate from `install/`. Install features provision and verify
topology. Smoke features exercise product behavior after a target is ready.
Both suites reuse the strict step catalog and harness mechanics.

## Execution model

```text
load target and direct features or committed plan
          |
          v
validate every phase and consent requirement
          |
          v
create an isolated nvcf-cli config and state session
          |
          +--> ordered phase A --> compensation --> restore feature and CLI state
          |
          +--> ordered phase B --> compensation --> restore feature state
          |
          v
remove the isolated nvcf-cli state file
```

Each phase is a separate Godog run. The built CLI and isolated authentication
baseline are suite-scoped. Mutable CLI selection state, the command cache,
last-command state, exported environment variables, and ledger-backed files
are feature-scoped.

Pending compensation is written to
`tests/bdd/out/<run-id>/pending-compensations.sh` before the matching mutation
runs. Successful compensation removes the file. A failed compensation leaves
the remaining recovery commands for the operator.
Run a retained script from the repository root with `sh <path>`.

## Target contract

`-bdd-target` selects a versioned YAML document. The top-level schema is strict.
The `env` map is intentionally open so a new smoke input does not require a Go
schema change.

```yaml
version: 1
name: example
env:
  BDD_NVCF_CLI_CONFIG: path/to/nvcf-cli.yaml
  BDD_COMPUTE_KUBECONFIG: ${KUBECONFIG}
  BDD_COMPUTE_CONTEXT: compute-context
  BDD_COMPUTE_CLUSTER: compute-cluster-name
  BDD_NVCA_BACKEND_NAMESPACE: nvca-operator
  BDD_NVCA_SYSTEM_NAMESPACE: nvca-system
  BDD_COMPUTE_REGION: us-west-1
  BDD_WORKLOAD_GPU: H100
  BDD_WORKLOAD_INSTANCE_TYPE: NCP.GPU.H100_1x
  BDD_FUNCTION_IMAGE: nvcr.io/example/team/sample:tag
```

The loader interpolates and exports every `env` entry. Each feature declares
the entries it requires with an environment-variable precondition. Empty or
product-invalid feature inputs fail through visible Gherkin and the real CLI or
API, not through the target loader.

`BDD_NVCF_CLI_CONFIG` is the only required runner input. The harness derives
the CLI state path from the config basename, then copies the config and current
state into a unique run session. Each phase starts from that copied baseline.
Concurrent targets do not mutate the same CLI selection state. The source
config and state remain unchanged.

Target files cannot set runner-owned values, cleanup mode, or destructive
consent. Do not commit credentials, tokens, registry keys, or kubeconfig
contents.

## Selection contract

Developers select one or more safe smokes directly with repeatable
`-bdd-feature` flags. Direct paths are relative to
`tests/bdd/smoke/features/` and may select only top-level feature files.

Nightly CI and reviewed operational workflows use `-bdd-plan`. A plan is a
committed, versioned list of independently executed phases. It is separate from
target data, so selecting a target cannot select executable specifications.

```yaml
version: 1
name: cluster-maintenance
phases:
  - name: cluster-maintenance
    feature: operations/cluster-maintenance.feature
    tags: "@smoke"
    mutatesTopology: false
    consent:
      environment: BDD_ALLOW_CLUSTER_MAINTENANCE
      equalsTargetEnvironment: BDD_COMPUTE_CLUSTER
```

Each phase selects exactly one `feature` or one `features` glob. A collection
glob expands in stable path order and each match runs as a separately restored
feature. Paths are relative to `tests/bdd/smoke/features/`. Absolute paths,
traversal, missing files, duplicate phases, duplicate features, and unknown plan
fields fail before execution. A phase marked `mutatesTopology: true` must
declare an invocation-time consent comparison.

Safe nightly collection example:

```yaml
version: 1
name: nightly
phases:
  - name: product-smokes
    features: "*.feature"
    tags: "~@skip"
    mutatesTopology: false
```

Smokes must remain independently selectable. A phase cannot use an
exported variable, file mutation, or successful-command cache entry from an
earlier phase as an undocumented provider.

## Safety contract

- Kubernetes commands spell out kubeconfig and context.
- Target data cannot grant destructive consent.
- The runner compares invocation-time consent with the selected target before
  it builds the CLI or runs any phase.
- Product mutation commands also use the product's expected-identity guard.
- Compensation is registered before its matching mutation, runs in reverse
  order, and has a visible timeout.
- Compensation commands interpolate when they are registered. Export any
  scenario-created identity before the compensation step that references it.
- `BDD_CLEANUP_MODE` is rejected because its commands are specific to local
  installation tests.

The maintenance plan requires `BDD_ALLOW_CLUSTER_MAINTENANCE` to equal the
target's `BDD_COMPUTE_CLUSTER`. It also requires `kubectl`, `jq`, and `grep` on
the machine running the suite.

## Run against an existing local target

The committed local targets interpolate `KUBECONFIG`, `SAMPLE_NGC_ORG`, and
`SAMPLE_NGC_TEAM`.

```sh
cd tests/bdd

KUBECONFIG=/path/to/kubeconfig \
SAMPLE_NGC_ORG=<org> \
SAMPLE_NGC_TEAM=<team> \
BDD_ALLOW_CLUSTER_MAINTENANCE=ncp-local-compute-1 \
go test ./smoke -run '^TestLive$' -timeout 60m -v -args \
  -bdd-target tests/bdd/smoke/targets/local-multi.yaml \
  -bdd-plan tests/bdd/smoke/plans/cluster-maintenance.yaml
```

Use `tests/bdd/smoke/targets/local-single.yaml` and consent to `ncp-local` for
the single-cluster topology.

For a local one-off, repeat `-bdd-feature` for the safe smokes to run. The
function and task files below are the planned lifecycle smoke names:

```sh
cd tests/bdd

go test ./smoke -run '^TestLive$' -timeout 60m -v -args \
  -bdd-target tests/bdd/smoke/targets/local-multi.yaml \
  -bdd-feature function-lifecycle.feature \
  -bdd-feature task-lifecycle.feature
```

For a remote target, create an untracked target file with the same schema and
use explicit CLI and Kubernetes coordinates. An API-only target can omit
Kubernetes and workload inputs when its selected features do not require them.

## Provisioning phases

Plans can represent ordered provisioning phases, but this POC does not commit
one. Existing local install features rely on `BDD_CLEANUP_MODE`, which the
smoke runner rejects. Provision those targets through the install suite, then
attach with a smoke selection.

A future provisioning-only phase must declare `mutatesTopology: true`, include
invocation-time consent, establish readiness, and stop before product smoke.

## Local checks

```sh
cd tests/bdd
go test -short ./...
scripts/lint.sh
```
