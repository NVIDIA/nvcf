# NATS chart

This wrapper consumes the upstream NATS chart pinned in `Chart.lock`.
Keep image defaults and nonroot settings in `values.yaml`. Do not edit
cached dependencies under `charts/`.

Use two-space YAML indentation. Preserve the upstream chart value nesting
and follow the existing Go template whitespace conventions in Helm templates.

Run from the repository root:

```sh
helm repo add nats https://nats-io.github.io/k8s/helm/charts/
helm dependency build deploy/helm/nats --skip-refresh
make -C deploy/helm/nats test
bash deploy/stacks/self-managed/tests/nats-upstream-images.sh
```

Image changes also affect the self-managed stack values, release inventory,
version catalog, and generated manifest. Keep these aligned. The box image
uses `tag: version@sha256:index-digest` so rendered inventories retain both
the version and immutable multi-platform identity. Explicit mirror overrides
use the same chart image keys.
