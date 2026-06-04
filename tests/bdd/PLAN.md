# Strict Infrastructure-Level Gherkin DSL

This is the complete set of step definitions for `tests/bdd`. Every
new step must reuse one of these patterns. Step handlers do not contain
domain-specific validation logic; that lives in Gherkin via CLI invocations
and output assertions.

## Vocabulary

The vocabulary is restricted to four categories:

1. File operations: copy, edit YAML, substitute strings.
2. Environment preconditions: env vars set, files exist, infrastructure
   reachable.
3. Command execution: exec a shell command and capture exit code, stdout,
   stderr.
4. Output assertions: exit code, substring presence/absence, YAML key
   read-back, JSON row matching.

## Interpolation rule

The DSL recognizes only the explicit `${VAR}` form for environment
variable expansion. A bare `$word` is treated as a literal string. This
matters for credential rendering: `"$oauthtoken:${NGC_API_KEY}"` keeps
the literal `$oauthtoken` Docker username and only expands `${NGC_API_KEY}`.

Implementations must not use `os.ExpandEnv` directly; expansion is via a
regex restricted to `\$\{[A-Z0-9_]+\}`.

## Steps

### Environment + file preconditions (Given)

| Step | Notes |
|------|-------|
| `Given environment variable {string} is set` | Fails if the env var is empty. Used at the top of any scenario that interpolates it later. |
| `Given file {string} exists` | Bare-file precondition. Same semantics as `Then file {string} should exist`; use `Given` form for narrative preconditions. |

### Infrastructure bootstrap (Given)

These are the only Givens that hide a CLI invocation, and each one wraps
exactly one Make target or one composite of `kubectl` calls. They contain
no business logic. Spelling them out in raw `make` calls would balloon
the feature file without adding coverage.

The bootstrap Givens are idempotent and cached per suite. The first
scenario that names the Given runs the underlying Make target; later
scenarios that name the same Given no-op. Each scenario that needs the
cluster must still declare the Given explicitly; caching is an
implementation detail, not a license to omit the precondition.

| Step | Wraps |
|------|-------|
| `Given a single-cluster ncp-local cluster is running` | `make -C tools/ncp-local-cluster build-and-deploy-cluster`. Runs once per suite. |
| `Given multi-cluster ncp-local compute clusters are running:` (table, see below) | Wraps `make -C tools/ncp-local-cluster build-and-deploy-multicluster COMPUTE_CLUSTER_COUNT=N`. Runs once per suite. |
| `Given the {string} image pull secret exists in namespaces:` (table) | `kubectl create namespace <ns>` + `kubectl create secret docker-registry` for each row. Hidden because the docker-registry secret syntax leaks the API key to argv. |

Compute-cluster table contract for the multi-cluster bootstrap Given:

- The table is a single-column list of compute cluster names. Each row
  is one compute cluster.
- The total row count is passed verbatim as `COMPUTE_CLUSTER_COUNT` to
  the Makefile target. `COMPUTE_CLUSTER_COUNT` counts compute clusters
  only; it never includes the control plane cluster.
- The control plane cluster is named `ncp-local-cp` by the Makefile.
  It is created automatically alongside the compute clusters. Do not
  put `ncp-local-cp` in the table; the handler rejects the row with an
  error so a misread is loud rather than silent.
- The step handler also validates that every row matches the
  `ncp-local-compute-<N>` shape so a typo cannot quietly spin up a
  differently-named cluster.

Example with one compute cluster:

```gherkin
Given multi-cluster ncp-local compute clusters are running:
  | ncp-local-compute-1 |
```

This runs `make -C tools/ncp-local-cluster build-and-deploy-multicluster COMPUTE_CLUSTER_COUNT=1`,
which produces the control plane `ncp-local-cp` plus the listed
compute cluster `ncp-local-compute-1`.

### Cross-scenario carry-over (Given)

