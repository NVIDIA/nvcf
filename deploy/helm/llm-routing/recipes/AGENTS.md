# LLM routing recipe tool

This entry point uses the checkout containing `recipe.py`. Builds include local edits and record the current commit ID for debugging. Image updates and rollback require the routing chart fingerprint recorded at installation. Helm dependencies are built automatically. `--source-dir` selects another existing checkout. Keep test mocks under `tests` and out of the normal model deployment.

Keep environment-specific values, credentials, kubeconfigs, generated TLS material and runtime evidence outside this repository. Pass an explicit context on every Kubernetes and Helm command. Do not change a live cluster while testing packaging.

Keep model-specific settings in the recipe folder (`recipe.json`, `model.lock.json`, `NOTICE`), not in `recipe.py` or the charts. The `memory.discrete` host limits in a recipe cap page cache as well as process memory, so a model pod at its cgroup limit with low resident memory is expected on GPUs with their own memory. Keep `sizing.py` free of cluster access. Placement must work for one model node and for a split across nodes; cover both in `tests/test_topology.py`.

Run `python3 -m unittest discover -s tests -v` and the chart tests under `charts/gguf-backend/tests`. Run `python3 recipe.py --config config.example.json --work-dir /tmp/recipe-render render` for offline chart validation. A render does not establish a fresh-cluster deployment.

Land runtime and chart fixes in their owning source directories with generated API files and tests. Never hand-edit generated CRDs or deepcopy code.
