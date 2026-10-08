# Build application images and chart packages

Image preparation is a temporary development workflow. The supported installation uses the `llm-stack` shared Helm release in namespace `llm-stack`, alongside independent model releases. Building or preloading images does not install the gateway, operator or model workloads.

## Development images

Use the [development preparation command](../dev-images/README.md) when gateway, router, Pylon or operator sources change, or when another cluster needs local images. It builds from the current checkout, including local edits, and publishes matching image values and chart packages. Router and Pylon builds use Cargo profile `integration`.

Preloading mounts the selected nodes' containerd sockets through temporary import Jobs. It requires explicit opt-in. Keep archives, build evidence and node-specific configuration outside Git. A replacement node needs the images preloaded again when the pull policy is `Never`.

## Registry images

Set the four image repositories, tags and `IfNotPresent` pull policies in a private values file. Gateway and router fields are under `gatewayStack`, and operator and Pylon fields are under `operator`. Use the [shared values example](../charts/shared-stack/values.local.example.yaml) for field names. Every eligible node must be able to pull images for its architecture.

Once images are distributed, [upgrade the shared release](../ADVANCED.md#shared-stack-upgrades) with the new values. Preserve its existing credentials, TLS configuration and identity. Model runtime images and checkpoints are pinned independently by their recipes.

## Verification image

The shared chart's installation and upgrade checks use `python:3.12-alpine` with `IfNotPresent` pull policy. Distribute this image alongside the four application images. The image-loader server uses the same default image.

For offline installation, preload the verification image for every eligible architecture and set `verification.image.pullPolicy: Never`. For a registry mirror, use:

```yaml
verification:
  image:
    repository: registry.example.com/mirrors/python
    tag: 3.12-alpine
    pullPolicy: IfNotPresent
```

Set `verification.imagePullSecrets` to Kubernetes Secret references when the mirror requires credentials. The verification Job reads the installed gateway CA and caller key. Its checks work with an empty registry or an existing stack with models.

## Chart packages

Run `bash package-charts.sh --output-dir /path/to/fresh-output` from `deploy/helm/llm-routing` after preparing the local dependency archives. The package contains the shared chart, both recipe charts, catalog, guide snapshots, notices and checksums.

Run the [offline checks](../ADVANCED.md#local-validation) and compare the package with its source before publishing artifacts. Keep image values and the chart bundle consistent. Neither packaging nor image preparation changes the Helm ownership model.
