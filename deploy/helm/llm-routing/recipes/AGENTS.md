# LLM model recipe tools

Read `README.md` to choose the GGUF recipe tool or the SGLang catalog planner. Shared infrastructure is owned by `../stack.py`. Bind independent models with its verified connection; model operations must not update shared gateway, operator or credential resources.

## GGUF recipes

`recipe.py` uses its containing checkout. Builds include local edits and record the current commit ID for debugging. Image updates and rollback require the routing chart fingerprint recorded at installation. Helm dependencies are built automatically. `--source-dir` selects another existing checkout.

Keep model-specific settings in the recipe folder (`recipe.json`, `model.lock.json`, `NOTICE`), not in `recipe.py` or the charts. Keep `sizing.py` free of cluster access. Placement must work for one model node and for a split across nodes; cover both in `tests/test_topology.py`.

## SGLang catalog

`catalog.json` owns supported model profiles, immutable artifact pins and workload envelopes. `recipes.py` plans placements from read-only Kubernetes inventory. The SGLang chart owns only a model release, its retained cache claims and its InferenceEndpoint.

Node count comes from the selected eligible profile. Do not calculate tensor parallelism from weight size alone, allocate shared GPUs, or silently evict another workload. Keep deployment status as pending until real hardware verification passes. Runtime or model pin changes require a new hardware validation pass. The detailed workflow is in `MULTI_MODEL.md`.

## Validation and safety

Keep test mocks under `tests` and out of the normal model deployment. Keep environment settings, plans, credentials, kubeconfigs, generated TLS material and runtime evidence outside this repository. Pass an explicit context on every Kubernetes and Helm command. Do not change a live cluster while testing packaging.

Run from this directory:

```bash
python3 -m pip install -r tests/requirements.txt
python3 -m unittest discover -s tests -v
python3 -m unittest discover -s charts/gguf-backend/tests -v
python3 recipe.py --context llm-routing-demo --config config.example.json --work-dir /tmp/recipe-render render
```

Tests render every SGLang profile and phase and cover GGUF placement topologies. A render does not establish a fresh-cluster deployment. Live qualification, download, serving and failure tests are separate actions.

Land runtime and chart fixes in their owning source directories with generated API files and tests. Never hand-edit generated CRDs or deepcopy code.
