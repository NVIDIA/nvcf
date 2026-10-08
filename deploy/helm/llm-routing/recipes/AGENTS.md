# LLM model recipes

Read `../README.md` for the Helm installation path and `../ADVANCED.md` for configuration, recovery and verification. `../charts/shared-stack` owns the `llm-stack` shared release in namespace `llm-stack`. Model operations own their resources and reference the installed shared CA. They must preserve shared gateway, operator and credential resources.

## Metadata and chart ownership

`catalog.json` owns SGLang model pins and hardware profiles. Each GGUF recipe folder owns `recipe.json`, `model.lock.json`, `profiles.json` and `NOTICE`. Keep model-specific settings in those sources. The charts bundle copies for independent Helm packaging. Keep bundled metadata synchronized and covered by equality tests. `charts/sglang/files/placement.py` owns the shared placement check. Run `python3 sync-placement.py` after editing it, then `python3 sync-placement.py --check` to verify the GGUF chart copy.

`planned.json` records unavailable models and upstream candidates. Keep their deployment profiles empty until an executable recipe is implemented.

`export_catalog.py` generates the common `index.json` from these sources. Regenerate the index after changing metadata or chart versions, then run `python3 export_catalog.py --check`. Resource metadata must reflect effective pod scheduling, serving, preparation and auxiliary workloads separately. Unified GPU memory shares the host memory pool.

The automatic SGLang and GGUF charts use `recipe`, `profileName`, explicit `nodes`, `storageClassName`, `runtimeClassName` and `sharedCAConfigMap`. Keep model preparation and startup inside Kubernetes. Keep supported installation and recovery on the automatic Helm lifecycle. Retain model PVCs and mount externally supplied claims without adopting them. `reuseCaches` requires complete pinned artifacts and performs local verification before serving.

## Local helpers

`../llm.py` reads live inventory for planning and reads installed shared-stack credentials for discovery and chat. Advanced verification helpers must work with the Helm-installed stack without a saved connection file.

Recipe tuning defaults live in `recipe.json` under `tuning.unified` and `tuning.discrete`. Automatic Helm profiles select defaults by `hardware.memoryMode`; their advertised context and concurrency must match. Keep derived placement, tuned llama.cpp arguments and resource reservations covered by tests.

The recipe's `memory.discrete` host limits cover process memory and page cache. A serving pod at its cgroup limit with low resident memory can reflect reclaimable model-file cache.

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

Tests cover every supported profile, startup gates, resource reservations, cache reuse and scoped ownership. A render does not establish a fresh-cluster deployment. Validate real readiness, inference and failure isolation separately. Land runtime and chart fixes in their owning sources. Generate API and deepcopy files through their existing generators.