The DSL exposes one generic primitive for reusing prior work, not a set
of domain-named aliases. The cache key is the fully resolved command
text after `${VAR}` expansion, so two scenarios whose pre-interpolation
text happens to be identical but whose env var values differ will
correctly miss the cache.

| Step | Behavior |
|------|----------|
| `Given command has succeeded:` (docstring) | The handler expands `${VAR}` in the docstring, hashes the resolved text, and looks it up in `CommandCache`. On hit: no-op. On miss: execute the resolved command and assert exit code 0; on success, record the resolved text. |

If a scenario needs multiple prior commands to have succeeded, repeat
the step. Refactoring the command in scenario 1 forces an identical
refactor in every consumer; that is a feature.

### File operations (Given / And)

| Step | Notes |
|------|-------|
| `And I copy the file {string} to {string}` | Both paths are repo-relative. |
| `And I update yaml file {string} with keys:` (two-column table of dotted-path and value) | Path supports dotted notation and `[n]` indices (e.g. `global.imagePullSecrets[0].name`). Missing intermediate maps and missing list indices are upserted: writing `global.imagePullSecrets[0].name` against a file that has neither `global.imagePullSecrets` nor any list entry creates both. Existing scalars at intermediate positions cause the step to fail rather than silently overwrite a non-map. Value cells expand `${VAR}` from `os.Environ`. |
| `And I substitute {string} in file {string} with base64 of {string}` | Used for credential rendering; the third arg expands `${VAR}` then base64-encodes. The handler never logs the substituted value. |

### Command execution (When)

| Step | Notes |
|------|-------|
| `When I run command {string}` | Single-line command. `${VAR}` expansion applies. Captures exit code + stdout + stderr in scenario state. The most recent run is the one assertions reference. |
| `When I run command:` (docstring) | Multi-line form for commands that don't fit on one line. Same recording semantics. |
| `When I run command with a terminal:` (docstring) | Same as the docstring form, but stdin is attached to a pseudo-terminal so the child sees a TTY on fd 0. For commands that gate interactive-only behavior on a TTY, such as `nvcf-cli self-hosted up` (its auth-gate mints the admin token only when stdin is a terminal). No input is written; stdout and stderr are captured separately as usual. |
| `When I export command output to environment variable {string}` | Exports the previous command's trimmed stdout under the named env var. Fails the step unless the prior command exited 0 and produced non-empty stdout. Snapshotted by the env Ledger; restored at suite teardown. |

### Assertions (Then / And)

| Step | Notes |
|------|-------|
| `Then the command exit code should be {int}` | Last-run exit code. |
| `Then the command output should contain {string}` | Substring match on combined stdout + stderr. |
| `Then the command output should not contain {string}` | Negative substring match. |
| `Then file {string} should exist` | |
| `Then yaml file {string} key {string} should equal {string}` | Reads the YAML file, walks the dotted key path, compares to the value (with `${VAR}` expansion). |
| `Then yaml file {string} key {string} should not be empty` | Same key resolution; passes if the resolved value is non-empty. Use for non-deterministic outputs (cluster IDs, identity sources) where exact-value assertions are wrong. |
| `Then yaml file {string} should match:` (docstring) | Parses the docstring as YAML, parses the file as YAML, and asserts strict equality of the two trees. Every key in expected must exist in actual and vice versa. `${VAR}` expansion applies to the docstring before parsing. See "YAML comparison semantics" below for tree rules. |
| `Then yaml file {string} key {string} should match:` (docstring) | Same as above but compares only the subtree at the dotted key path. |
| `Then yaml file {string} should contain:` (docstring) | Subset variant: every key in expected must exist in actual with the same value; extra keys in actual are allowed. Use this when the file has dynamic or future-additive fields. `${VAR}` expansion applies. |
| `Then yaml file {string} key {string} should contain:` (docstring) | Subset semantics scoped to the subtree at the dotted key path. |
| `Then the json output should contain rows:` (table) | Parses the last command's stdout as JSON (expected: array of objects). For each table row, asserts an object matching every column value exists in the array. Extra objects are allowed; ordering is not asserted. |

