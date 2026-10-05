# Spark GLM recipe

This entry point uses the checkout containing `spark.py`. `source.lock.json` records the required compatibility baseline, which must be an ancestor of the selected checkout's `HEAD`. Builds include local edits. Helm dependencies are built automatically. `--source-dir` selects another existing checkout. Keep test mocks under `tests` and out of the normal model deployment.

Keep environment-specific values, credentials, kubeconfigs, generated TLS material and runtime evidence outside this repository. Pass an explicit context on every Kubernetes and Helm command. Do not change a live cluster while testing packaging.

Run `python3 -m unittest discover -s tests -v` and the chart tests under `charts/gguf-backend/tests`. Run `python3 spark.py --config config.example.json --work-dir /tmp/spark-render render` for offline chart validation. A render does not establish a fresh-cluster deployment.

Land runtime and chart fixes in their owning source directories with generated API files and tests. Update `source.lock.json` when the recipe requires a newer baseline. Never hand-edit generated CRDs or deepcopy code.
