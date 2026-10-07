# Model recipe tools

Use the [LLM routing runbook](../README.md) to install shared infrastructure with an empty registry, then choose a model flow:

- [`recipe.py`](recipe.py) installs a llama.cpp GGUF recipe, optionally bound to an existing shared stack. Each folder with a `recipe.json`, such as [`glm-5.3`](glm-5.3/recipe.json), owns its model lock, runtime settings and memory rules. `sizing.py` derives hardware requirements for single-node and split-node placement. The [advanced guide](../ADVANCED.md) covers combined installation and maintenance.
- [`recipes.py`](recipes.py) plans and deploys independent SGLang releases from [`catalog.json`](catalog.json), supported hardware profiles and current cluster capacity. Follow [Independent Qwen models](../ADVANCED.md#qwen-catalog-configuration) for FP8/NVFP4 precision deployments, shared-stack binding and lifecycle verification.

The directory contains both tools, their backend charts, clients and regression tests. Shared infrastructure belongs to [`../stack.py`](../stack.py). Model recipes own their runtime, cache and endpoint resources.
