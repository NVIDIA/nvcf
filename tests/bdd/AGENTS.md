# AGENTS.md - tests/bdd

Scope: everything under `tests/bdd/`.

This directory is the strict-DSL replacement for the legacy `tests/bdd`
runner. The whole point is a Gherkin vocabulary that an AI can extend
without inventing opaque domain helpers. Read `PLAN.md` before touching code.

## The strict-DSL contract

Steps describe what an operator types: copy files, edit YAML, run a
shell command, assert against exit codes / file contents / JSON
output. Step handlers in `steps/` are thin wrappers around helpers in
`dsl/` and around `harness.CommandRunner`. Domain validation lives in
Gherkin via `When I run command` plus an output assertion, never inside
a handler.

Use a shared step when a repeated operator action or observable keeps every
meaningful target, value, context, and timeout visible while hiding only
command or output-format mechanics. Keep `When I run command` plus an
assertion as the escape hatch for uncommon or command-specific behavior.

Function lifecycle steps are a narrow command-adapter exception. They store
only the selected `nvcf-cli` config, pass visible arguments to one CLI command,
and preserve the real command result. They do not store function identity,
apply defaults, parse or normalize product values, or enforce product
preconditions. Their `successfully` wording asserts exit code 0 to keep happy
paths compact. Use raw command steps for negative and exit-code-specific cases
so `nvcf-cli` and the NVCF API remain the product-validation boundary.

## Layering

- `harness/` owns shared process mechanics: `Config`, `CommandRunner`,
  `Ledger`, `CommandCache`, `DeferredCommands`, `Suite`, CLI state isolation,
  and one-feature Godog execution. It does not register steps or select
  features.
- `dsl/` owns pure helpers: `${VAR}` interpolation, dotted-path YAML
  upsert and read, YAML subtree match/contain, self-managed secrets
  rendering, kubectl manifest builders, JSON row matching. Every helper
  is unit-testable in isolation. No I/O coordination, no Godog dependency.
- `steps/` owns Godog step handlers and `ScenarioContext`. Each
  handler is one or two lines plus a delegate to a `dsl` helper or
  `Suite.Runner`.
- `install/` owns topology provisioning, stack installation, registration,
  install verification, ncp-local cleanup policy, and its live entry points.
- `smoke/` owns attach-mode product smokes, target loading, selection,
  invocation-time consent, isolated CLI sessions, and restoration between
  feature files.
- `fixtures/` contains inputs genuinely shared by both suites. Suite-specific
  fixtures belong below the owning suite.

Install features register `steps.RegisterInstall`. Smoke features register
`steps.RegisterSmoke`, which deliberately excludes local topology bootstrap
steps. Do not make smoke depend on install feature wiring.

A step handler that does anything beyond argv assembly, ledger
snapshot, runner invocation, and result capture is a smell. Move the
logic into `dsl/`.

## Vocabulary rules

- `${VAR}` interpolation is the only env-var form the DSL recognizes;
  a bare `$word` is left literal. Implementations must not use
  `os.ExpandEnv`. Expansion lives in `dsl.Interpolate`.
- Function lifecycle CLI option tables preserve row order, repeated options,
  empty values, and product-invalid values. Only the `option | value` table
  structure is validated before the command runs.
- Gateway API route readiness tables expose each route's kind, name,
  namespace, and intended Gateway parent plus the shared context and timeout.
  The step requires `Accepted=True` and `ResolvedRefs=True` for that parent but
  does not allowlist route kinds or duplicate Gateway API validation.
- File-mutating steps (`I copy the file`, `I update yaml file`,
  `I prepare self-managed secrets file`, `I substitute a block`)
  snapshot the destination through `Suite.Ledger` before the first write.
  Suite teardown restores every snapshotted path.
- `Given command has succeeded:` keys on the fully resolved command
  text. Two scenarios whose pre-interpolation text matches but whose
  env vars differ must miss the cache. The cache lives in
  `Suite.Cache`.
- Scenario compensation interpolates when its step runs. Export any dynamic
  identity before registering a compensation that references it. The pending
  stack is written to `out/<run-id>/pending-compensations.sh`; a failed
  recovery leaves the script in place for an operator.
- Bootstrap Givens (`a single-cluster ncp-local cluster is running`,
  `multi-cluster ncp-local compute clusters are running:`, `Helm is
  authenticated to OCI registry ...`, `the ... image pull secret exists in
  namespaces:`) each wrap exactly one Make target or one Helm invocation. The
  image pull secret Given applies one namespace manifest and one docker-registry
  secret manifest per row. Caching is idempotent per suite; the underlying
  bootstrap runs at most once even if multiple scenarios name the Given.
- The Helm OCI registry authentication Given reads `NGC_API_KEY` from the
  process environment and passes it only through
  `CommandRunner.RunWithSensitiveStdin`. The key must never be interpolated
  into command text, argv, command logs, captured output, or failure messages.
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
  overlapping host ports, including 8080, 8443, 10081, and NATS on
  4222. The multi-cluster control plane also claims 10086 for the gRPC
  worker callback path. Leaving the wrong topology running causes the
  bootstrap Make target to fail deep inside k3d with a generic port-bind
  error; the precheck surfaces this immediately.