#### YAML comparison semantics

Both `should match` and `should contain` walk the parsed YAML trees with
these rules:

- Maps are unordered: comparison ignores key insertion order.
- Lists are ordered: comparison is positional and item-wise.
- For `should match`, list lengths must be equal. For `should contain`,
  list lengths must still be equal and items still compared in order;
  subset semantics apply only inside maps. (YAML lists carry positional
  meaning, so set-style subset would silently accept reorderings.)
- Type mismatch (expected map vs actual scalar, etc.) is always a
  failure regardless of mode.
- Missing key paths fail with a "path not present" error rather than a
  comparison error.

## Harness-exported env vars

The runner sets these before any scenario runs. Feature files interpolate
them via `${VAR}` in command strings and table cells.

| Var | Set by | Used for |
|-----|--------|----------|
| `NVCF_CLI` | `go build` of `src/clis/nvcf-cli` at suite start | Absolute path to the freshly built CLI binary. |
| `REPO_ROOT` | `git rev-parse --show-toplevel` at suite start | Absolute path to the repo root. Required when invoking `make -C deploy/stacks/self-managed` because the Makefile's `-C` changes cwd; relative paths to fixtures from there break. |
| `NGC_API_KEY` / `SAMPLE_NGC_ORG` / `SAMPLE_NGC_TEAM` | The operator's shell | Passed through unchanged. The `Given environment variable {string} is set` step asserts they are non-empty before any scenario uses them. |

Feature files may also export their own env vars at runtime via
`When I export command output to environment variable {string}`. Those
are snapshotted by the env Ledger and restored at teardown so they do
not leak into later test binaries in the same `go test` invocation.
The EKS Helmfile feature exports `EKS_GATEWAY_ADDR` this way from
`kubectl get gateway` after the Gateway resource is Programmed.

## CLI argv

Feature files invoke `nvcf-cli` via `${NVCF_CLI}` interpolation in
`When I run command`. The harness does not prepend any flags; every flag
is spelled out in Gherkin.

Reference argv shapes used by the CLI features (matches the current CLI
contract verified in `src/clis/nvcf-cli/cmd/`):

- `self-hosted up`:
  ```
  ${NVCF_CLI} --config <cfg> self-hosted --stack deploy/stacks/self-managed --env local --plain up --cluster-name <name> --region us-west-1 --nca-id nvcf-default
  ```
- `self-hosted install --control-plane` (multi-cluster):
  ```
  ${NVCF_CLI} --config <cfg> self-hosted --stack deploy/stacks/self-managed --env local --plain --control-plane-context k3d-<cp> --compute-plane-context k3d-<compute> install --control-plane --cluster-name <cp> --region us-west-1 --nca-id nvcf-default
  ```
- `self-hosted control-plane profile validate`:
  ```
  ${NVCF_CLI} --config <cfg> self-hosted --stack deploy/stacks/self-managed --env local --plain control-plane profile validate --file <profile-path> --require in-cluster
  ```
- `self-hosted compute-plane register`:
  ```
  ${NVCF_CLI} --config <cfg> self-hosted --stack deploy/stacks/self-managed --env local --plain compute-plane register --control-plane-profile <profile-path> --cluster-name <compute> --kube-context k3d-<compute> --region us-west-1 --output <values-path>
  ```
- `self-hosted compute-plane install`:
  ```
  ${NVCF_CLI} --config <cfg> self-hosted --stack deploy/stacks/self-managed --env local --plain compute-plane install --values <values-path> --kube-context k3d-<compute> --cluster-name <compute>
  ```

## File restoration

Every step that writes into a path under the repo working tree
(`I copy the file ... to ...`, `I update yaml file ...`,
`I substitute ... in file ...`) registers that path with the runner's
restoration ledger:

- Before the first write, the runner snapshots the file (exists/not,
  body, mode) in memory. Repeat writes against the same path during a
  suite do not overwrite the snapshot; only the first write records.
