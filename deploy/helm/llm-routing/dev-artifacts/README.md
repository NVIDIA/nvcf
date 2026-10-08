# Prepare development images and charts

This directory contains temporary image building, preloading, chart packaging and distribution artifacts used by the [installation guide](../README.md). The shared and model chart sources, recipe catalog and runtime commands remain outside it. Remove this directory when published images and charts replace these artifacts.

## Requirements

Use Python 3.11+, Helm, kubectl and Docker with Buildx support. Docker needs access to base images and build dependencies. The selected Kubernetes cluster must have compatible ARM64 or AMD64 nodes using containerd. Image import Jobs need permission to mount the nodes' containerd sockets.

## Build images and package charts

Run from `deploy/helm/llm-routing`:

```bash
python3 dev-artifacts/build.py --allow-containerd-import
```

The script:

1. Builds gateway, router, operator and Pylon from the checkout, including local edits, with fresh image tags. Router and Pylon use Cargo profile `integration`. It also pulls the digest-pinned Python verification image and tags it for the same local distribution.
2. Loads the images onto compatible nodes in the current kubeconfig context.
3. Packages the shared stack and both model charts.
4. Updates `dev-artifacts/values.yaml` and `dev-artifacts/charts/` after preparation succeeds. Failures preserve the previous shared artifacts.

Build outputs and reuse:

- `dev-artifacts/charts/` contains three Helm archives, `index.json`, one combined `NOTICE` and `SHA256SUMS`.
- Image archives, saved configuration and build evidence stay outside Git. Use `--output-dir` to choose their parent directory.
- The committed values contain image references and shared Helm settings, without credentials or workstation paths.
- Images use `Never` pull policy. Replacement nodes and evicted images need preloading again.
- Rebuilding adds image content to node caches. It does not remove old images, restart pods or upgrade releases.

To retry a failed preparation, pass `--resume-from BUILD_DIR` using the printed build-state directory, with the same context and namespace and `--allow-containerd-import`. The helper reuses its private image configuration and node identities, then stages chart packages again.

Commit `dev-artifacts/values.yaml` and `dev-artifacts/charts/` together with the source changes, then follow the [installation guide](../README.md). The script does not commit or push.

## Registry images

Set the four image repositories, tags and `IfNotPresent` pull policies in a private values file. Gateway and router fields are under `gatewayStack`, and operator and Pylon fields are under `operator`. Use the [shared values example](../charts/shared-stack/values.local.example.yaml) for field names. Every eligible node must be able to pull images for its architecture.

Once images are distributed, [upgrade the shared release](../ADVANCED.md#shared-stack-upgrades) with the new values. Preserve its existing credentials, TLS configuration and identity. Model runtime images and checkpoints are pinned independently by their recipes.

## Verification image

The shared chart's installation and upgrade checks use Python. The development build preloads a digest-pinned `python:3.12-alpine` alongside the four application images. It publishes `verification.image` with a local tag, `Never` pull policy and the matching architecture selector. Re-run image preparation to add this image to an older development bundle; chart packaging alone does not preload images.

Without development values, the shared chart defaults to `python:3.12-alpine` with `IfNotPresent`. For a registry mirror, use:

```yaml
verification:
  image:
    repository: registry.example.com/mirrors/python
    tag: 3.12-alpine
    pullPolicy: IfNotPresent
```

Set `verification.imagePullSecrets` to Kubernetes Secret references when the mirror requires credentials. The verification Job reads the installed gateway CA and caller key. Its checks work with an empty registry or an existing stack with models.

## Chart packages

Run `bash dev-artifacts/package-charts.sh --output-dir /path/to/fresh-output` from `deploy/helm/llm-routing` after synchronizing recipe metadata and placement sources. The package contains the shared chart, both recipe charts, catalog, guide snapshots, one combined `NOTICE` and checksums.

The packager checks the maintained placement copies, then stages local chart dependencies without changing the source tree. Run the [offline checks](../ADVANCED.md#local-validation) and compare packages with their sources before publishing artifacts. Keep image values and chart bundles consistent.

Run development tests from `deploy/helm/llm-routing`:

```bash
python3 -m unittest discover -s dev-artifacts/tests -v
```

These tests mock image builds and cluster access. Permanent recipe and CLI tests run without this directory.
