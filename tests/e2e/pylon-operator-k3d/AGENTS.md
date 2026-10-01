# AGENTS.md - tests/e2e/pylon-operator-k3d

End-to-end test of Pylon Operator with the LLM gateway stack on a local k3d
cluster. `README.md` lists the prerequisites, settings and assertions.

## Layout

- `run.sh`: installs the charts from this checkout, applies the manifests and
  runs every assertion. Keep it compatible with bash 3.2.
- `manifests/`: the sample backend and the `InferenceEndpoint`, without a
  namespace; `run.sh` applies them with `-n`.
- `Makefile`: `test`, `test-cleanup`, `check`, and the local workflow targets
  `cluster`, `images`, `import`, `deploy`, `test-unit`, `test-charts`, `all`
  and `destroy`. `docs/dev/inference-endpoints-quickstart.md` describes the
  workflow.

## Commands

From the repository root:

```bash
make -C tests/e2e/pylon-operator-k3d check         # bash -n, and shellcheck when installed
make -C tests/e2e/pylon-operator-k3d test          # needs the k3d cluster and imported images
make -C tests/e2e/pylon-operator-k3d test-cleanup  # also removes the releases and namespaces
make -C tests/e2e/pylon-operator-k3d deploy        # install and stop once the endpoint is ready
make -n -C tests/e2e/pylon-operator-k3d all        # dry run of the whole local workflow
```

## Rules

- Never create, delete or switch clusters or kube contexts from `run.sh`.
  Every `kubectl` and `helm` call goes through `kc` or `hm`, which pass
  `E2E_KUBE_CONTEXT`, and every phase calls `ensure_context`.
- Only the `cluster` and `destroy` Makefile targets create or delete the k3d
  cluster. `cluster` leaves an existing cluster and the current context alone,
  and `destroy` deletes only with `CONFIRM=1`.
- Never print the cluster token or the API key. The key is written only to the
  work directory with mode 600.
- A new assertion is a check function that returns 0 when satisfied and sets
  `LAST_OBSERVED`, run through `expect` (poll until true) or `hold` (stay true).
- Chart, operator, Pylon or gateway changes that the test depends on need a
  rebuilt and re-imported image; see `README.md`.
