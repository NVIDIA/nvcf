# Destructive cleanup plan for the BDD suite

Opt-in, pre-suite cleanup hooks that let an operator re-run a feature
against an already-modified host without manually tearing down k3d.
This revision incorporates the second-round risk review and is built
around one governing rule.

## Governing rule

Topology cleanup may delete topology resources. Stack cleanup may
only delete stack-owned resources and stack-generated artifacts.

Concretely:

- Topology cleanup deletes one or more k3d clusters. Everything
  inside them goes with the cluster.
- Stack cleanup uninstalls helm releases on an explicit allow-list,
  deletes namespaces on an explicit allow-list, and removes
  stack-generated handoff files. It must not touch:
  - Helm releases not on the allow-list, including the gateway-api
    controller `eg` in `envoy-gateway-system` installed by
    `tools/ncp-local-cluster/scripts/setup-gateway-api.sh`.
  - Namespaces shared with topology infrastructure, including
    `envoy-gateway-system` and `cert-manager`.
  - Any CRD.

A failure during cleanup aborts the suite. Only NotFound is
swallowed via `--ignore-not-found`; timeouts, permission errors,
finalizer stalls, and unreachable kube contexts propagate.

## Goals

1. Topology cleanup: nuke every `ncp-local*` k3d cluster on the host.
   Cheapest path to a guaranteed-clean run when switching topologies.
2. Stack-only cleanup: uninstall every stack-owned helm release and
   delete every stack-owned namespace on a retained k3d cluster.
   Faster than topology rebuild; intended for switching between
   install methods (CLI install vs Helmfile install, `local` vs
   `local-bdd` env) without paying the k3d boot cost.

Both opt-in. Default suite behavior is unchanged.

## Non-goals

- Auto-detection of which cleanup mode is appropriate. Operator
  picks; harness validates.
- Cleanup at the Gherkin layer. Per `AGENTS.md`, multi-step
  destruction must not hide behind a domain-named Given.
- CRD removal. Stale CRDs are documented residue; manual cleanup
  required for major schema rolls.

## Risks addressed by this revision

| Risk | Mitigation |
|------|------------|
| Stack cleanup uninstalls every helm release in every namespace, taking out `eg` and other topology infrastructure. | Stack cleanup iterates an explicit `(release, namespace)` allow-list. Releases not in the list are untouched. |
| Stack cleanup deletes `envoy-gateway-system` (and possibly `cert-manager`), removing topology resources. | Stack-cleanup namespace allow-list excludes both. Stack-owned releases within those namespaces are uninstalled in place; the namespace itself survives. Only topology cleanup destroys those namespaces, and it does so by destroying the entire cluster. |
| Stack cleanup swallows failures via `\|\| true`. | No `\|\| true` anywhere. `helm uninstall` and `kubectl delete` use `--ignore-not-found`. Timeouts, permission errors, and finalizer stalls return non-zero and abort the suite via `RunPreSuiteCleanup`. |
| `destroy-all-ncp-local` silently depends on `jq`, references a non-existent prerequisite, and hides pipe failures. | Recipe checks `command -v jq` before running. Pipeline runs under `set -o pipefail`. No `ensure-kube-config-dir` reference. Single quoted command list per step. |
| `_clean-stack-out` uses a repo-rooted path inside a `make -C deploy/stacks/self-managed` recipe. | Path is relative to the recipe's cwd: `out/*.yaml`. |
| Snapshotting CLI state before cleanup preserves a token that points at a cluster that cleanup destroyed. | Cleanup runs first; the state-file snapshot captures the post-cleanup baseline. For destructive modes the operator's pre-suite token is intentionally not preserved (it was invalidated by cleanup anyway). Documented in README. |
| Verification matrix mentions a test that did not exist. | `TestMultiClusterHelmfile` is now in `godog_test.go` and shipped on the branch. |
| Repo style drift in the plan file itself. | This document has no bold and no em-dash characters. Plain ASCII throughout. |

## Operator surface

### Env var (BDD-driven)

```
BDD_CLEANUP_MODE = "" | "stack-single" | "stack-multi" | "topology-single" | "topology-multi"
```

