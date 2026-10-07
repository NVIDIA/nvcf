# Build application images and chart packages

Normal installations use the committed [development image values](../dev-images/values.yaml) and the images already preloaded on the shared Spark nodes. Build gateway, router, Pylon and operator images only when application code changes or another cluster needs images. Builds include local edits. Router and Pylon builds use Cargo profile `integration`.

## Package charts for distribution

Use the output variables from the [installation guide](../README.md#0-package-charts). From `deploy/helm/llm-routing`, package shared infrastructure and both model charts with their recipe catalog:

```bash
bash package-charts.sh --output-dir "$LLM_CHARTS"
```

The output contains three Helm archives, `index.json`, model terms under `notices/`, and `SHA256SUMS`. Development packaging needs Helm and Python 3. The command builds local dependencies in a temporary directory and preserves source files and existing output files. Choose a fresh output directory for another build.

Distribute the archives, catalog and matching `dev-images/values.yaml` to the installer. Installation uses Helm and kubectl. Images must already be available in the configured registry or preloaded on every eligible node. The temporary `dev-images` workflow can be removed when registry images are published.

## Build shared stack images

Run from `deploy/helm/llm-routing` when a new application image set is needed:

```bash
python3 dev-images/build.py \
  --context "$LLM_CONTEXT" --allow-containerd-import
```

The script builds and exports all four images with fresh tags, then preloads every compatible node. Only after all imports succeed does it replace `dev-images/values.yaml`. A failed build or import preserves the previous values file. Commit and push the updated values with the source changes so teammates use the same image references. The script does not commit, push or upgrade Helm releases.

Each invocation uses a new work directory outside the checkout. Use `--output-dir /path/to/builds` to choose its parent directory. Archives, saved configuration and build evidence remain there. The committed values contain the image references, architecture and shared Helm configuration, without workstation paths or credentials.

Docker must be running with Buildx support for the selected architecture and access to base images and build dependencies. ARM64 and AMD64 are supported. The values use `Never` pull policy, so a replacement node or an evicted image must be preloaded before scheduling these workloads. Rebuilds add new image content to node caches and do not remove old images. Building does not restart running pods. Apply the new references with the shared Helm upgrade command in the installation guide.

The existing `build-shared-images.py` helper remains available for custom workflows and retries using a saved configuration. It writes `shared.values.yaml` in its own `--output-dir` and does not update the committed file. The target namespace may already exist. Reusing that helper's output directory rebuilds its saved tags.

Import Jobs run in a separate preparation namespace and mount the selected nodes' containerd sockets. Setup detects the K3s socket. For another containerd installation, set `config.containerd.socketPath` and compatible image-loader client settings in the saved `image-build-config.json`, then retry with `build-shared-images.py` and that saved output directory.

## Use registry images

Set the chart's four image repositories, tags and `IfNotPresent` pull policies in a private values file. The gateway and router fields are under `gatewayStack`, and the operator and Pylon fields are under `operator`. Use the [shared values example](../charts/shared-stack/values.local.example.yaml) for the field names. Every eligible node must be able to pull the images for its architecture. Use locally built images until registry images are published.

## Verification image

The shared chart's installation and upgrade checks use `python:3.12-alpine` with `IfNotPresent` pull policy. Distribute this image alongside the four application images. The image-loader server uses the same default image.

For an offline installation, preload the image for each eligible node's architecture and set `verification.image.pullPolicy: Never`. For a registry mirror, set these values in your Helm values file:

```yaml
verification:
  image:
    repository: registry.example.com/mirrors/python
    tag: 3.12-alpine
    pullPolicy: IfNotPresent
```

Add `verification.imagePullSecrets` with Kubernetes Secret references when the mirror requires credentials. The verification Job uses the installed gateway CA and caller key. Its checks support both an empty registry and an existing stack with models.

## Build for a combined installation

Run the commands below from `deploy/helm/llm-routing/recipes`. They use the saved configuration from [Configure](../ADVANCED.md#configure). Add `--work-dir DIR` to every command when using a non-default installation directory.

1. Build all four images.

   ```bash
   python3 recipe.py build-images
   ```

2. Choose the distribution method.

   - Registry: set `images.pullPolicy` to `IfNotPresent`, authenticate Docker with registry write credentials, then push. Every node where Pylon can schedule needs registry pull access.

     ```bash
     python3 recipe.py push-images
     ```

   - Node preload: set `images.pullPolicy` to `Never`, export the images, then [import the archive](#import-an-archive).

     ```bash
     python3 recipe.py export-images
     ```

3. Continue with [Deploy in order](../ADVANCED.md#deploy-in-order).

## Rebuild gateway or router

1. Edit the service in your checkout: `src/invocation-plane-services/llm-api-gateway` for gateway or `src/libraries/rust/stargate` for router. Build it with a fresh tag.

   ```bash
   COMPONENT=gateway # Or router.
   NEW_TAG=dev-$(date -u +%Y%m%d%H%M%S)
   python3 recipe.py build-images --component "$COMPONENT" --tag "$NEW_TAG"
   ```

2. Distribute the image using your existing method.

   - Registry:

     ```bash
     python3 recipe.py push-images --component "$COMPONENT" --tag "$NEW_TAG"
     ```

   - Node preload: export the image, then [import the archive](#import-an-archive).

     ```bash
     python3 recipe.py export-images --component "$COMPONENT" --tag "$NEW_TAG"
     ```

3. Keep `COMPONENT` and `NEW_TAG` set and continue with [Update only gateway or router](../ADVANCED.md#update-only-gateway-or-router).

## Import an archive

After `export-images`, upload and import the archive through Kubernetes. Keep each archive below 1 GiB. Import Jobs mount the selected nodes' containerd sockets to update their image caches.

For all four images:

```bash
python3 recipe.py import-images --allow-containerd-import
```

For a gateway/router rebuild, import only that component on the control node:

```bash
python3 recipe.py import-images \
  --component "$COMPONENT" --tag "$NEW_TAG" --allow-containerd-import
```

For runtimes other than K3s, configure `containerd.socketPath` and a compatible `ctr` client in the image-loader chart.
