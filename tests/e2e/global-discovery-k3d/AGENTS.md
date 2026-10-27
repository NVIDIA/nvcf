# AGENTS.md - tests/e2e/global-discovery-k3d

End-to-end test of the Global Discovery Service on local k3d clusters.
`README.md` lists the prerequisites, settings and the checks of each phase.

## Layout

- `run.sh`: creates or reuses the k3d clusters, builds and imports the
  `gds-server` image, installs the chart from this checkout and runs the
  checks of each phase in `E2E_PHASES`. Keep it compatible with bash 3.2.
- `manifests/`: the chart values of phase 1 (`gds-server-values.yaml`) and
  the REST body of the labelled export (`frontend-export.json`).
- `Makefile`: `test`, `test-cleanup`, `clean`, `deploy`, `dry-run`, `check`,
  and the local workflow targets `cluster`, `images`, `all`, `test-unit`,
  `test-charts` and `destroy`.

## Commands

From the repository root:

```bash
make -C tests/e2e/global-discovery-k3d check         # bash -n, and shellcheck when installed
make -C tests/e2e/global-discovery-k3d dry-run       # chart render with the phase 1 values, no cluster
make -C tests/e2e/global-discovery-k3d test-cleanup  # full run, then remove what it created
make -C tests/e2e/global-discovery-k3d clean         # remove what earlier runs left
```

## Rules

- Never touch clusters other than the ones a phase names. Cluster names
  must be `gds-e2e` or start with `gds-e2e-`; `check_cluster_name` enforces
  it.
- Never read, write or switch the default kubeconfig or its current
  context. Every `kubectl` and `helm` call goes through `kc <cluster>` or
  `hm <cluster>`, which pass the work directory kubeconfig and the context.
- `run.sh` deletes only clusters it created and recorded in
  `$E2E_WORK_DIR/created-clusters`, and only with `E2E_CLEANUP=1` or
  `E2E_CLEANUP_ONLY=1`. A cluster from `make cluster` keeps running; cleanup
  removes only the release and namespace from it.
- A new assertion is a check function that returns 0 when satisfied and sets
  `LAST_OBSERVED`, run through `expect` (poll until true) or `once` (steps
  that write).
- Feed text to `grep -q`, `head` and `awk` through here-strings, not pipes:
  with `pipefail`, a reader that exits early fails the pipe with SIGPIPE.
- REST bodies follow the proto3 JSON mapping of module `docs/api.md`
  (lowerCamelCase, enum names as strings, `uint64` as strings). Keep the
  checks in step with that document when the API changes.
- A new phase is a `run_phase_<n>` function added to `run_phases`; see
  `README.md`.
