# Model recipes

Follow the [LLM routing guide](../README.md) to install shared infrastructure once and deploy models directly with Helm. The [common catalog](index.json) includes both SGLang and llama.cpp recipes, precise per-node resources, pinned artifacts, supported profiles, workload limits, validation and local chart references.

`availability.deployable: true` identifies executable recipes, with validation recorded separately for each profile. Every catalog entry is currently executable. [planned.json](planned.json) records requested models without an executable recipe, with dated evidence and remaining gaps. It is empty now. A planned entry has no served model ID or installable profile.

## Committed values

Automatic recipes select pinned model and runtime settings from the chart. Supply the recipe and node placement, plus a profile and verified node capabilities when it requires NVMe or a fabric link. The defaults are runtime class `nvidia`, storage class `local-path` and shared CA `llm-gateway-stack-ca`. The files below are optional Helm `--values` examples. Replace their nodes and capability facts with your own. These profiles target Linux ARM64 GB10 (Spark) or GB300 (Station) nodes.

| Recipe | Values file | Installation |
| --- | --- | --- |
| Qwen3.5-4B BF16, Station | [qwen3.5-4b-station.yaml](values/qwen3.5-4b-station.yaml) | [Automatic, one node](../README.md#2-install-a-model) |
| Nemotron Nano 12B v2 BF16, Station | [nemotron-nano-12b-v2-station.yaml](values/nemotron-nano-12b-v2-station.yaml) | [Automatic, one node](../README.md#2-install-a-model) |
| Nemotron 3.5 Lightning NVFP4, Station | [nemotron-3.5-lightning-station.yaml](values/nemotron-3.5-lightning-station.yaml) | [Automatic, one node](../README.md#2-install-a-model) |
| DeepSeek V4 Flash FP4/FP8, Station | [deepseek-v4-flash-station.yaml](values/deepseek-v4-flash-station.yaml) | [Automatic, two nodes with a fabric link](../README.md#2-install-a-model) |
| GLM-5.3 UD-IQ2_M, Station | [glm-5.3-station.yaml](values/glm-5.3-station.yaml) | [Automatic, two nodes](../ADVANCED.md#helm-glm-recipe) |
| Qwen3.8-27B FP8, Station | [qwen3.8-27b-station.yaml](values/qwen3.8-27b-station.yaml) | [Automatic, one node](../README.md#2-install-a-model) |
| Qwen3.8-27B FP8 | [qwen3.8-27b.yaml](values/qwen3.8-27b.yaml) | [Automatic, one node](../README.md#2-install-a-model) |
| Qwen3.8-27B NVIDIA NVFP4 | [qwen3.8-27b-nvfp4.yaml](values/qwen3.8-27b-nvfp4.yaml) | [Automatic, one node](../README.md#2-install-a-model) |
| Qwen3.5-4B BF16 | [qwen3.5-4b.yaml](values/qwen3.5-4b.yaml) | [Automatic, one node](../README.md#2-install-a-model). Gateway checks passed at context 8,192 and concurrency 1 with a 2,048-token completion budget. |
| Nemotron Nano 12B v2 BF16 | [nemotron-nano-12b-v2.yaml](values/nemotron-nano-12b-v2.yaml) | [Automatic, one node](../README.md#2-install-a-model). Short-prompt gateway checks passed at context 8,192 and concurrency 1. |
| Nemotron 3.5 Lightning NVFP4 | [nemotron-3.5-lightning.yaml](values/nemotron-3.5-lightning.yaml) | [Automatic, one node](../README.md#2-install-a-model). Short-prompt gateway checks passed at context 8,192 and concurrency 1. |
| DeepSeek V4 Flash | [deepseek-v4-flash.yaml](values/deepseek-v4-flash.yaml) | [Automatic, two nodes](../README.md#2-install-a-model). Short-prompt gateway checks passed at context 8,192 and concurrency 1. Needs a verified 200 Gbps link. |
| GLM-5.3 UD-IQ2_M, three nodes | [glm-5.3-x3.yaml](values/glm-5.3-x3.yaml) | [Automatic, three nodes](../ADVANCED.md#helm-glm-recipe). Short-prompt gateway checks passed at context 2,048 and concurrency 1. |
| GLM-5.3 UD-IQ2_M, two nodes | [glm-5.3.yaml](values/glm-5.3.yaml) | [Automatic, two nodes](../ADVANCED.md#helm-glm-recipe). Latest trials stopped at the host-memory guard. |
| Qwen3.8-Flash-Next NVFP4, NVMe offload | [qwen3.8-flash-next-nvme.yaml](values/qwen3.8-flash-next-nvme.yaml) | [Automatic, one node](../ADVANCED.md#helm-flash-next-recipes). Startup and short-prompt gateway checks passed at context 8,192 and concurrency 1. |
| Qwen3.8-Flash-Next NVFP4, tensor parallel | [qwen3.8-flash-next-tp2.yaml](values/qwen3.8-flash-next-tp2.yaml) | [Automatic, two nodes](../ADVANCED.md#helm-flash-next-recipes). Live validation pending. |
| Qwen3.8-Flash-Next NVFP4, Station | [qwen3.8-flash-next-station.yaml](values/qwen3.8-flash-next-station.yaml) | [Automatic, one node](../ADVANCED.md#helm-flash-next-recipes). Short-prompt gateway checks passed at context 8,192 and concurrency 1. |

Planned and unavailable catalog entries have no installable values file. Their missing artifacts or chart support must be resolved first.

See [build and package](../dev-artifacts/README.md) for development image preparation and local chart archives.

The charts own model workloads, persistent caches and InferenceEndpoints. Shared routing and credentials belong to the shared infrastructure release. Kubernetes manages preparation, startup and readiness. Choose distinct available GPU nodes from a supported hardware profile.

## Catalog maintenance

Every recipe needs a `description`, a short single-line ASCII summary that the demo UI shows under the model name. SGLang entries set it in `catalog.json` and GGUF recipes set it in `recipe.json`. The exporter rejects an invalid value and a test requires one on every recipe. Recipes that share a card name share one description. Recipe metadata remains in [catalog.json](catalog.json) for SGLang and [glm-5.3](glm-5.3/profiles.json) for GGUF. [planned.json](planned.json) owns requested models without an executable recipe. Regenerate the common index after changing recipe metadata:

```bash
python3 export_catalog.py
python3 export_catalog.py --check
```

Resolve each deployment's `chart.localPath` and `guide` relative to its containing `index.json`. The source index points to chart directories and repository guides. `dev-artifacts/package-charts.sh` rewrites these fields to the packaged chart archives and included guide snapshots. `chart.archive` and `licenseNotice` identify files relative to the package directory. `licenseNotice` is `NOTICE` when terms are recorded and `null` otherwise. The packager includes each distinct maintained notice once in that file, including terms for planned upstream candidates. Guide snapshots describe the repository workflow, so their commands and links to other source files still require the checkout.

Chart packages include the same pinned metadata. Tests check synchronization. The common index records runtime smoke tests separately from automatic Helm lifecycle validation and points to local archives until publication is configured.

Each exported `deployment.chart` includes `repository: null` and `publication: "local"` by default. To describe a published chart, set optional `chartRepository` on its SGLang model in `catalog.json` or its GGUF `recipe.json`, then regenerate the index. Supply the full OCI repository ending in the chart name, without a tag or digest; the exported chart version selects the tag. The exporter emits that address as `repository` with `publication: "oci"` and retains the local paths. This setting neither publishes a chart nor checks registry availability. Consumers must handle a null repository as local-only. The planner continues using local charts unless `--chart-source` is supplied.

Use `../llm.py` for recipe discovery, placement planning, model discovery and chat. Use the [independent model lifecycle](../ADVANCED.md#independent-model-lifecycle) to upgrade, verify, stop, resume, remove and reinstall each recipe through its Helm release. See [Advanced deployment and configuration](../ADVANCED.md) for verification and recovery.
