# Model recipes

Follow the [LLM routing guide](../README.md) to install shared infrastructure once and deploy models directly with Helm. The [common catalog](index.json) includes both SGLang and llama.cpp recipes, precise per-node resources, pinned artifacts, supported profiles, workload limits, validation and local chart references.

The catalog contains seven model families and eight entries. `availability.deployable: true` identifies executable recipes, with validation recorded separately for each profile. Nemotron 5 Nano 12B, Nemotron 5 Super 49B and Qwen3.8-4B are unavailable because their exact public artifacts were not verified. DeepSeek V4 Flash is planned, with verified upstream checkpoint and runtime candidates under `upstreamCandidates`. These four entries have no served model ID or installable profile. [planned.json](planned.json) records the dated evidence and remaining gaps.

- [SGLang chart](charts/sglang/values.example.yaml): automatic Qwen3.8-27B FP8 and NVFP4 installation on a selected compatible node.
- [GGUF chart](charts/gguf-backend/values.yaml): automatic two-node GLM-5.3 UD-IQ2_M installation. See [GLM configuration](../ADVANCED.md#helm-glm-recipe).
- [Build and package](BUILDING.md): development image preparation and local chart archives.

The charts own model workloads, persistent caches and InferenceEndpoints. Shared routing and credentials belong to the shared infrastructure release. Kubernetes manages preparation, startup and readiness. Choose distinct available GPU nodes from a supported hardware profile.

## Catalog maintenance

Recipe metadata remains in [catalog.json](catalog.json) for SGLang and [glm-5.3](glm-5.3/profiles.json) for GGUF. [planned.json](planned.json) owns requested models without an executable recipe. Regenerate the common index after changing recipe metadata:

```bash
python3 export_catalog.py
python3 export_catalog.py --check
```

Chart packages include the same pinned metadata. Tests check synchronization. The common index records runtime smoke tests separately from automatic Helm lifecycle validation and points to local archives until publication is configured.

The existing `recipe.py`, `recipes.py` and `sizing.py` workflows remain available for advanced placement planning, combined installations and phased operation. See [Advanced deployment and configuration](../ADVANCED.md).
