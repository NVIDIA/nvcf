# Prepare development images and charts

This temporary workflow prepares the shared image values and Helm packages used by the [installation guide](../README.md). Run it when application code or chart sources change, or another cluster needs images. Normal installers use the prepared artifacts without building locally.

## Requirements

Use Python 3.11+, Helm, kubectl and Docker with Buildx support. Docker needs access to base images and build dependencies. The selected Kubernetes cluster must have compatible ARM64 or AMD64 nodes using containerd. Image import Jobs need permission to mount the nodes' containerd sockets.

## Build images and package charts

Run from `deploy/helm/llm-routing`:

```bash
python3 dev-images/build.py \
  --context your-context --allow-containerd-import
```

This command builds gateway, router, operator and Pylon from the checkout, including local edits. It uses fresh image tags, preloads compatible nodes and packages the shared stack and both model charts. After preparation succeeds, it updates `dev-images/values.yaml` and the packages in `dev-images/charts/`. It preserves the previous shared values if preparation fails.

Commit the updated `dev-images/values.yaml` and `dev-images/charts/` with the source changes so teammates receive a matching set. The script does not commit, push or upgrade Helm releases. Continue with the main installation guide to apply the new images.

Each invocation keeps image archives, saved configuration and build evidence in a new directory outside the checkout. Use `--output-dir /path/to/builds` to choose its parent directory. The committed values contain image references, architecture and shared Helm configuration, without workstation paths or credentials.

The values use `Never` pull policy. A replacement node or an evicted image must be preloaded before scheduling these workloads. Rebuilds add new image content to node caches and do not remove old images. Building does not restart running pods.

The committed `dev-images/charts/` directory includes three Helm archives, `index.json`, model terms under `notices/`, and `SHA256SUMS`. See [advanced image distribution](../recipes/BUILDING.md) for registry images, saved-configuration retries and component rebuilds. Remove this temporary workflow when published images and charts replace it.
