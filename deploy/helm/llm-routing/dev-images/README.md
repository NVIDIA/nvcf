# Prepare development images and charts

This temporary workflow prepares the shared image values and Helm packages used by the [installation guide](../README.md). Run it when application code or chart sources change, or another cluster needs images. Normal installers use the prepared artifacts without building locally.

## Requirements

Use Python 3.11+, Helm, kubectl and Docker with Buildx support. Docker needs access to base images and build dependencies. The selected Kubernetes cluster must have compatible ARM64 or AMD64 nodes using containerd. Image import Jobs need permission to mount the nodes' containerd sockets.

## Build images and package charts

Run from `deploy/helm/llm-routing`:

```bash
python3 dev-images/build.py --allow-containerd-import
```

The script:

1. Builds gateway, router, operator and Pylon from the checkout, including local edits, with fresh image tags.
2. Loads the images onto compatible nodes in the current kubeconfig context.
3. Packages the shared stack and both model charts.
4. Updates `dev-images/values.yaml` and `dev-images/charts/` after preparation succeeds. Failures preserve the previous shared artifacts.

Build outputs and reuse:

- `dev-images/charts/` contains three Helm archives, `index.json`, model notices and `SHA256SUMS`.
- Image archives, saved configuration and build evidence stay outside Git. Use `--output-dir` to choose their parent directory.
- The committed values contain image references and shared Helm settings, without credentials or workstation paths.
- Images use `Never` pull policy. Replacement nodes and evicted images need preloading again.
- Rebuilding adds image content to node caches. It does not remove old images, restart pods or upgrade releases.

Commit `dev-images/values.yaml` and `dev-images/charts/` together with the source changes, then follow the [installation guide](../README.md). The script does not commit or push.

See [advanced image distribution](../recipes/BUILDING.md) for registry images, saved-configuration retries and component rebuilds. Remove this temporary workflow when published images and charts replace it.
