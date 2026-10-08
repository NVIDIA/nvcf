---
name: llm-add-recipe
description: >-
  Add or update an LLM routing model recipe through hardware research, Helm
  deployment, live verification and tuning, then propagate the verified recipe
  into chart metadata, examples, catalogs and packages. Use when adding a model
  recipe or qualifying a hardware profile.
license: Apache-2.0
compatibility: Requires an NVCF checkout, Python 3.11+, Helm and recipe test dependencies. Live qualification also requires authorized Kubernetes access and suitable GPUs.
author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
version: "1.0.0"
tags:
  - llm-routing
  - recipes
  - helm
tools:
  - Shell
  - Read
  - Write
metadata:
  internal: false
  author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
  version: "1.0.0"
  tags:
    - llm-routing
    - recipes
    - helm
  languages:
    - python
    - yaml
  frameworks:
    - helm
    - kubernetes
  domain: cloud-infrastructure
---

# Add an LLM routing recipe

## Instructions

Work in `deploy/helm/llm-routing` after reading its `AGENTS.md` and
`recipes/AGENTS.md`. The default outcome is a recipe deployed and verified on
the target cluster, with measured settings propagated to all maintained and
generated copies. Treat offline checks as preparation for that live loop.
If the user requests research or local edits only, stop at that boundary and
report qualification as pending. Confirm cluster access and authorization
before deployment. Reuse authorization already given for the target and scope.

### 1. Establish the artifact and hardware requirements

Identify the exact model, precision, target hardware, context length and
concurrency. Use primary model cards, checkpoint manifests and runtime
documentation to verify:

- An accessible checkpoint pinned to a full revision, its weight size and license.
- A compatible runtime image pinned by digest, including the target CPU
  architecture, GPU generation, model architecture and quantization support.
- Per-node memory for weights, KV cache, runtime overhead and startup peaks.
  On unified-memory systems, account for the operating system and other workloads.
- Disk capacity for downloaded files, temporary preparation and any offload.
- Distributed runtime support, partitioning and inter-node network requirements
  when the model spans nodes. Adding node memory totals does not establish fit.

Record sources and distinguish measured values from estimates. An access error
does not establish that a model is nonexistent. If artifacts or runtime support
remain unresolved, update [planned.json](../../../../deploy/helm/llm-routing/recipes/planned.json)
with dated evidence and `upstreamCandidates` where supported. Keep its deployable
profiles empty. Clear the corresponding planned entry when promoting a model
to an executable recipe so the exported IDs remain unique.

### 2. Choose an existing chart and check its execution path

The shared release owns the gateway, router, operator and CRD. Each recipe has
an independent model release. Keep preparation and serving in the model chart.

- SGLang: use [catalog.json](../../../../deploy/helm/llm-routing/recipes/catalog.json)
  as the maintained source. Check `recipes/charts/sglang/templates/_helpers.tpl`
  and `files/runtime.py` before reusing a profile. The runtime assigns one GPU
  per rank and the chart accepts one or two nodes. NVMe offload
  is specific to the model's supported embedding-offload implementation.
  Checkpoint validation expects model and tokenizer configuration, tokenizer
  data, and complete safetensors weights. Other layouts need runtime support.
- GGUF: use [glm-5.3](../../../../deploy/helm/llm-routing/recipes/glm-5.3/recipe.json)
  as a structural example. Check `recipes/charts/gguf-backend/templates/_automatic.tpl`
  and `files/automatic.py`. The current chart selects the first profile and
  constructs a leader plus one RPC worker. Additional topologies need chart
  and runtime changes before they can be advertised as executable profiles.
  Also update the exporter's GGUF license and guide mapping, which currently
  describes GLM, when adding another model family.

Reuse supported behavior and make required changes in its owning component.
Do not add a separate Python deployment path. Copying another model's flags,
resource sizes or validation records is not evidence of compatibility.

### 3. Fill the maintained metadata and synchronize chart copies

For SGLang, add a model to `recipes/catalog.json` using an existing entry as the
field reference:

- Identity: `id`, `name`, `releaseName` and `precision`.
- Artifacts: `repository`, `revision`, `image`, `runtime`, `license` and `source`.
- Each profile: `id`, `nodes`, `hardware`, `cpu`, `memoryGiB`, `memoryLimitGiB`,
  `cacheGiB`, `minFreeDiskGiB`, `maxContext`, `maxConcurrency`, `flags` and
  applicable offload or fabric requirements. Check effective defaults in the
  chart and exporter as well as the profile limits.

Copy the new or changed model into `recipes/charts/sglang/files/profiles.json`,
whose top-level keys are model IDs. Its model pins and profile contents must
match the source catalog. Existing `test_helm_sglang.py` coverage checks these
copies and lists the supported IDs explicitly. Extend it for the new recipe.

For GGUF, create `recipes/<recipe-id>/recipe.json`, `model.lock.json`,
`profiles.json` and `NOTICE`. The recipe owns release/served names, llama.cpp
revision, tuning and startup settings. The lock records checkpoint files and
integrity data. Profiles own the runtime image, hardware, role resources and
workload limits. Match context and concurrency to the selected memory-mode
tuning. Copy these files into
`recipes/charts/gguf-backend/files/recipes/<recipe-id>/` and extend the bundled
metadata equality and rendering tests for that recipe.

For either backend:

- Add a values example under `recipes/values/` for each deployable profile,
  with its recipe ID, profile name, node placeholders and required capabilities.
- Keep license notices in their maintained sources. Check model, runtime and
  derivative terms before distributing artifacts.
