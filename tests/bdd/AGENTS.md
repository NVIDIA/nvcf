# AGENTS.md - tests/bdd

Scope: everything under `tests/bdd/`.

This directory is the strict-DSL replacement for the legacy `tests/bdd`
runner. The whole point is a Gherkin vocabulary that an AI can extend
without inventing domain helpers. Read `PLAN.md` before touching code.

## The strict-DSL contract

Steps describe what an operator types: copy files, edit YAML, run a
shell command, assert against exit codes / file contents / JSON
output. Step handlers in `steps/` are thin wrappers around helpers in
`dsl/` and around `harness.CommandRunner`. Domain validation lives in
Gherkin via `When I run command` plus an output assertion, never inside
a handler.

If a scenario asks for behavior the catalog cannot express, the right
move is almost always another `When I run command` plus an assertion,
not a new step.

## Layering

- `harness/` owns suite lifecycle: `Config`, `CommandRunner`, `Ledger`,
  `CommandCache`, `Suite`. Step handlers depend on these; nothing else
  does.
- `dsl/` owns pure helpers: `${VAR}` interpolation, dotted-path YAML
  upsert and read, YAML subtree match/contain, kubectl manifest
  builders, JSON row matching. Every helper is unit-testable in
  isolation. No I/O coordination, no Godog dependency.
- `steps/` owns Godog step handlers and `ScenarioContext`. Each
  handler is one or two lines plus a delegate to a `dsl` helper or
  `Suite.Runner`.
- `godog_test.go` owns the live entry points and the fake-runner
  wiring tests.

A step handler that does anything beyond argv assembly, ledger
snapshot, runner invocation, and result capture is a smell. Move the
logic into `dsl/`.

## Vocabulary rules

- `${VAR}` interpolation is the only env-var form the DSL recognizes;
  a bare `$word` is left literal. Implementations must not use
  `os.ExpandEnv`. Expansion lives in `dsl.Interpolate`.
- File-mutating steps (`I copy the file`, `I update yaml file`,
  `I substitute`) snapshot the destination through `Suite.Ledger`
  before the first write. Suite teardown restores every snapshotted
  path.
- `Given command has succeeded:` keys on the fully resolved command
  text. Two scenarios whose pre-interpolation text matches but whose
  env vars differ must miss the cache. The cache lives in
  `Suite.Cache`.
- Bootstrap Givens (`a single-cluster ncp-local cluster is running`,
  `multi-cluster ncp-local compute clusters are running:`, `the ...
  image pull secret exists in namespaces:`) each wrap exactly one
  Make target or one `kubectl apply` per row. Caching is idempotent
  per suite; the underlying Make runs at most once even if multiple
  scenarios name the Given.
- Features that bring up a `tools/ncp-local-cluster` topology must
  include a conflict precheck in their Background before the
  bootstrap Given, asserting the OTHER topology is absent. Use
  `I run command "k3d cluster get <conflicting-cluster>"` followed
  by `the command exit code should be 1` (k3d v5 exits 1 on
  "not found"). Single-cluster features (CLI and Helmfile) check
  for `ncp-local-cp`; multi-cluster features check for `ncp-local`.
  The Gherkin comment above the precheck must call out the exact
  `make destroy` command an operator runs to remediate. Both
  single- and multi-cluster control-plane k3d serverlbs claim
  0.0.0.0:8080/8443/4222, so leaving the wrong topology running
  causes the bootstrap Make target to fail deep inside k3d with a
  generic port-bind error; the precheck surfaces this immediately.
- `harness.NewSuite` snapshots `~/.nvcf-cli.nvcf-cli-local.state`
  through the Ledger so the admin JWT `nvcf-cli init` writes during a
  live run is restored (or removed) at suite teardown. HOME is
  intentionally NOT isolated: k3d, kubectl, docker, and helm all
  resolve their config under `$HOME`, and pointing HOME at an empty
  per-run directory breaks the bootstrap Givens. Subsequent
  self-hosted commands read the JWT back from the state file, so the
  token never appears in argv or per-command logs. Do not introduce
  step handlers that capture secrets into env vars; relying on the
  state file keeps the JWT out of `<seq>.cmd` lines.
