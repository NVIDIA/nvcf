# Spark GLM recipe

This entry point uses the public source revision in `source.lock.json`. The older CPU deployment has been retired. Keep test mocks under `tests` and out of the normal model deployment.

Keep environment-specific values, credentials, kubeconfigs, generated TLS material and runtime evidence outside this repository. Pass an explicit context on every Kubernetes and Helm command. Do not change a live cluster while testing packaging.

Run `python3 -m unittest discover -s tests -v` and the chart tests under `charts/gguf-backend/tests`. Run `python3 spark.py --config config.example.json --work-dir /tmp/spark-render render --source-dir /path/to/prepared/source` for offline chart validation. A render does not establish a fresh-cluster deployment.

Regenerate `patches/stack-fixes.patch` from the owning source checkout, including its generated API files and tests. Never hand-edit generated CRDs or deepcopy code. Keep the source pin and patch digest synchronized.