- At suite teardown, the runner restores every registered path to its
  pre-suite state. Files that did not exist before are deleted; files
  that did are rewritten with the original bytes and mode.
- `Config.LedgerDir` (`out/<run-id>/originals/`) is reserved for an
  on-disk variant if very large fixtures ever push memory limits.
  Today the directory is created but unused.

This guarantee is in the runner, not in Gherkin. Feature files do not
need a "restore" step; the contract is "any path you touch via the DSL
is yours for the duration of the suite, and the runner cleans up".

Files written via `When I run command` (e.g. `make install` producing
`deploy/stacks/self-managed/out/<env>-register-values.yaml`) are not
covered by automatic restoration. The runner cannot tell which side
effects a shell command intended; if a scenario depends on cleaning up
such files it must do so via another `When I run command "rm ..."`.

## Why this list

The promise of the DSL is that any future feature file can be written
using only these steps. If a new scenario needs something the catalog
doesn't cover, the right move is almost always to express it as a
`When I run command` + an output assertion, not to add a new step.

The infrastructure-bootstrap Givens (cluster up, image pull secret) and
the single `Given command has succeeded:` carry-over are the only places
where the strict DSL bends to share state across scenarios. Each one's
hidden work is one CLI invocation, visible by either the wrapped Make
target or the docstring command text.

## What this displaces from tests/bdd

Going away in `tests/bdd`:

- The `operator` package's domain methods (`ApplyLocalNCPLocalDefaults`,
  `SetImagePullSecret`, `SetNGCOrgAndTeamFromEnv`,
  `ConfigureLocalEnvoyGateway`). Replaced by `I update yaml file ... with
  keys:` against the dotted paths those methods wrote.
- The `stack` package's Helmfile method wrappers (`Install`, `Destroy`,
  `Template`, `HelmfileTemplate`, `RegisterCluster`,
  `InstallNvcaOperator`). Replaced by `When I run command "make ..."`.
- The Helm release readback methods (`HelmReleaseDeployed`,
  `NVCAOperatorReady`, `NVCAAgentReady`). Replaced by `helm list -o json`
  + `kubectl rollout status` / `kubectl wait` directly in Gherkin.
- The `harness.CLIHarness` interface and its five domain methods
  (`SelfHostedUp`, `SelfHostedInstallControlPlane`,
  `SelfHostedComputePlaneRegister`, `SelfHostedComputePlaneInstall`,
  `ControlPlaneProfileValidate`). Replaced by `When I run command
  "${NVCF_CLI} ..."` with the full CLI argv spelled out.
- The `harness.HandoffArtifactPath` / typed `ControlPlaneProfile` and
  `NVCAValues` Go structs. Replaced by `yaml file ... key ... should
  equal ...` against the same fields.
- The `LocalConfig` struct and `localConfigFromEnv` helper. Replaced by
  `${VAR}` expansion in table cells.
- The `ImagePullSecretConfigured` / "references the pull secret"
  assertion (already removed in the prior MR).
- The fine-grained feature-state caching for individual scenarios
  (`HelmfileEnvironmentPrepared`, `restoreLocalHelmfileEnvironmentPrepared`,
  `restoreLocalStackInstalled`, `restoreHelmfileStackInstalled`).
  Replaced by `Given command has succeeded:` keyed on the exact command
  text.

## What stays from tests/bdd

- `harness.Config` for resolving repo root and feature artifact paths.
- The command-execution backend with logging and Kubernetes diagnostics
  collection; the `When I run command` step reuses this.
- The CLI build step that produces `NVCF_CLI`.
- The fixture file `fixtures/self-managed-local-bdd.yaml` as a starting
  point for the Helmfile feature, and `fixtures/nvcf-cli-local.yaml` for
  the CLI features.

## Implementation plan

The implementation is split into four code phases plus an optional
cutover, designed so each phase is independently reviewable. Phase 1
ships the foundation with no Godog integration; phase 2 builds step
handlers against fake collaborators; phase 3 wires the first feature
end-to-end; phase 4 wires the remaining features. Phase 5 retires the
old `tests/bdd` tree.

