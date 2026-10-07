# LLM model recipes

Read `../README.md` for the Helm installation path and `../ADVANCED.md` for configuration, recovery and existing Python workflows. `../charts/shared-stack` owns shared infrastructure. Model operations own their resources and reference the installed shared CA. They must preserve shared gateway, operator and credential resources.

## Metadata and chart ownership

`catalog.json` owns SGLang model pins and hardware profiles. Each GGUF recipe folder owns `recipe.json`, `model.lock.json`, `profiles.json` and `NOTICE`. Keep model-specific settings in those sources. The charts bundle copies for independent Helm packaging. Keep bundled metadata synchronized and covered by equality tests.

`planned.json` records unavailable models and upstream candidates. Keep their deployment profiles empty until an executable recipe is implemented.

`export_catalog.py` generates the common `index.json` from these sources. Regenerate the index after changing metadata or chart versions, then run `python3 export_catalog.py --check`. Resource metadata must reflect effective pod scheduling, serving, preparation and auxiliary workloads separately. Unified GPU memory shares the host memory pool.

The automatic SGLang and GGUF charts use `recipe`, `profileName`, explicit `nodes`, `storageClassName`, `runtimeClassName` and `sharedCAConfigMap`. Keep model preparation and startup inside Kubernetes. Preserve explicit legacy phases. Retain model PVCs and mount externally supplied claims without adopting them. `reuseCaches` requires complete pinned artifacts and performs local verification before serving.

## Existing Python workflows

`recipe.py` uses its containing checkout. Builds include local edits and record the current commit ID for debugging. Image updates and rollback require the routing chart fingerprint recorded at installation. Helm dependencies are built automatically. `--source-dir` selects another existing checkout.

Recipe tuning defaults live in `recipe.json` under `tuning.unified` and `tuning.discrete`. Saved configuration overrides use `tuning` and `resources`. Derive placement and tuned llama.cpp arguments from those settings, and keep `serverArgs` for the remaining runtime flags. Automatic Helm profiles select defaults by `hardware.memoryMode`; their advertised context and concurrency must match.

The recipe's `memory.discrete` host limits cover process memory and page cache. A serving pod at its cgroup limit with low resident memory can reflect reclaimable model-file cache.

Keep `sizing.py` free of cluster access. Its supported placement includes one model node and a split across nodes, covered by `tests/test_topology.py`. `recipes.py` plans SGLang placements from read-only Kubernetes inventory and supports deployment bound to a verified `../stack.py` connection.

## Validation and safety

Node count comes from an eligible hardware profile. Require exclusive GPUs, preserve other allocations and use the cache's original node for retained local storage. Runtime or model pin changes require new hardware qualification. Record automatic Helm validation separately from earlier runtime or phased-flow evidence.

Keep test mocks under `tests`. Store environment settings, plans, credentials, kubeconfigs, TLS material and evidence outside this repository. Pass an explicit context on every Kubernetes and Helm cluster command. Run packaging tests without live cluster changes.

Run from this directory:

```bash
python3 -m pip install -r tests/requirements.txt
python3 -m unittest discover -s tests -v
python3 -m unittest discover -s charts/gguf-backend/tests -v
python3 export_catalog.py --check
helm lint --strict charts/sglang -f charts/sglang/values.example.yaml
helm lint --strict charts/gguf-backend -f charts/gguf-backend/values.example.yaml
```

Tests cover every supported profile and phase, startup gates, resource reservations, cache reuse and scoped ownership. A render does not establish a fresh-cluster deployment. Validate real readiness, inference and failure isolation separately. Land runtime and chart fixes in their owning sources. Generate API and deepcopy files through their existing generators.
