# AGENTS.md - tests/e2e/global-discovery-k3d

End-to-end test of the Global Discovery Service on local k3d clusters.
`README.md` lists the prerequisites, settings and the checks of each phase.

## Layout

- `run.sh`: creates or reuses the k3d clusters (the hub `$E2E_CLUSTER`, and
  `$E2E_CLUSTER-a` and `-b` for phases 2 and 3) on one Docker network, builds
  the images with Bazel and imports them, installs the charts from this
  checkout and runs the checks of each phase in `E2E_PHASES`. Keep it
  compatible with bash 3.2.
- `manifests/`: chart values (`gds-server-values.yaml`,
  `gds-mcs-controller-values.yaml`, `router-values.yaml`), the REST body of
  phase 1 (`frontend-export.json`), and the workloads of phases 2 and 3
  (`mcs-demo.yaml`, `mcs-client.yaml`, `pylon.yaml`) with `__NAME__`
  placeholders that `render_manifest` fills.
- `Makefile`: `test`, `test-cleanup`, `clean`, `deploy`, `dry-run`, `check`,
  and the local workflow targets `cluster`, `images`, `all`, `test-unit`,
  `test-charts` and `destroy`.

## Commands

From the repository root:

```bash
make -C tests/e2e/global-discovery-k3d check         # bash -n, and shellcheck (PATH or Docker image)
make -C tests/e2e/global-discovery-k3d dry-run       # chart renders with the phase values, no cluster
make -C tests/e2e/global-discovery-k3d test E2E_PHASES=1   # the single-cluster checks only
make -C tests/e2e/global-discovery-k3d test-cleanup  # full run, then remove what it created
make -C tests/e2e/global-discovery-k3d clean         # remove what earlier runs left
```

## Rules

- Never touch clusters other than the ones a phase names. Cluster names
  must be `gds-e2e` or start with `gds-e2e-`, Docker networks must start
  with `gds-e2e-`; `check_cluster_name` and `check_network_name` enforce it.
  When another run may be active, use another `E2E_CLUSTER` prefix,
  `E2E_WORK_DIR` and local ports.
- Never read, write or switch the default kubeconfig or its current
  context. Every `kubectl` and `helm` call goes through `kc <cluster>` or
  `hm <cluster>`, which pass the work directory kubeconfig and the context.
- `run.sh` deletes only clusters and networks it created and recorded in
  `$E2E_WORK_DIR` (`created-clusters`, `created-networks`), and only with
  `E2E_CLEANUP=1` or `E2E_CLEANUP_ONLY=1`. A reused cluster keeps running;
  cleanup removes the releases, namespaces, routes and network attachments
  run.sh added. Workload namespaces go before the controller and the hub, so
  the ServiceExport finalizers can still deregister.
- Images are loaded from the Bazel `.tar` targets under run-specific tags
  (`load_bazel_image`), never through the shared `<package>:latest` tag of
  the `_load` targets, which another build may change at any time.
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
