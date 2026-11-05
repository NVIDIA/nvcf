# NATS wrapper chart

Keep prefilled images in the existing values. The catalog index is for CI;
Helm consumes the referenced settings directly. Preserve server, reloader
and box overrides and auth-callout secrets.

Run from the repository root. With a fresh Helm configuration, first register
the repository from `Chart.lock` and download the chart dependency:

```sh
helm repo add nats https://nats-io.github.io/k8s/helm/charts/
make -C deploy/helm/nats deps
make -C deploy/helm/nats test
NVCF_IMAGE_CATALOG_E2E=1 go test -C tools/docs-version-sync -run '^TestImageCatalogCharts/nats$' -count=1
```

The image test renders defaults and ordinary Helm overrides without a cluster.
Follow root QA guidance before live installation.