- Pre-suite destructive cleanup is governed by the single env var
  `BDD_CLEANUP_MODE`. Valid values: `stack-single`, `stack-multi`,
  `topology-single`, `topology-multi`, or unset. Unknown values fail
  the suite at start; the harness never silently downgrades to
  no-cleanup. The mode maps to one make target in `harness/cleanup.go`
  via `cleanupCommandFor`; that map is the single source of truth.
  Both the env-var path and the Make targets in
  `tools/ncp-local-cluster/Makefile` and
  `deploy/stacks/self-managed/Makefile` are intentionally maintained
  so an operator can clean by hand without involving `go test`.
- Cleanup belongs in `harness/cleanup.go`, never in `steps/`. Do not
  introduce a `Given the cluster is freshly destroyed` Given or
  similar; the conflict precheck inside every feature Background is
  the in-band assertion that cleanup worked.
- Cleanup runs BEFORE the CLI state-file snapshot in `NewSuite`. The
  snapshot captures the post-cleanup baseline (typically empty for
  destructive modes); teardown restores to that baseline. For
  destructive modes the operator's pre-suite JWT is intentionally
  not preserved because the cluster it pointed at is gone.
- Governing rule for cleanup: topology cleanup may delete topology
  resources, stack cleanup may only delete stack-owned resources
  and stack artifacts. Topology cleanup is implemented as Make
  targets in `tools/ncp-local-cluster/Makefile` (`destroy`,
  `destroy-all-ncp-local`) because k3d cluster lifecycle belongs to
  the cluster-build tooling. Stack cleanup is implemented as a
  bash script at `tests/bdd/scripts/destroy-stack.sh` so the
  BDD-specific allow-lists, namespace lists, and kubectl/helm
  context plumbing stay co-located with the harness that owns
  them. Stack-owned releases and namespaces are explicit
  allow-lists at the top of the script (`STACK_RELEASES_CP`,
  `STACK_RELEASES_WORKER`, `STACK_NAMESPACES_*`, `STACK_CRS_WORKER`).
  Do not introduce blanket `helm list`-based uninstall or namespace
  deletion that catches topology infrastructure (`eg` in
  `envoy-gateway-system`, the namespace itself, `cert-manager`).

## CLI vs Helmfile install paths (two intentionally distinct workflows)

The suite exercises two operator workflows that share a stack but
differ in how endpoint URLs reach the worker layer. Future changes
to either path should respect the boundary; do not introduce a
profile dependency into the Helmfile path or a values-file
dependency into the CLI path.

- CLI path (`single-cluster-up.feature`, `multi-cluster-up.feature`)
  is profile-driven. `nvcf-cli self-hosted install --control-plane`
  writes `out/control-plane-profile.yaml` with both endpoint layers
  (`inCluster` plus `computeReachable`). The follow-up
  `compute-plane register --control-plane-profile <path>
  --kube-context <ctx>` picks the right URL block based on the
  kube-context, probes JWKS, and emits a values file with the right
  URLs already baked in. The profile is the single source of truth.

- Helmfile path (`single-cluster-helmfile.feature`,
  `multi-cluster-helmfile.feature`) is values-driven. The operator
  authors `environments/<env>.yaml` carrying the URLs they want;
  `make install` runs helmfile sync; `make register-cluster`
  (older `nvcf-cli cluster register`) calls ICMS with name + nca +
  region, auto-discovers JWKS from the CURRENT kubectl context,
  and writes a values file from the ICMS response. There is no
  profile in this path. Operators are responsible for putting the
  topology-correct URLs in their environment file.

