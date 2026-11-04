# Inventory and documentation tooling

This Go module owns stack inventories, umbrella manifests and chart image
catalog inspection and mirror overrides. Reuse the existing image-reference
parser and inventory extraction when adding image sources.

Run from the repository root:

```sh
go test -C tools/docs-version-sync ./...
go vet -C tools/docs-version-sync ./...
NVCF_IMAGE_CATALOG_E2E=1 go test -C tools/docs-version-sync -run '^TestImageCatalogCharts$' -count=1
```

The catalog render test requires Helm and downloads pinned NATS and OpenBao
dependencies into test-owned directories. It does not create a cluster or
contact image registries. Fixture checks are separate from publication QA.

`images mirror` compares AMD64 and ARM64 source and destination manifests
with skopeo. Keep verification mandatory and emit overrides only after the
rendered chart matches the mapping. Do not log computed chart values, which
can contain credentials.
