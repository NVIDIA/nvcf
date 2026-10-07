# Build application images

Build gateway, router, Pylon and operator images from your checkout, including local edits. Router and Pylon builds use Cargo profile `integration`.

## Build for a new shared stack

Run from `deploy/helm/llm-routing`:

```bash
export LLM_ROUTING_CONTEXT=YOUR_CONTEXT
python3 stack.py install --build-images
```

This temporary development flag builds the images for the selected routing node's architecture, exports an archive and imports it onto compatible nodes before installing the shared stack. Docker must be running with Buildx support for the target architecture and access to base images and build dependencies. The command records fresh local tags and `Never` pull policy in the saved configuration.

Import Jobs run in a separate image preparation namespace and mount the selected nodes' containerd sockets. Setup detects the K3s containerd socket. For another containerd installation, initialize the configuration with `stack.py init` and set `containerd.socketPath` and compatible image-loader client settings before installation. See [Shared stack settings](../ADVANCED.md#shared-stack-settings) for configuration paths.

## Use registry images

Configure `images.COMPONENT.repository`, `tag` and `pullPolicy` for `gateway`, `router`, `pylon` and `operator` in the shared configuration. Use published images for the target architecture and grant every node where Pylon can schedule registry pull access. Install with:

```bash
python3 stack.py install
```

The bundled registry references are placeholders. When published images become the default, the main guide can use this command directly.

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
