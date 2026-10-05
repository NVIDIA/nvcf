# Build application images

Build gateway, router, Pylon and operator images for `linux/arm64` from your checkout, including local edits. Run these commands from `deploy/helm/llm-routing/spark`.

## Requirements

- A running Docker engine with Buildx, ARM64 build capability and access to base images and build dependencies.
- Helm dependencies prepared with `python3 spark.py prepare`.
- For registry distribution, set `images.prefix` and a fresh `images.tag` in the configuration. `images.repositories` overrides the repository for individual components.

Router and Pylon builds use Cargo profile `integration`.

## Build and distribute

1. Build all four images.

   ```bash
   python3 spark.py build-images
   ```

2. Choose the distribution method.

   - Registry: set `images.pullPolicy` to `IfNotPresent`, authenticate Docker with registry write credentials, then push. Every node where Pylon can schedule needs registry pull access.

     ```bash
     python3 spark.py push-images
     ```

   - Node preload: set `images.pullPolicy` to `Never`, export the images, then [import the archive](#import-an-archive).

     ```bash
     python3 spark.py export-images
     ```

3. Continue with [Deploy in order](../README.md#deploy-in-order).

## Rebuild gateway or router

1. Edit the service in your checkout: `src/invocation-plane-services/llm-api-gateway` for gateway or `src/libraries/rust/stargate` for router. Build it with a fresh tag.

   ```bash
   COMPONENT=gateway # Or router.
   NEW_TAG=dev-$(date -u +%Y%m%d%H%M%S)
   python3 spark.py build-images --component "$COMPONENT" --tag "$NEW_TAG"
   ```

2. Distribute the image using your existing method.

   - Registry:

     ```bash
     python3 spark.py push-images --component "$COMPONENT" --tag "$NEW_TAG"
     ```

   - Node preload: export the image, then [import the archive](#import-an-archive).

     ```bash
     python3 spark.py export-images --component "$COMPONENT" --tag "$NEW_TAG"
     ```

3. Keep `COMPONENT` and `NEW_TAG` set and continue with [Update only gateway or router](../README.md#update-only-gateway-or-router).

## Import an archive

After `export-images`, upload and import the archive through Kubernetes. Keep each archive below 1 GiB. Import Jobs mount the selected nodes' containerd sockets to update their image caches.

For all four images:

```bash
python3 spark.py import-images --allow-containerd-import
```

For a gateway/router rebuild, import only that component on the control node:

```bash
python3 spark.py import-images \
  --component "$COMPONENT" --tag "$NEW_TAG" --allow-containerd-import
```

For runtimes other than K3s, configure `containerd.socketPath` and a compatible `ctr` client in the image-loader chart.
