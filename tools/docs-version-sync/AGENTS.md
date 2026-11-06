# Inventory and documentation tooling

This Go module owns stack inventories, umbrella manifests and documentation
synchronization. Reuse the existing image-reference parser and inventory
extraction when adding image sources.

Run from the repository root:

```sh
go test -C tools/docs-version-sync ./...
go vet -C tools/docs-version-sync ./...
NVCF_IMAGE_CATALOG_E2E=1 go test -C tools/docs-version-sync -run '^TestImageCatalogCharts$' -count=1
```

The chart test renders prefilled image defaults and ordinary Helm overrides.
It downloads pinned NATS and OpenBao dependencies into test-owned directories.
It does not create a cluster or verify registry publication. Keep the chart
probe and override helpers test-only; installation uses native Helm values.