Unknown values fail `NewSuite` immediately with an error that lists
the valid set. Empty (or unset) skips cleanup.

### Make targets (operator-typed, also invoked by harness)

```sh
# Topology
make -C tools/ncp-local-cluster destroy CLUSTER_NAME=ncp-local
make -C tools/ncp-local-cluster destroy-all-ncp-local

# Stack
make -C deploy/stacks/self-managed destroy-stack-single CLUSTER_NAME=ncp-local
make -C deploy/stacks/self-managed destroy-stack-multi
```

## Allow-lists (single source of truth)

### Stack-owned helm releases

Sourced from `deploy/stacks/self-managed/helmfile.d/*.gotmpl` releases
plus the worker layer in `helmfile-nvca-operator.yaml.gotmpl`.
Defined as Make variables in `deploy/stacks/self-managed/Makefile` so
both targets read the same list.

```make
# Format: name:namespace per release. Update whenever helmfile.d
# adds or removes a release.
STACK_RELEASES_CP := \
  nats:nats-system \
  cert-manager:cert-manager \
  openbao-server:vault-system \
  cassandra:cassandra-system \
  api-keys:api-keys \
  admin-issuer-proxy:api-keys \
  sis:sis \
  api:nvcf \
  nvct-api:nvcf \
  invocation-service:nvcf \
  grpc-proxy:nvcf \
  ess-api:ess \
  notary-service:nvcf \
  reval:nvcf \
  nats-auth-callout-service:nats-system \
  ingress:envoy-gateway-system \
  llm-request-router:nvcf \
  llm-api-gateway:nvcf

STACK_RELEASES_WORKER := \
  nvca-operator:nvca-operator
```

`ingress:envoy-gateway-system` is on the release allow-list but the
namespace itself is NOT on the namespace allow-list, by design.

### Stack-owned namespaces (deleted by stack cleanup)

Excludes `envoy-gateway-system` and `cert-manager` because those are
shared with topology infrastructure.

```make
STACK_NAMESPACES_CP := \
  nats-system cassandra-system vault-system api-keys sis ess nvcf

STACK_NAMESPACES_WORKER := \
  nvca-operator nvca-system nvcf-backend
```

### Stack-owned namespaced custom resources

Deleted before helm uninstall so finalizer-bearing CRs do not block
namespace termination.

```make
STACK_CRS_WORKER := nvcfbackend
```

If a future install path adds another finalizer-bearing CR, extend
this list.

## File-by-file design

### tools/ncp-local-cluster/Makefile

#### Modified target: destroy

Make idempotent on a missing cluster. No `|| true` after delete;
delete failure of a present cluster is a real error.

```make
destroy:
	@if k3d cluster get $(CLUSTER_NAME) >/dev/null 2>&1; then \
		echo "Destroying k3d cluster $(CLUSTER_NAME)..."; \
		k3d cluster delete $(CLUSTER_NAME); \
		echo "Cluster $(CLUSTER_NAME) destroyed."; \
	else \
		echo "Cluster $(CLUSTER_NAME) absent; skipping."; \
	fi
```

#### New target: destroy-all-ncp-local

Discovers and deletes every `ncp-local*` cluster, handles the orphan
compute-cluster case the existing `destroy-multicluster` misses.

```make
destroy-all-ncp-local:
	@command -v jq >/dev/null 2>&1 || { \
		echo "destroy-all-ncp-local requires jq; install it and retry." >&2; \
		exit 1; \
	}
	@set -o pipefail; \
	NAMES=$$(k3d cluster list -o json | jq -r '.[] | select(.name|startswith("ncp-local")) | .name'); \
	if [ -z "$$NAMES" ]; then \
		echo "No ncp-local* clusters present."; \
		exit 0; \
	fi; \
	for name in $$NAMES; do \
		echo "Destroying k3d cluster $$name..."; \
		k3d cluster delete "$$name"; \
	done
```

No `|| true`. `set -o pipefail` so the pipe failure of `k3d cluster
list` aborts the recipe instead of silently producing an empty list.

#### Unmodified: destroy-multicluster

Left in place for operators who want to delete exactly the resolved
compute-cluster set. Harness does not invoke it.

