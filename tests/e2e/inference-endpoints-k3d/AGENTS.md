# AGENTS.md - tests/e2e/inference-endpoints-k3d

End-to-end test of Pylon Operator with the LLM gateway stack on a local k3d
cluster. `README.md` lists the prerequisites, settings and assertions.

## Layout

- `run.sh`: installs the charts from this checkout, or with `IMAGE_SOURCE=ngc`
  the published charts and images named in `PUBLISHED_VERSIONS_FILE`, applies
  the manifests and runs every assertion. Keep it compatible with bash 3.2.
- `restart-cases.sh`: the restart and failure cases, their checks and their
  faults. `run.sh` sources it and calls `run_restart_cases`; it is not run on
  its own and uses `run.sh`'s helpers. The same bash 3.2 and `kc` rules apply.
- `manifests/`: the sample backend and the `InferenceEndpoint`, without a
  namespace; `run.sh` applies them with `-n`. `run.sh` fills in the sample's
  `${E2E_SAMPLE_IMAGE}` and `${E2E_SAMPLE_IMAGE_PULL_SECRETS}` placeholders
  with sed into the work directory first; do not apply the file directly.
- `Makefile`: `test`, `test-cleanup`, `check`, and the local workflow targets
  `cluster`, `images`, `import`, `deploy`, `test-unit`, `test-charts`, `all`
  and `destroy`. `docs/dev/inference-endpoints-quickstart.md` describes the
  workflow.

## Commands

From the repository root:

```bash
make -C tests/e2e/inference-endpoints-k3d check         # bash -n, and shellcheck when installed
make -C tests/e2e/inference-endpoints-k3d test          # needs the k3d cluster and imported images
make -C tests/e2e/inference-endpoints-k3d test-cleanup  # also removes the releases and namespaces
make -C tests/e2e/inference-endpoints-k3d deploy        # install and stop once the endpoint is ready
make -n -C tests/e2e/inference-endpoints-k3d all        # dry run of the whole local workflow
E2E_DRY_RUN=1 tests/e2e/inference-endpoints-k3d/run.sh  # values files and chart renders, no cluster
E2E_DRY_RUN=1 IMAGE_SOURCE=ngc PUBLISHED_VERSIONS_FILE=<versions.json> tests/e2e/inference-endpoints-k3d/run.sh
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
- Never print `NGC_API_KEY` or put it on a command line. It goes only into the
  registry config file in the work directory (mode 600), which `on_exit`
  removes.
- `IMAGE_SOURCE=local` must keep working exactly as before; ngc-only values go
  in the second values file of `prepare_stack` and `prepare_operator`.
- The versions file format in `README.md` is a contract with the publishing
  pipeline. In it the operator image key is `nvcf-pylon-operator`, and its
  chart key is `pylon-operator`. Change the format only together with the
  pipeline that produces the file.
- A new assertion is a check function that returns 0 when satisfied and sets
  `LAST_OBSERVED`, run through `expect` (poll until true) or `hold` (stay true).
- Chart, operator, Pylon or gateway changes that the test depends on need a
  rebuilt and re-imported image; see `README.md`.
