# Build application images

Build the four `linux/arm64` application images from the prepared source. Use the same Bash session, `SPARK_RECIPE`, `SPARK_WORK` and `spark` helper from [Configure and prepare](../README.md#configure-and-prepare).

## Requirements

- Docker with Buildx and ARM64 build capability, plus access to base images and build dependencies.
- The prepared source at `$SPARK_WORK/source`, including the bundled operator/chart patch. The operator needs the endpoint canary timing fields used by GLM.
- A fresh `images.tag` and your own `images.prefix` in the external configuration. Optional `images.repositories` entries override the repository for individual components. The example names are placeholders for images you build.
- Either write access to your chosen registry and pull access on every node where Pylon can schedule, or an authorized archive-transfer path to the nodes.

Router and Pylon use Cargo profile `integration` for functional validation. Qualify performance separately. The NVIDIA CUDA environment image is provisioned separately as described in the installation steps.

## Build and distribute

1. Build gateway, router, Pylon and operator images.

   ```bash
   spark build-images
   ```

2. Choose the distribution method matching `images.pullPolicy`.

   - Registry: set `IfNotPresent`, authenticate Docker to your registry, then push the images.

     ```bash
     spark push-images
     ```

   - Node preload: set `Never`, export the images, then follow [Import an archive](#import-an-archive). Replace the prefix and tag with your configuration. If you set repository overrides, use those complete image names instead.

     ```bash
     IMAGE_PREFIX=registry.example.com/team/llm-poc
     IMAGE_TAG=dev-1
     docker save -o "$SPARK_WORK/arm64-images.tar" \
       "$IMAGE_PREFIX/gateway:$IMAGE_TAG" "$IMAGE_PREFIX/router:$IMAGE_TAG" \
       "$IMAGE_PREFIX/pylon:$IMAGE_TAG" "$IMAGE_PREFIX/operator:$IMAGE_TAG"
     ```

3. Return to [Deploy in order](../README.md#deploy-in-order).

## Rebuild gateway or router

1. Edit the selected service in the prepared source and build it with a fresh tag.

   ```bash
   COMPONENT=gateway # Or router.
   NEW_TAG=dev-$(date -u +%Y%m%d%H%M%S)
   spark build-images --component "$COMPONENT" --tag "$NEW_TAG"
   ```

2. Distribute the image using your existing method.

   - Registry:

     ```bash
     spark push-images --component "$COMPONENT" --tag "$NEW_TAG"
     ```

   - Node preload: set `IMAGE_REPOSITORY` to the selected component's full configured repository, export it, then follow [Import an archive](#import-an-archive).

     ```bash
     IMAGE_REPOSITORY=registry.example.com/team/llm-poc/gateway
     docker save -o "$SPARK_WORK/arm64-images.tar" "$IMAGE_REPOSITORY:$NEW_TAG"
     ```

3. Keep `COMPONENT` and `NEW_TAG` set and return to [Update only gateway or router](../README.md#update-only-gateway-or-router).

## Import an archive

1. Copy the archive through your authorized transfer path to `containerd.archiveDirectory` on `containerd.archiveNode`. The configured `containerd.runAsUser` must be able to read the directory and archive. The example `/var/tmp/llm-poc-images` is a directory on that node.
2. Set `containerd.nodeNames` to every ARM64 node where Pylon can schedule. For runtimes other than K3s, configure the actual containerd socket and a compatible `ctr` client in the image-loader chart. Keep the archive below 1 GiB or enlarge the chart's importer storage limit.
3. Import the archive and inspect the retained import Jobs. For all four images:

   ```bash
   spark import-images --archive "$SPARK_WORK/arm64-images.tar" --allow-containerd-import
   ```

   For a gateway/router rebuild, import only the selected component on the configured control node:

   ```bash
   spark import-images --archive "$SPARK_WORK/arm64-images.tar" \
     --component "$COMPONENT" --tag "$NEW_TAG" --allow-containerd-import
   ```

Opt-in import Jobs access the selected nodes' containerd sockets with runtime administration privileges. The server exposes the dedicated archive directory and exits after acknowledgements. The importer checks the archive checksum and imports into the `k8s.io` namespace, preserving containerd configuration.