### deploy/stacks/self-managed/Makefile.dist

No new targets. The stack-destroy logic uses raw helm and kubectl
with explicit kube-context flags; it belongs in the dev wrapper.

### deploy/stacks/self-managed/Makefile (dev wrapper)

#### New target: destroy-stack-single

```make
destroy-stack-single: ## Uninstall NVCF stack from one cluster; leaves k3d running
	@CTX="k3d-$(CLUSTER_NAME)"; \
	if ! kubectl --context "$$CTX" cluster-info >/dev/null 2>&1; then \
		echo "Context $$CTX unreachable; nothing to clean."; \
		exit 0; \
	fi; \
	$(MAKE) _delete-stack-crs    CTX="$$CTX" CR_LIST="$(STACK_CRS_WORKER)" NS_LIST="nvca-operator"; \
	$(MAKE) _uninstall-stack-releases CTX="$$CTX" RELEASE_LIST="$(STACK_RELEASES_WORKER) $(STACK_RELEASES_CP)"; \
	$(MAKE) _delete-stack-namespaces CTX="$$CTX" NS_LIST="$(STACK_NAMESPACES_WORKER) $(STACK_NAMESPACES_CP)"; \
	$(MAKE) _clean-stack-out
```

#### New target: destroy-stack-multi

Iterates the discovered ncp-local-compute-* clusters explicitly,
then the control-plane cluster. No global `kubectl config
use-context` anywhere; every command carries `--context` /
`--kube-context`.

```make
destroy-stack-multi: ## Uninstall NVCF stack from CP plus every discovered compute cluster
	@command -v jq >/dev/null 2>&1 || { \
		echo "destroy-stack-multi requires jq; install it and retry." >&2; \
		exit 1; \
	}
	@set -o pipefail; \
	COMPUTES=$$(k3d cluster list -o json | jq -r '.[] | select(.name|startswith("ncp-local-compute-")) | .name'); \
	for name in $$COMPUTES; do \
		CTX="k3d-$$name"; \
		echo ">>> Cleaning compute cluster $$CTX"; \
		if ! kubectl --context "$$CTX" cluster-info >/dev/null 2>&1; then \
			echo "Context $$CTX unreachable; skipping."; \
			continue; \
		fi; \
		$(MAKE) _delete-stack-crs    CTX="$$CTX" CR_LIST="$(STACK_CRS_WORKER)" NS_LIST="nvca-operator"; \
		$(MAKE) _uninstall-stack-releases CTX="$$CTX" RELEASE_LIST="$(STACK_RELEASES_WORKER)"; \
		$(MAKE) _delete-stack-namespaces CTX="$$CTX" NS_LIST="$(STACK_NAMESPACES_WORKER)"; \
	done; \
	if k3d cluster get ncp-local-cp >/dev/null 2>&1; then \
		echo ">>> Cleaning control-plane cluster k3d-ncp-local-cp"; \
		$(MAKE) _uninstall-stack-releases CTX="k3d-ncp-local-cp" RELEASE_LIST="$(STACK_RELEASES_CP)"; \
		$(MAKE) _delete-stack-namespaces CTX="k3d-ncp-local-cp" NS_LIST="$(STACK_NAMESPACES_CP)"; \
	fi
	@$(MAKE) _clean-stack-out
```

#### New internal helpers

These are private and not advertised in `make help`. They take
parameters via Make variables so the public targets compose them.

##### _delete-stack-crs

For each namespace in `NS_LIST` that exists, delete each CR kind in
`CR_LIST`. NotFound is fine; timeouts are not.

```make
_delete-stack-crs:
	@for ns in $(NS_LIST); do \
		if kubectl --context $(CTX) get namespace $$ns >/dev/null 2>&1; then \
			for cr in $(CR_LIST); do \
				echo "  delete $$cr in $$ns"; \
				kubectl --context $(CTX) -n $$ns delete $$cr --all --ignore-not-found --timeout=60s; \
			done; \
		fi; \
	done
```

No `|| true`. A finalizer-stall timeout will propagate and abort
cleanup.

##### _uninstall-stack-releases

