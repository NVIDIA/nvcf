# Build application images

Build gateway, router, Pylon and operator images for `linux/arm64` from your checkout, including local edits. Run these commands from `deploy/helm/llm-routing/recipes`.

## Requirements

- A running Docker engine with Buildx, ARM64 build capability and access to base images and build dependencies.
- For registry distribution, set `images.prefix` and a fresh `images.tag` in the configuration. `images.repositories` overrides the repository for individual components.

Router and Pylon builds use Cargo profile `integration`.

## Build for a new shared stack

Create a private build configuration from the example. Choose a new directory for these build settings:

```bash
export IMAGE_WORK="$HOME/.local/state/nvcf/llm-images"
mkdir -p "$IMAGE_WORK"
cp config.example.json "$IMAGE_WORK/config.json"
```

Set `images.prefix` to your registry path and `images.tag` to a fresh tag in that file. Build and push use only local sources and Docker. The example node and model settings can stay as supplied for these commands.

Authenticate Docker with registry write credentials, then build and push all four images:

```bash
python3 recipe.py --work-dir "$IMAGE_WORK" build-images
python3 recipe.py --work-dir "$IMAGE_WORK" push-images
```

In your private shared stack configuration, set each `images.COMPONENT.repository` to `PREFIX/COMPONENT` for `gateway`, `router`, `pylon` and `operator`. Set their `tag` to the build tag and `pullPolicy` to `IfNotPresent`. For example, `images.prefix` of `registry.example.com/team/llm-poc` builds the gateway at `registry.example.com/team/llm-poc/gateway:TAG`. If you set `images.repositories`, use those component overrides instead.

The routing node and every node where Pylon can schedule need registry pull access and ARM64 support for these images. Continue with [Install the shared stack](../README.md#1-install-the-shared-stack).

## Build for a combined installation

These commands use the saved configuration from [Configure](../ADVANCED.md#configure). Add `--work-dir DIR` to every command when using a non-default installation directory.

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