- Leave untested validation pending. Record an exact profile and workload only
  after its checks pass. Clear stale validation when model or runtime pins change.

The exporter generates the common index only. The packager copies existing
chart sources. Neither synchronizes the chart's recipe metadata for you.

### 4. Check locally and regenerate distribution files

Run from `deploy/helm/llm-routing`, using the existing recipe test dependencies:

```bash
python3 recipes/export_catalog.py
python3 recipes/export_catalog.py --check
cd recipes
python3 -m unittest discover -s tests -p test_catalog_export.py -v
python3 -m unittest discover -s tests -p test_committed_values.py -v
cd ..
```

Run the affected backend's metadata, startup, cache and rendering tests. Add
coverage for the new profile alongside the existing model tests.
Use its values example for `helm lint --strict` and `helm template`, then
inspect placement, resource requests, cache retention, readiness and endpoint
configuration. Keep rendered manifests in a private temporary directory.

After the chart copies and index agree:

```bash
PACKAGE_DIR="$(mktemp -d)"
bash dev-artifacts/package-charts.sh --output-dir "$PACKAGE_DIR"
```

Compare archive contents with maintained chart sources, verify `SHA256SUMS`,
and refresh the changed distribution files in `dev-artifacts/charts/`, including
the packaged index, guides and combined notice. Update the model inventory and
installation links when the supported profiles change. Offline checks establish
metadata and packaging consistency. They do not establish GPU memory fit or
successful inference.

### 5. Deploy, verify and adjust the recipe

Use an explicit Kubernetes context and inspect existing releases, allocations
and cache placement first. Reuse the existing shared stack. If it is absent,
install it once through the [Helm workflow](../../../../deploy/helm/llm-routing/README.md).
Preserve unrelated workloads, credentials, cache PVCs, PVs and downloaded files.
Record cache identities and placement before lifecycle tests. Establish a test
budget and the intended final release state with the user's requested scope.

Set `CONTEXT`, `RECIPE_ID`, `PROFILE_ID`, `MODEL_RELEASE`, `MODEL_CHART`,
`MODEL_VALUES` and `SERVED_MODEL_ID` to the selected context, metadata, packaged
chart and private values file. Use the catalog's release name for a new
installation, and inspect any existing release before upgrading it.

```bash
python3 llm.py --context "$CONTEXT" plan \
  --model "$RECIPE_ID" --profile "$PROFILE_ID" --verbose
```

Inspect the plan for eligible placement or an existing release. The command can
exit successfully while reporting blockers. Resolve those blockers before
installing. For an existing release, preserve its original cache placement.

```bash
helm --kube-context "$CONTEXT" upgrade --install "$MODEL_RELEASE" "$MODEL_CHART" \
  --namespace llm-stack --values "$MODEL_VALUES" --wait --timeout 120m &&
kubectl --context "$CONTEXT" --namespace llm-stack wait \
  --for=condition=Registered "inferenceendpoint/$MODEL_RELEASE" --timeout=5m &&
python3 llm.py --context "$CONTEXT" models &&
python3 recipes/verify.py --context "$CONTEXT" --model "$SERVED_MODEL_ID" \
  --output /path/to/private/model-results.json
```

Stop on a failed upgrade or verification. Before recording qualification, confirm
the deployed Helm revision and running pod configuration match the intended
chart, model/runtime pins and values. A still-serving older release does not
validate a failed upgrade.

Supply verified node capabilities to the planner and values when the profile
needs NVMe or a fabric link. Inspect preparation and serving logs, memory use
and restart counts. The verifier exercises gateway inference, streaming and
authentication. Exercise the intended context length and concurrency separately
before claiming that workload is validated.

If startup or inference fails, use the logs to correct model-specific flags,
memory reservations, workload limits or cache requirements in the maintained
recipe. Fix chart/runtime defects in their owning component. Synchronize the
chart copies, rerun affected local tests, rebuild the model chart and upgrade
the same model release with its retained cache. Repeat the failed live checks.
Keep memory and hardware guards active. Report a blocker if resolving it would
require unavailable hardware, access or changes outside the authorized scope.

Follow the [model lifecycle](../../../../deploy/helm/llm-routing/ADVANCED.md#independent-model-lifecycle)
to test stop/resume and uninstall/reinstall using the original cache nodes.
Verify endpoint withdrawal and restoration, unchanged cache identities and
weight reuse, and another model's availability when one is already running.
Restore the agreed final state and keep logs and private values outside Git.

### 6. Propagate the verified result

Update the maintained recipe's effective settings and existing validation
records with the model/runtime pins, test date, profile, context length,
concurrency and checks that actually passed. A short-prompt smoke test does not
qualify full context, performance or other hardware profiles. Keep untested
profiles pending and retain failed results accurately.

Repeat the chart-copy synchronization in step 3, regenerate `recipes/index.json`
with `python3 recipes/export_catalog.py`, then run the affected tests and
`--check`. Repackage through `dev-artifacts/package-charts.sh` and refresh the
packaged catalog, changed archives, guides, combined notice and checksums from
that output. Update the README validation summary to match the recorded result.
Verify the final archives match source and compare the rendered pod templates
with the tested deployment. SGLang hashes model metadata into its configuration
checksum, so even a validation-record update can trigger a rollout. If executable
content or pod templates changed after the live pass, upgrade with the final
package and rerun the affected readiness and inference checks. Metadata changes
that leave pod templates and runtime behavior unchanged need only local checks.

Finish with the tested model/profile, artifact pins, workload, check results,
remaining gaps and final release/cache state. Commit and publish only within
the user's requested scope.