Iterates the explicit `(name:namespace)` allow-list. Skips any
release not present in the cluster via `--ignore-not-found`. Does
not enumerate `helm list` because that would re-introduce the
blast-radius risk.

```make
_uninstall-stack-releases:
	@for entry in $(RELEASE_LIST); do \
		name=$${entry%:*}; \
		ns=$${entry#*:}; \
		echo "  uninstall $$name in $$ns"; \
		helm --kube-context $(CTX) uninstall "$$name" -n "$$ns" \
			--ignore-not-found --wait --timeout 2m; \
	done
```

`--ignore-not-found` is a helm 3.13+ flag. The dev workstation README
documents the minimum helm version separately; the harness builds
nvcf-cli but does not manage helm, so we rely on the existing
prerequisite.

##### _delete-stack-namespaces

Stack-owned namespaces only. `envoy-gateway-system` and
`cert-manager` are intentionally absent from `NS_LIST` callers.

```make
_delete-stack-namespaces:
	@for ns in $(NS_LIST); do \
		echo "  delete namespace $$ns"; \
		kubectl --context $(CTX) delete namespace $$ns \
			--ignore-not-found --wait --timeout=120s; \
	done
```

##### _clean-stack-out

Runs from `deploy/stacks/self-managed/`, so the path is `out/*.yaml`.

```make
_clean-stack-out:
	@echo "  clean out/"
	@rm -f out/*.yaml
```

Only `*.yaml` so developer ad-hoc text notes in the same directory
survive.

### tests/bdd/harness/cleanup.go (new file)

#### Type: CleanupMode

Closed-enum string. Empty string is the explicit "no cleanup" value
(matches the env var being unset).

```go
type CleanupMode string

const (
    CleanupNone           CleanupMode = ""
    CleanupStackSingle    CleanupMode = "stack-single"
    CleanupStackMulti     CleanupMode = "stack-multi"
    CleanupTopologySingle CleanupMode = "topology-single"
    CleanupTopologyMulti  CleanupMode = "topology-multi"
)

func validCleanupModes() []CleanupMode {
    return []CleanupMode{
        CleanupNone, CleanupStackSingle, CleanupStackMulti,
        CleanupTopologySingle, CleanupTopologyMulti,
    }
}
```

#### Function: ResolveCleanupMode

Validates against the closed enum. Returns an error for any unknown
value; the caller propagates that so the suite never starts on a
typo.

```go
// ResolveCleanupMode reads BDD_CLEANUP_MODE and returns the validated
// enum value. Any value outside the enumerated set returns an error;
// the unset (empty) case maps to CleanupNone with no error. Callers
// must NOT silently downgrade an invalid value to CleanupNone -- a
// typo would leave scenarios running against state the operator
// intended to wipe.
func ResolveCleanupMode() (CleanupMode, error) {
    raw := strings.TrimSpace(os.Getenv("BDD_CLEANUP_MODE"))
    for _, candidate := range validCleanupModes() {
        if string(candidate) == raw {
            return candidate, nil
        }
    }
    valid := []string{}
    for _, c := range validCleanupModes() {
        valid = append(valid, fmt.Sprintf("%q", string(c)))
    }
    return CleanupNone, fmt.Errorf(
        "BDD_CLEANUP_MODE=%q is not a valid cleanup mode; expected one of %s",
        raw, strings.Join(valid, ", "),
    )
}
```

#### Function: Suite.RunPreSuiteCleanup

Shells out via the same `CommandRunner` everything else uses;
stdout/stderr land in `out/<run-id>/logs/<seq>.{cmd,stdout,stderr}`.
Failure aborts the suite. CleanupNone is a no-op.