### Package layout

```
tests/bdd/
  features/                      (Gherkin, already committed)
  fixtures/                      (sample env + CLI config, already committed)
  STEPS.md                       (this document)

  harness/                       (phase 1)
    config.go                    (paths, env exports)
    runner.go                    (CommandRunner around infra.Runner)
    ledger.go                    (file restoration ledger)
    cache.go                     (command-success cache)
    suite.go                     (lifecycle: build CLI, set env, teardown)

  dsl/                           (phase 1)
    interp.go                    (${VAR} regex interpolation)
    yamledit.go                  (dotted-path upserts, reads)
    jsoncmp.go                   (json output row matching)

  steps/                         (phase 2)
    context.go                   (ScenarioContext + RegisterAll)
    file_steps.go                (I copy / I update yaml / I substitute)
    command_steps.go             (I run command / command has succeeded)
    assertion_steps.go           (exit code, output contains, file/yaml/json)
    infra_steps.go               (cluster bootstrap, image pull secret)

  godog_test.go                  (phase 3 and 4: TestSingleClusterUp,
                                  TestMultiClusterUp, TestSingleClusterHelmfile,
                                  wiring tests with fakes)
```

The `tests/bdd/harness` and `tests/bdd/dsl` packages are new
files. The runner inside `harness/runner.go` delegates to the existing
`tests/bdd/infra.LoggingRunner` so the command-log + diagnostics paths
do not get reinvented.

### Contracts

All exported types and signatures the rest of the package depends on.
Anything not listed here is package-internal.

#### harness package

```go
// Config carries every path and env var the suite needs. Resolved once
// at suite start.
type Config struct {
    RepoRoot       string  // absolute, from git rev-parse --show-toplevel
    CLIPath        string  // absolute, where the suite built nvcf-cli
    OutDir         string  // tests/bdd/out/<run-id>/
    LedgerDir      string  // OutDir/originals/
    CommandLogDir  string  // OutDir/logs/
    DiagnosticsDir string  // OutDir/diagnostics/
}

func ResolveConfig() (Config, error)

// Result is what a single command execution produced.
type Result struct {
    ExitCode int
    Stdout   string
    Stderr   string
}

// CommandRunner adapts the harness-shaped string commands to the
// infra.Runner argv shape, captures Result fields, and routes logging
// through the existing LoggingRunner.
type CommandRunner interface {
    Run(ctx context.Context, commandText string) (Result, error)
}

func NewCommandRunner(inner infra.Runner, cwd string) CommandRunner

// Ledger snapshots files before first write and restores at suite
// teardown. Safe to call Snapshot multiple times on the same path;
// only the first call records state.
type Ledger struct{ /* unexported */ }

func NewLedger(snapshotsDir string) *Ledger
func (l *Ledger) Snapshot(path string) error
func (l *Ledger) RestoreAll() error

// CommandCache records successful command runs keyed by the
// pre-interpolation command text. Used by Given command has succeeded.
type CommandCache struct{ /* unexported */ }

func NewCommandCache() *CommandCache
func (c *CommandCache) Record(commandText string)
func (c *CommandCache) Has(commandText string) bool

// Suite is the top-level lifecycle owner. Built once per go test
// invocation; runs one or more feature files.
type Suite struct {
    Config Config
    Runner CommandRunner
    Ledger *Ledger
    Cache  *CommandCache
}

func NewSuite(t *testing.T) (*Suite, error) // builds nvcf-cli, sets NVCF_CLI + REPO_ROOT
func (s *Suite) Teardown() error            // calls Ledger.RestoreAll
```

#### dsl package

Pure helpers, no I/O coordination, no godog deps. Each is unit-testable
in isolation.

