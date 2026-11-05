# cert-manager wrapper chart

Expose all five image settings in the wrapper values, including the indirect
ACME solver. Empty tags inherit the vendored chart appVersion. The catalog
index is for CI; Helm consumes the referenced settings directly.

Run from the repository root:

```sh
helm lint deploy/helm/cert-manager
NVCF_IMAGE_CATALOG_E2E=1 go test -C tools/docs-version-sync -run '^TestImageCatalogCharts/cert-manager$' -count=1
```

The image test renders defaults and ordinary Helm overrides without a cluster.
Follow root QA guidance before live installation. Preserve external-controller
mode and explicit repository/tag/digest overrides.
