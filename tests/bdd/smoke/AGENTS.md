# AGENTS.md - tests/bdd/smoke

Scope: attach-mode smoke BDD under `tests/bdd/smoke/`.

This suite exercises product behavior against an existing target. It never
provisions topology and never inherits install cleanup.

## Ownership

- `features/*.feature` contains safe, independently selectable, self-cleaning
  product smokes.
- `features/operations/` contains cluster-wide or destructive operational
  workflows. These require a committed consent plan.
- `targets/` contains non-secret target coordinates and capability inputs.
- `plans/` contains reviewed CI collections and operational compositions.
- `runner_test.go` owns target loading, selection, isolated CLI sessions, and
  restoration between feature files.
- `plan.go` owns committed plan expansion and direct feature selection.

Shared command execution, logs, state restoration, compensation, DSL helpers,
and target-neutral steps remain in the parent `harness/`, `dsl/`, and
`steps/` packages.

Register features with `steps.RegisterSmoke`. Install bootstrap steps must
remain undefined in this suite.

## Selection

Nightly CI uses a committed plan. A safe collection phase uses `features`
with a glob and expands every matched file into a separately restored run.

Developers select safe smokes directly with repeatable `-bdd-feature` flags.
Direct selection cannot reach `features/operations/`.

Run from `tests/bdd`:

```sh
go test ./smoke -run '^TestLive$' -timeout 60m -v -args \
  -bdd-target tests/bdd/smoke/targets/local-multi.yaml \
  -bdd-feature function-lifecycle.feature \
  -bdd-feature task-lifecycle.feature
```

A committed plan uses `-bdd-plan` instead of `-bdd-feature`. Direct
features may share one `-bdd-tags` filter. Plans own their own tag filters.

## Safety

- `BDD_CLEANUP_MODE` must be unset.
- Every feature declares its required target inputs in Gherkin.
- Every Kubernetes command exposes kubeconfig and context.
- Register compensation before the action it reverses.
- Operational features require plan-level invocation consent.
- Target YAML cannot supply the consent variable selected by a plan.

## Tests

Run from `tests/bdd`:

```sh
go test -short ./smoke
```

The wiring test discovers every feature recursively and checks it against the
smoke step catalog.