```go
// Interpolate replaces ${NAME} sequences with os.Getenv(NAME). Bare
// $word is left literal. Missing env vars expand to the empty string.
func Interpolate(s string) string

// UpdateYAMLKeys reads the file, applies each key,value pair as a
// dotted-path upsert, and writes the file back. Path syntax:
// "a.b.c" walks maps; "[n]" indexes lists. Missing intermediate maps
// and missing list indices are created. Encountering a scalar where a
// map or list is expected is a hard error.
func UpdateYAMLKeys(path string, keys [][2]string) error

// ReadYAMLKey resolves a dotted path against the YAML at path.
// (value, true, nil) on found; (empty, false, nil) on not found;
// non-nil error only for IO or parse failures.
func ReadYAMLKey(path, dottedKey string) (string, bool, error)

// MatchMode selects between strict subtree equality and subset
// matching for MatchYAMLSubtree. Map comparison ignores key order in
// both modes. List comparison is order-sensitive and length-strict in
// both modes (YAML lists are ordered; a set-style subset would
// silently accept reorderings, which is the wrong default for our use
// case).
type MatchMode int

const (
    MatchExact  MatchMode = iota // every key in expected and actual must match
    MatchSubset                  // every key in expected must exist in actual; extras allowed
)

// MatchYAMLSubtree compares an expected YAML docstring to the subtree
// at keyPath inside the file at filePath. Empty keyPath compares
// against the whole file. The docstring runs through ${VAR}
// interpolation before YAML parsing. On mismatch the returned error
// names the first path that differed.
func MatchYAMLSubtree(filePath, keyPath, expectedYAML string, mode MatchMode) error

// SubstituteFile replaces every occurrence of placeholder with
// replacement in the named file. Used for credential rendering.
// Never logs placeholder or replacement.
func SubstituteFile(path, placeholder, replacement string) error

// JSONContainsRows parses raw as a JSON array of objects, and for each
// row map asserts that an object matching every (key, value) pair
// exists in the array. Extra objects in the array are fine.
func JSONContainsRows(raw string, rows []map[string]string) error
```

#### steps package

```go
// ScenarioContext is the per-scenario state godog hands to each
// handler. The collaborators are shared across scenarios in the same
// suite via the same Suite pointer; LastResult / LastErr are reset
// per scenario.
type ScenarioContext struct {
    Suite      *harness.Suite
    LastResult harness.Result
    LastErr    error
}

func NewScenarioContext(suite *harness.Suite) *ScenarioContext

// RegisterAll wires every step from every category to the godog
// ScenarioContext. Test code calls this once per ScenarioInitializer.
func RegisterAll(ctx *godog.ScenarioContext, sc *ScenarioContext)
```

Each `*_steps.go` file holds a small registrar (`registerFileSteps`,
`registerCommandSteps`, etc.) plus the handler methods on
`*ScenarioContext`. Handler signatures match Godog's expected
`func(ctx context.Context, ...) error` shape.

### Phase 1 (MR 1): foundation, no Godog

Files:
- `tests/bdd/harness/config.go`
- `tests/bdd/harness/runner.go`
- `tests/bdd/harness/ledger.go`
- `tests/bdd/harness/cache.go`
- `tests/bdd/harness/suite.go`
- `tests/bdd/dsl/interp.go`
- `tests/bdd/dsl/yamledit.go`
- `tests/bdd/dsl/jsoncmp.go`
- Unit tests next to each source file.

Acceptance:
- `go test ./tests/bdd/harness ./tests/bdd/dsl` passes.
- Ledger snapshot/restore roundtrip is covered including the
  did-not-exist-becomes-deleted case.
- YAML upsert tests cover map-creation, list-creation, scalar-in-the-way
  error, and `${VAR}` expansion in value cells.
- Interpolator tests cover braced expansion only and verify `$word`
  literally survives.
- No godog dependency yet.

### Phase 2 (MR 2): step handlers

Files:
- `tests/bdd/steps/context.go`
- `tests/bdd/steps/file_steps.go`
- `tests/bdd/steps/command_steps.go`
- `tests/bdd/steps/assertion_steps.go`
- `tests/bdd/steps/infra_steps.go`
- Unit tests in `steps/steps_test.go` that drive each handler with a
  fake CommandRunner and a real Ledger backed by a t.TempDir.