```go
// RunPreSuiteCleanup invokes the make target corresponding to mode
// through the suite's CommandRunner. Logging, argv recording, and
// shlex parsing flow through the same pipe scenario commands use;
// a post-mortem `ls out/<run-id>/logs/` shows the cleanup alongside
// scenario steps.
//
// Failure aborts the suite. A half-cleaned cluster would leave
// scenarios running on top of stale state, exactly the case this
// hook exists to prevent.
func (s *Suite) RunPreSuiteCleanup(ctx context.Context, mode CleanupMode) error {
    if mode == CleanupNone {
        return nil
    }
    cmd, ok := cleanupCommandFor(mode)
    if !ok {
        return fmt.Errorf("no cleanup command for mode %q", mode)
    }
    result, err := s.Runner.Run(ctx, cmd)
    if err != nil {
        return fmt.Errorf("pre-suite cleanup (%s): %w", mode, err)
    }
    if result.ExitCode != 0 {
        return fmt.Errorf(
            "pre-suite cleanup (%s) exit=%d (see %s for stdout/stderr)",
            mode, result.ExitCode, s.Config.CommandLogDir,
        )
    }
    return nil
}
```

#### Function: cleanupCommandFor (package-private)

Single source of truth so the unit test asserts command text exactly
and the wiring stays declarative.

```go
func cleanupCommandFor(mode CleanupMode) (string, bool) {
    switch mode {
    case CleanupTopologySingle:
        return "make -C tools/ncp-local-cluster destroy CLUSTER_NAME=ncp-local", true
    case CleanupTopologyMulti:
        return "make -C tools/ncp-local-cluster destroy-all-ncp-local", true
    case CleanupStackSingle:
        return "make -C deploy/stacks/self-managed destroy-stack-single CLUSTER_NAME=ncp-local", true
    case CleanupStackMulti:
        return "make -C deploy/stacks/self-managed destroy-stack-multi", true
    }
    return "", false
}
```

### tests/bdd/harness/suite.go (modified)

#### Ordering inside NewSuite: cleanup first, then snapshot

For destructive modes the operator's pre-suite JWT was already
invalidated by the cleanup, so snapshotting first preserves nothing
useful. Snapshot-after captures a meaningful baseline (the
post-cleanup state) that the Ledger restores on teardown.

```go
func NewSuite(t *testing.T) (*Suite, error) {
    // ... existing config + dir setup ...
    runner := NewCommandRunner(cfg.RepoRoot, cfg.CommandLogDir)
    if err := buildCLI(cfg); err != nil {
        return nil, err
    }
    t.Setenv("NVCF_CLI", cfg.CLIPath)
    t.Setenv("REPO_ROOT", cfg.RepoRoot)

    suite := &Suite{
        Config: cfg,
        Runner: runner,
        Ledger: NewLedger(cfg.LedgerDir),
        Cache:  NewCommandCache(),
    }

    mode, err := ResolveCleanupMode()
    if err != nil {
        return nil, err
    }
    if err := suite.RunPreSuiteCleanup(context.Background(), mode); err != nil {
        return nil, err
    }

    if err := suite.snapshotCLIStateFile("nvcf-cli-local"); err != nil {
        return nil, err
    }
    return suite, nil
}
```

The README's cleanup section calls out the consequence: with
`BDD_CLEANUP_MODE=topology-*`, the operator's pre-suite CLI state is
not preserved across the run; the JWT that pointed at the now-deleted
cluster was already useless.

### tests/bdd/harness/cleanup_test.go (new file)

