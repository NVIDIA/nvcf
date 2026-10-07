# Build application images and chart packages

Build gateway, router, Pylon and operator images from your checkout, including local edits. Router and Pylon builds use Cargo profile `integration`.

## Package charts for distribution

From `deploy/helm/llm-routing`, package shared infrastructure and both model charts with their recipe catalog:

```bash
./package-charts.sh --output-dir /tmp/llm-chart-packages
```

The output contains three Helm archives, `index.json`, model terms under `notices/`, and `SHA256SUMS`. Development packaging needs Helm and Python 3. The command builds local dependencies in a temporary directory and preserves source files and existing output files. Choose a fresh output directory for another build.

Distribute the archives and catalog to the installer. Installation uses Helm and kubectl. Images must already be available in the configured registry or preloaded on every eligible node. Build and export development images with the commands below until registry images are published.

## Build for a new shared stack

Run this developer prerequisite from `deploy/helm/llm-routing` before the target namespace exists:

```bash
python3 build-shared-images.py \
  --namespace llm-stack --output-dir /tmp/llm-images --allow-containerd-import
```

The command resolves the current kubeconfig context once, builds and exports the four images, preloads every compatible node, and writes `/tmp/llm-images/shared.values.yaml` for the [Helm installation](../README.md). Docker must be running with Buildx support for the selected architecture and access to base images and build dependencies. Use `--context NAME` to choose another context and `--control-node NODE` to select the build architecture. ARM64 and AMD64 are supported.

The saved configuration keeps fresh image tags and node UIDs stable on retry. The emitted values select that architecture and use `Never` pull policy. Existing unrelated output files are preserved. This command prepares a fresh stack namespace. Existing combined installations can use the component rebuild commands below with their saved configuration.

Import Jobs run in a separate preparation namespace and mount the selected nodes' containerd sockets. Setup detects the K3s socket. For another containerd installation, set `config.containerd.socketPath` and compatible image-loader client settings in the saved `image-build-config.json`, then retry the command.

## Use registry images

Set the chart's four image repositories, tags and `IfNotPresent` pull policies in a private values file. The gateway and router fields are under `gatewayStack`, and the operator and Pylon fields are under `operator`. Use the [shared values example](../charts/shared-stack/values.local.example.yaml) for the field names. Every eligible node must be able to pull the images for its architecture. Use locally built images until registry images are published.

## Verification image

The shared chart's installation and upgrade checks use `python:3.12-alpine` with `IfNotPresent` pull policy. Distribute this image alongside the four application images. The image-loader server uses the same default image.

For an offline installation, preload the image for each eligible node's architecture and set `verification.image.pullPolicy: Never`. For a registry mirror, set these values in `shared.values.yaml`:

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