- `harness.NewSuiteWithOptions` derives the state path from the selected CLI config
  basename and snapshots that exact file through the lifecycle Ledger. The
  smoke runner copies the source config and current state into a unique
  session first, so concurrent target runs cannot share function or task
  selection state. HOME is intentionally not isolated: k3d, kubectl, docker,
  and helm resolve their config there. Subsequent self-hosted commands read the
  JWT from the selected state file, so the token never appears in argv or
  command logs. Do not capture secrets into env vars.
- Install-suite pre-run destructive cleanup is governed by the single env var
  `BDD_CLEANUP_MODE`. Valid values: `stack-single`, `stack-multi`,
  `topology-single`, `topology-multi`, or unset. Unknown values fail
  the suite at start; the harness never silently downgrades to
  no-cleanup. The mode maps to one command in `install/cleanup.go`
  via `cleanupCommandFor`; that map is the single source of truth.
  Both the env-var path and the Make targets in
  `tools/ncp-local-cluster/Makefile` and
  `deploy/stacks/self-managed/Makefile` are intentionally maintained
  so an operator can clean by hand without involving `go test`.
- The `smoke/` suite rejects `BDD_CLEANUP_MODE`. Those cleanup
  commands are specific to ncp-local. Remote and production targets must never
  inherit local cleanup behavior.
- Cleanup policy belongs in `install/cleanup.go`, never in `harness/` or
  `steps/`. Do not
  introduce a `Given the cluster is freshly destroyed` Given or
  similar; the conflict precheck inside every feature Background is
  the in-band assertion that cleanup worked.
- Cleanup runs before the CLI state-file snapshot through the install suite's
  `BeforeStateSnapshot` callback to `NewSuiteWithOptions`. The
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
`fixtures/self-managed-local-bdd-multi.yaml` carries the same
service-DNS URL shape as the single-cluster local fixture. In the
multi-cluster ncp-local topology, those names resolve to alias
Services in the compute cluster and the alias Endpoints point at the
control-plane LB. If those hostnames or ports ever change, keep the
multi fixture's `selfManaged` block, the local stack values, and the CLI
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

- Wiring tests in `install/install_test.go` exercise install features against a
  fake `CommandRunner`. They assert `status == 0` plus one substring
  check that a destructive command was issued. Do not deep-equality
  the recorder; consolidating equivalent steps in the future must not
  break these tests.
- Smoke wiring tests live in `smoke/` and recursively match every step in
  `smoke/features/**/*.feature` against the smoke catalog. They do not fake
  product responses. Smoke features run one file per Godog phase. The
  runner resets the command cache and restores CLI state, step-exported
  environment values, and ledger-backed files between phases.
- Live entry points (`TestSingleClusterUp`, `TestMultiClusterUp`,
  `TestSingleClusterHelmfile`) skip under `-short`. They build the
  CLI and exercise real `make`/`kubectl`/`helm` against k3d.

## Smoke features

- Target YAML is versioned, non-secret data. Its strict top-level shape carries
  a name and an open `env` map. It must not select features, set runner-owned
  values, or contain destructive consent.
- Developers select safe top-level features directly with repeatable
  `-bdd-feature` flags. Direct selection cannot reach
  `smoke/features/operations/`.
- Plan YAML is versioned, committed selection for nightly collections and
  reviewed operational workflows. Each phase selects one `feature` or a
  `features` glob. Globs expand in stable order and every file executes as an
  independently restored run. Do not rely on Godog file ordering or encode
  selections in comma-separated process environment values.
- A phase marked `mutatesTopology: true` must declare an invocation-time
  consent comparison. Validate every phase's consent against target data before
  building the CLI or executing a feature.
- Every smoke is independently selectable. It declares required
  target fields through environment preconditions and does not depend on an
  earlier feature's exported variables or files.
- Every Kubernetes command exposes both kubeconfig and context in Gherkin.
  Ambient current context is not portable and is unsafe for remote targets.
- Cluster-wide mutations require separate invocation-time consent and use the
  product's expected-cluster identity guard before mutation.
- Register compensation before its destructive command. Keep the action,
  target, and timeout visible. Compensation handlers hide only reverse-order
  execution and continue-after-failure mechanics.
- Do not add capability auto-detection, target-based feature skipping, or
  product validation to the runner. Missing inputs fail the feature's visible
  preconditions; unsupported behavior fails through the real CLI or API.

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
2. Put install workflows in `install/features/`. Put safe attach-mode product
   smokes in `smoke/features/`; put reviewed operational workflows in
   `smoke/features/operations/`.
3. Seed install handoff artifacts in the matching wiring test inside
   `install/install_test.go`. Smoke wiring is discovered automatically and
   should not require a Go edit.
4. Confirm `go test -short ./...` is green.

## Adding a step

Adding a step is deliberate. Add one only for a repeated action or observable
that keeps meaningful inputs visible and has no hidden workflow branching.
Do not add opaque composite steps such as `Given the stack is installed`.

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
