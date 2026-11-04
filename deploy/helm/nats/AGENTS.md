# NATS wrapper chart

Keep image versions in the existing values. Catalog entries reference those
paths. Preserve server, reloader and box overrides and auth-callout secrets.

Run from the repository root:

```sh
make -C deploy/helm/nats test
NVCF_IMAGE_CATALOG_E2E=1 go test -C tools/docs-version-sync -run '^TestImageCatalogCharts/nats$' -count=1
```

The catalog test renders source and mirror values without a cluster.
Follow root QA guidance before live installation.