```go
func TestResolveCleanupMode(t *testing.T) {
    cases := map[string]struct {
        envValue string
        want     CleanupMode
        wantErr  bool
    }{
        "unset":               {envValue: "",                 want: CleanupNone},
        "stack-single":        {envValue: "stack-single",     want: CleanupStackSingle},
        "stack-multi":         {envValue: "stack-multi",      want: CleanupStackMulti},
        "topology-single":     {envValue: "topology-single",  want: CleanupTopologySingle},
        "topology-multi":      {envValue: "topology-multi",   want: CleanupTopologyMulti},
        "typo rejected":       {envValue: "stack_single",     wantErr: true},
        "legacy var rejected": {envValue: "fresh-topology",   wantErr: true},
    }
    for name, tc := range cases {
        t.Run(name, func(t *testing.T) {
            t.Setenv("BDD_CLEANUP_MODE", tc.envValue)
            got, err := ResolveCleanupMode()
            if (err != nil) != tc.wantErr {
                t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
            }
            if !tc.wantErr && got != tc.want {
                t.Fatalf("got=%q want=%q", got, tc.want)
            }
        })
    }
}

func TestRunPreSuiteCleanup_invokesRightCommand(t *testing.T) {
    cases := []struct {
        mode    CleanupMode
        wantCmd string
    }{
        {CleanupTopologySingle, "make -C tools/ncp-local-cluster destroy CLUSTER_NAME=ncp-local"},
        {CleanupTopologyMulti,  "make -C tools/ncp-local-cluster destroy-all-ncp-local"},
        {CleanupStackSingle,    "make -C deploy/stacks/self-managed destroy-stack-single CLUSTER_NAME=ncp-local"},
        {CleanupStackMulti,     "make -C deploy/stacks/self-managed destroy-stack-multi"},
    }
    for _, tc := range cases {
        t.Run(string(tc.mode), func(t *testing.T) {
            recorder := &recordingRunner{}
            suite := &Suite{Runner: recorder, Config: Config{CommandLogDir: t.TempDir()}}
            if err := suite.RunPreSuiteCleanup(context.Background(), tc.mode); err != nil {
                t.Fatalf("RunPreSuiteCleanup: %v", err)
            }
            if got := recorder.runs[0]; got != tc.wantCmd {
                t.Fatalf("got %q want %q", got, tc.wantCmd)
            }
        })
    }
}

func TestRunPreSuiteCleanup_noopOnNone(t *testing.T) {
    recorder := &recordingRunner{}
    suite := &Suite{Runner: recorder, Config: Config{CommandLogDir: t.TempDir()}}
    if err := suite.RunPreSuiteCleanup(context.Background(), CleanupNone); err != nil {
        t.Fatalf("CleanupNone should be a no-op, got %v", err)
    }
    if len(recorder.runs) != 0 {
        t.Fatalf("CleanupNone should not invoke the runner, got %v", recorder.runs)
    }
}

func TestRunPreSuiteCleanup_nonzeroExitFailsSuite(t *testing.T) {
    runner := &recordingRunner{nextResult: Result{ExitCode: 2}}
    suite := &Suite{Runner: runner, Config: Config{CommandLogDir: t.TempDir()}}
    if err := suite.RunPreSuiteCleanup(context.Background(), CleanupStackSingle); err == nil {
        t.Fatal("expected error on non-zero exit")
    }
}
```

`recordingRunner` is a local test double identical in shape to the
pattern in `runner_test.go`.

### Documentation updates

#### tests/bdd/README.md

Add a top-level "Cleanup between runs" section after the live-run
prerequisites. Contents:

- One paragraph framing the trade-off (topology is clean but slow;
  stack is faster but preserves whatever the cluster carried before
  the install).
- Table of the four cleanup values, what each does, and the
  hand-typed `make` equivalent.
- The four end-to-end command examples from the verification matrix.
- Operator-facing notes:
  - CRDs survive every mode. If a previous install registered CRDs
    that the next install schema-incompatibly changes, you must clear
    them manually.
  - With `BDD_CLEANUP_MODE=topology-*`, your pre-suite CLI state is
    not preserved across the run. The token that pointed at the
    now-deleted cluster was already invalid.
  - jq is a prerequisite for `topology-multi` and `stack-multi`.
  - Do not run BDD concurrently against shared k3d.

#### tests/bdd/AGENTS.md

Add a rule block after the existing harness-level rules:

- Cleanup belongs in `harness/cleanup.go`, never in `steps/`. No
  domain-named Gherkin Given for destruction.
- `BDD_CLEANUP_MODE` is the only env var. Unknown values fail
  the suite at start; the harness never silently downgrades to
  CleanupNone.
- New Make targets are invoked from the harness only through the
  `cleanupCommandFor` map. Both the env-var and the Make-target
  surface are intentionally maintained so an operator can clean by
  hand without involving go test.
- The governing rule (topology vs stack scope) is restated here so a
  future contributor reading AGENTS.md before editing the Make
  targets sees it.

## Wiring-test impact

`newWiringSuite` in `godog_test.go` constructs `Suite` directly and
does not call `NewSuite`. Cleanup never fires in wiring tests; no
canned responses needed.

