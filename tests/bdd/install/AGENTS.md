# AGENTS.md - tests/bdd/install

Scope: install-oriented BDD under `tests/bdd/install/`.

This suite owns topology provisioning, stack installation, registration, and
install verification. It may use local or EKS infrastructure. Product smokes
that only need an existing target belong under `tests/bdd/smoke/`.

## Ownership

- `features/` contains install workflows.
- `install_test.go` owns live entry points and fake-runner wiring tests.
- `cleanup.go` owns `BDD_CLEANUP_MODE` parsing and the explicit
  ncp-local cleanup command map.
- Shared command execution, logs, state restoration, compensation, DSL helpers,
  and target-neutral steps remain in the parent `harness/`, `dsl/`, and
  `steps/` packages.

Register features with `steps.RegisterInstall`. Smoke registration deliberately
excludes local topology bootstrap steps.

## Cleanup

Install cleanup runs before the shared harness snapshots CLI state. Keep every
cleanup mode and command in `cleanup.go`. Do not move local destroy policy into
the shared harness or smoke runner.

Never run destructive cleanup without the user's explicit approval in the
current thread.

## Tests

Run from `tests/bdd`:

```sh
go test -short ./install
```

Live entry points skip under `-short`. Run one explicitly with the matching
cleanup mode only after the user approves the destructive local operation.