Acceptance:
- `go test ./tests/bdd/steps` passes.
- Each handler validates argument shape and propagates results into
  ScenarioContext fields. No domain logic; everything routes through
  dsl helpers or the runner.
- `Given command has succeeded:` consults `CommandCache` before
  invoking the runner; the test asserts a second call with the same
  text does not call the runner.

### Phase 3 (MR 3): suite entry points and first feature

Files:
- `tests/bdd/godog_test.go` adds `TestSingleClusterUp` plus
  `TestSingleClusterUpFeatureFileWiresToSteps`.
- Wiring test uses a fake CommandRunner that returns canned
  exit-code-0 results so every step resolves.

Acceptance:
- `go test ./tests/bdd -run TestSingleClusterUpFeatureFileWiresToSteps`
  passes.
- The handler chain resolves every step in
  `features/single-cluster-up.feature` against the registered patterns.
- `TestSingleClusterUp` is the live entry point; not run in CI but
  documented in AGENTS.md alongside the existing live invocations.

### Phase 4 (MR 4): remaining features wired

Files:
- `tests/bdd/godog_test.go` gains `TestMultiClusterUp` and
  `TestSingleClusterHelmfile`, plus their wiring tests.

Acceptance:
- Both new wiring tests pass against the same fake CommandRunner shape.
- Live run of either feature is exercisable; the documented argv path
  matches what `harness/cli.go` produced in the old suite (verified by
  reading the recorded command log).

### Phase 5 (MR 5, optional, gated on live verification)

Files:
- Delete `tests/bdd/operator/`, `tests/bdd/stack/`, the now-unused
  `tests/bdd/steps/*.go` handlers, and the feature files that have a
  `bdd` counterpart.
- Move `tests/bdd/*` to `tests/bdd/*`.
- Update `tests/bdd/AGENTS.md` and any `.gitlab-ci.yml` references.

Acceptance:
- Live `make` invocations in the project root that reference the BDD
  suite still resolve.
- One green live run of each feature on the contributor's k3d.

### Testing strategy by phase

Each phase ships its tests at the same level as its code:

| Phase | What gets tested |
|-------|------------------|
| 1 | Pure helpers (dsl) with table-driven Go tests. Ledger / Cache integration tests with t.TempDir backing the snapshots. |
| 2 | Step handlers, one per file, driven by a fake CommandRunner. Verifies argv shape and ScenarioContext side effects, not call counts on a recorder. |
| 3 | Single-cluster-up feature end-to-end with fakes. The wiring test follows the same shape as the existing `TestSingleClusterFeatureFileWiresToSteps` in tests/bdd. |
| 4 | Same shape for multi-cluster and helmfile features. |
| 5 | Live run of each feature once before deletion. Manual gate. |

### Risks called out for review

- Ledger needs to also cover the directory side of the equation: if
  `I copy the file ... to deploy/.../local-bdd.yaml` creates the
  `environments/local-bdd/` directory along the way, restore must
  remove the file but leave any pre-existing directory ancestors
  alone. Implementation note rather than a contract change.
- `Given command has succeeded:` keys on the fully resolved command
  text. The handler must expand `${VAR}` first and hash the result,
  not the raw docstring. Two scenarios whose pre-interpolation text
  is identical but whose env var values differ must miss the cache.
- `MatchYAMLSubtree` list comparison is order-sensitive and length-
  strict in both Exact and Subset modes. The CLI features do not
  currently assert against lists (the profile YAMLs are map-only at
  the asserted paths), so this is forward-looking. If a future feature
  needs set-style list comparison, add a new MatchMode rather than
  overloading Subset.
- The Helmfile feature's per-Rule Background re-runs the file
  authoring before every scenario. Ledger snapshotting is idempotent
  on repeat writes (only the first call records), so this is safe,
  but worth a phase-1 test that the snapshot is the file's
  pre-suite state, not the pre-scenario state.
