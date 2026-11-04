# cert-manager wrapper chart

Preserve vendored defaults. The wrapper catalog references all five image
paths, including the controller's indirect ACME solver. Tags fall back to
the dependency chart appVersion.

Run from the repository root:

```sh
helm lint deploy/helm/cert-manager
NVCF_IMAGE_CATALOG_E2E=1 go test -C tools/docs-version-sync -run '^TestImageCatalogCharts/cert-manager$' -count=1
```

The catalog test renders source and mirror values without a cluster.
Follow root QA guidance before live installation. Keep external-controller
mode and explicit repository/tag/digest overrides.
