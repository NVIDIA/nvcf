# Shared routing chart

Compose the existing gateway stack and Pylon operator through local Helm dependencies. Do not copy their templates or change operator behavior here. Keep this chart free of model, GPU, RuntimeClass and storage requirements. Generated credentials must keep their identity across upgrades. Fail on missing credentials during upgrades, incompatible CRDs or overlapping operator watches.

From the repository root, run:

```sh
python3 -m unittest discover -s deploy/helm/llm-routing/tests -p 'test_helm_shared*.py' -v
```

Tests build dependencies in temporary copies and never access a live cluster. Build the gateway stack dependencies before this chart's dependencies when packaging. Do not commit generated dependency archives or private values.