`harness/cleanup_test.go` is the new unit-test surface; it stays
self-contained.

## Live verification matrix

Anchored `-run` so the `...FeatureFileWiresToSteps` sibling does not
also match.

| Goal | Setup | Command |
|------|-------|---------|
| Topology cleanup wipes stale single-cluster before multi-cluster install | `TestSingleClusterUp` left ncp-local running | `BDD_CLEANUP_MODE=topology-multi go test -run '^TestMultiClusterUp$' -timeout 60m -v` |
| Topology cleanup wipes stale multi-cluster before single-cluster install | `TestMultiClusterUp` left ncp-local-cp + compute-1 running | `BDD_CLEANUP_MODE=topology-single go test -run '^TestSingleClusterUp$' -timeout 30m -v` |
| Stack cleanup allows install-method swap on a retained cluster | `TestSingleClusterUp` left CLI install on ncp-local | `BDD_CLEANUP_MODE=stack-single NGC_API_KEY=... SAMPLE_NGC_ORG=... SAMPLE_NGC_TEAM=... go test -run '^TestSingleClusterHelmfile$' -timeout 90m -v` |
| Stack cleanup on multi-cluster, retains `eg` in `envoy-gateway-system` | `TestMultiClusterUp` left CLI install on cp+compute | `BDD_CLEANUP_MODE=stack-multi NGC_API_KEY=... SAMPLE_NGC_ORG=... SAMPLE_NGC_TEAM=... go test -run '^TestMultiClusterHelmfile$' -timeout 90m -v` |
| Invalid mode fails suite immediately | none | `BDD_CLEANUP_MODE=stack_single go test -run '^TestSingleClusterUp$' -v` expect: `NewSuite` error citing the valid set |
| CleanupNone default | none | `go test -run '^TestSingleClusterUp$' -timeout 30m -v` runs unchanged |
| Make targets work standalone | populated multi-cluster | `make -C deploy/stacks/self-managed destroy-stack-multi` |
| Make targets idempotent on empty state | nothing installed | `make -C deploy/stacks/self-managed destroy-stack-single` and `make -C tools/ncp-local-cluster destroy-all-ncp-local` both exit 0 |
| Stack cleanup preserves `eg` | post stack-multi cleanup | `kubectl --context k3d-ncp-local-cp -n envoy-gateway-system get deployment eg` returns the deployment, not NotFound |

The last row is the critical regression test for the governing rule.
Add it to a follow-up live-run check in the README; it does not need
its own Gherkin scenario.

## Known residue (out of scope for v1)

1. CRDs survive every mode. Manual `kubectl --context <ctx> delete
   crd <name>` when a major install changes schema incompatibly.
2. Orphan volumes. `k3d cluster delete` removes attached volumes;
   stack cleanup retains PVCs in lingering namespaces until namespace
   termination completes. If a namespace hangs on a finalizer the
   namespace delete times out and the suite aborts loudly.
3. Concurrent `go test` runs against the same k3d host race. Mitigation
   is documentation only.

## Implementation order

Commit 1: `feat(self-managed): idempotent destroy and allow-listed stack cleanup`

- `tools/ncp-local-cluster/Makefile`: guard `destroy`, add
  `destroy-all-ncp-local` with jq check and pipefail.
- `deploy/stacks/self-managed/Makefile`: add `STACK_RELEASES_*`,
  `STACK_NAMESPACES_*`, `STACK_CRS_WORKER` variables; add
  `destroy-stack-single`, `destroy-stack-multi`, and the four `_`
  helpers.
- Verify by hand: each target idempotently exits 0 when target state
  is already clean; `eg` survives stack cleanup.

Commit 2: `test(bdd): wire pre-suite cleanup via BDD_CLEANUP_MODE`

- `tests/bdd/harness/cleanup.go` and `cleanup_test.go`.
- `tests/bdd/harness/suite.go` ordering change (cleanup before
  snapshot).
- `tests/bdd/README.md` cleanup section.
- `tests/bdd/AGENTS.md` cleanup-rule block restating the governing
  rule.

Two commits keep the operator-usable Make work mergeable on its own
even if the harness wiring needs another review pass.