For multi-cluster Helmfile the BDD fixture
`fixtures/self-managed-local-bdd-multi.yaml` carries the
compute-reachable `.test` hostnames that
`tools/ncp-local-cluster/scripts/configure-control-plane-endpoints.sh`
aliases to the control-plane LB. The same `.test` hostnames appear
literally in `multi-cluster-up.feature`'s `controlPlane.endpoints.
computeReachable` profile assertion, so the two paths agree on the
multi-cluster URL surface. If those hostnames or ports ever change
in the alias script, three places need to stay in sync: that
script, the multi fixture's `selfManaged` block, and the CLI
feature's profile assertion. A follow-up drift-detection check is
tracked separately (see commit history).

Two recurring failure modes worth remembering when editing either
multi-cluster feature:

1. Single-cluster URLs in a multi-cluster fixture. In-cluster DNS
   (`api.sis.svc.cluster.local` etc.) only resolves inside the
   control-plane k3d cluster. The NVCA agent on a separate compute
   cluster cannot dial those addresses. Symptom: NVCA agent in
   `CrashLoopBackOff` with `dial tcp ... connect: connection
   refused` against an in-cluster hostname.

2. Wrong kubectl context when `make register-cluster` runs. The
   `nvcf-cli cluster register` command auto-discovers OIDC issuer
   and JWKS from the CURRENT context by spawning a probe Job in
   that cluster, then registers that identity with ICMS. If the
   context is the cp cluster, ICMS records the cp cluster's JWKS
   for the compute cluster's row. The compute cluster's NVCA agent
   then 401s against ICMS at runtime ("Signed JWT rejected:
   ... no matching key(s) found"). Switch the context to the
   compute cluster BEFORE `make register-cluster`, not after.

## Tests

- Every Go file under `tests/bdd/` carries the SPDX header in
  `.goheader.tmpl`. `golangci-lint` enforces this.
- Run the non-live tests before pushing:

  ```sh
  cd tests/bdd
  go test -short ./...
  golangci-lint run --config .golangci.yml ./...
  ```

  `tests/bdd` carries its own `go.mod`, so the lint invocation MUST run
  from inside `tests/bdd/`. Running `golangci-lint` against
  `./tests/bdd/...` from the repo root produces a confusing
  "no go files to analyze" or "directory prefix does not contain main
  module" error. The `tests/bdd/scripts/lint.sh` wrapper handles the
  cd internally and works from any cwd:

  ```sh
  tests/bdd/scripts/lint.sh
  ```

- Wiring tests in `godog_test.go` exercise feature files against a
  fake `CommandRunner`. They assert `status == 0` plus one substring
  check that a destructive command was issued. Do not deep-equality
  the recorder; consolidating equivalent steps in the future must not
  break these tests.
- Live entry points (`TestSingleClusterUp`, `TestMultiClusterUp`,
  `TestSingleClusterHelmfile`) skip under `-short`. They build the
  CLI and exercise real `make`/`kubectl`/`helm` against k3d.

## Style

- Plain ASCII only in committed text. No bold, no em dashes, no
  smart quotes.
- Lower-case any identifier used only inside its own package. The
  interface satisfaction Godog enforces is not a reason to export a
  step handler.
- Avoid trivial forwarding helpers. Inline anything that is a single
  expression wrapped in a one-liner.
- Comments above important exported functions should say what the
  contract is, not restate the implementation.

## Commit messages

- Conventional Commits. Common scopes: `bdd`, `dsl`, `harness`,
  `steps`.

## Adding a feature file

1. Read `PLAN.md` and confirm every step in your draft is in the
   catalog. If something cannot be expressed, prefer raw `When I run
   command` + an output assertion.
2. Write the feature file in `features/`.
3. Seed any handoff artifacts in the matching wiring test inside
   `godog_test.go`.
4. Confirm `go test -short ./...` is green.

## Adding a step

Adding a step is rare. The DSL should not grow domain-shaped
primitives.

1. Add the row to `PLAN.md` first: regex, table/docstring shape, one
   sentence of behavior.
2. Implement the handler in the matching `steps/*_steps.go`. Keep it
   thin.
3. If the handler needs a pure operation, put it in `dsl/` with a
   unit test.
4. Add a positive unit test in `steps/steps_test.go` driving the
   handler against a fake CommandRunner.

## Live-run output

Every live run writes a fresh directory under `tests/bdd/out/`:

- `out/<run-id>/logs/<seq>.{cmd,stdout,stderr}` for every command the
  runner executed.
- `out/<run-id>/originals/` is reserved for an on-disk ledger variant
  if very large fixtures ever push memory limits.
- `out/<run-id>/diagnostics/` is reserved for Kubernetes diagnostics
  collection once the integration with the existing collector lands.

Restore happens automatically at suite teardown; the working tree
should be clean after a green run.
