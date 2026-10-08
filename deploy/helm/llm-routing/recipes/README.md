# Model recipes

Follow the [LLM routing guide](../README.md) to install shared infrastructure once and deploy models directly with Helm. The [common catalog](index.json) includes both SGLang and llama.cpp recipes, precise per-node resources, pinned artifacts, supported profiles, workload limits, validation and local chart references.

The catalog contains seven model families and eight entries. `availability.deployable: true` identifies executable recipes, with validation recorded separately for each profile. Nemotron 5 Nano 12B, Nemotron 5 Super 49B and Qwen3.8-4B are unavailable because their exact public artifacts were not verified. DeepSeek V4 Flash is planned, with verified upstream checkpoint and runtime candidates under `upstreamCandidates`. These four entries have no served model ID or installable profile. [planned.json](planned.json) records the dated evidence and remaining gaps.

## Committed values

Automatic recipes select pinned model and runtime settings from the chart. Supply the recipe and node placement, plus a profile and verified node capabilities for Flash-Next. The defaults are runtime class `nvidia`, storage class `local-path` and shared CA `llm-gateway-stack-ca`. The files below are optional Helm `--values` examples. Replace their nodes and capability facts with your own. These profiles target Linux ARM64 GB10 nodes.

| Recipe | Values file | Installation |
| --- | --- | --- |
| Qwen3.8-27B FP8 | [qwen3.8-27b.yaml](values/qwen3.8-27b.yaml) | [Automatic, one node](../README.md#2-install-a-model) |
| Qwen3.8-27B NVIDIA NVFP4 | [qwen3.8-27b-nvfp4.yaml](values/qwen3.8-27b-nvfp4.yaml) | [Automatic, one node](../README.md#2-install-a-model) |
| GLM-5.3 UD-IQ2_M | [glm-5.3.yaml](values/glm-5.3.yaml) | [Automatic, two nodes](../ADVANCED.md#helm-glm-recipe). Latest trial stopped at the host-memory guard. |
| Qwen3.8-Flash-Next NVFP4, NVMe offload | [qwen3.8-flash-next-nvme.yaml](values/qwen3.8-flash-next-nvme.yaml) | [Automatic, one node](../ADVANCED.md#helm-flash-next-recipes). Startup and short-prompt gateway checks passed at context 8,192 and concurrency 1. |
| Qwen3.8-Flash-Next NVFP4, tensor parallel | [qwen3.8-flash-next-tp2.yaml](values/qwen3.8-flash-next-tp2.yaml) | [Automatic, two nodes](../ADVANCED.md#helm-flash-next-recipes). Live validation pending. |

Planned and unavailable catalog entries have no installable values file. Their missing artifacts or chart support must be resolved first.

See [build and package](../dev-artifacts/README.md) for development image preparation and local chart archives.

The charts own model workloads, persistent caches and InferenceEndpoints. Shared routing and credentials belong to the shared infrastructure release. Kubernetes manages preparation, startup and readiness. Choose distinct available GPU nodes from a supported hardware profile.

## Catalog maintenance

Recipe metadata remains in [catalog.json](catalog.json) for SGLang and [glm-5.3](glm-5.3/profiles.json) for GGUF. [planned.json](planned.json) owns requested models without an executable recipe. Regenerate the common index after changing recipe metadata:

```bash
python3 export_catalog.py
python3 export_catalog.py --check
```

Resolve each deployment's `chart.localPath` and `guide` relative to its containing `index.json`. The source index points to chart directories and repository guides. `dev-artifacts/package-charts.sh` rewrites these fields to the packaged chart archives and included guide snapshots. `chart.archive` and `licenseNotice` identify files relative to the package directory. Guide snapshots describe the repository workflow, so their commands and links to other source files still require the checkout.

Chart packages include the same pinned metadata. Tests check synchronization. The common index records runtime smoke tests separately from automatic Helm lifecycle validation and points to local archives until publication is configured.

Each exported `deployment.chart` includes `repository: null` and `publication: "local"` by default. To describe a published chart, set optional `chartRepository` on its SGLang model in `catalog.json` or its GGUF `recipe.json`, then regenerate the index. Supply the full OCI repository ending in the chart name, without a tag or digest; the exported chart version selects the tag. The exporter emits that address as `repository` with `publication: "oci"` and retains the local paths. This setting neither publishes a chart nor checks registry availability. Consumers must handle a null repository as local-only. The planner continues using local charts unless `--chart-source` is supplied.

Use `../llm.py` for recipe discovery, placement planning, model discovery and chat. Use the [independent model lifecycle](../ADVANCED.md#independent-model-lifecycle) to upgrade, verify, stop, resume, remove and reinstall each recipe through its Helm release. See [Advanced deployment and configuration](../ADVANCED.md) for verification and recovery.
