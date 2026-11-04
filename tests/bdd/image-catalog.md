# Chart image mirror verification

`TestChartImageCatalogMirroring` runs the inventory tool against real source
and mirror registries and renders NATS, cert-manager, OpenBao and Cassandra.
It checks generated overrides without creating or changing cluster resources.
Separate live chart installation checks establish runtime behavior.

Prerequisites:

- Helm, built chart dependencies and skopeo with registry credentials.
- A built `tools/docs-version-sync` executable.
- A QA directory containing `<profile>-source-values.yaml` and
  `<profile>-mapping.yaml` for `nats`, `cert-manager`, `openbao` and
  `cassandra`. Mapping entries must exactly match the inspected effective
  source images and identify already-published mirror destinations.
- A `trust/` subdirectory with registry CA/client certificates when required.
  It may be empty when registries use publicly trusted certificates.

Build the tool and run from the repository root:

```sh
go build -C tools/docs-version-sync -o /absolute/path/docs-version-sync .
REPO_ROOT="$PWD" \
IMAGE_CATALOG_TOOL=/absolute/path/docs-version-sync \
IMAGE_CATALOG_QA_ROOT=/absolute/path/isolated-qa \
IMAGE_CATALOG_MIRROR_PREFIX=mirror.example \
go test -C tests/bdd -run '^TestChartImageCatalogMirroring$' -count=1 -v
```

Keep the anchored test name. `TestChartImageCatalogMirroringWiring` uses fake
command results and does not verify artifacts or deployments. The real suite
requires matching AMD64 and ARM64 platform digests and complete rendered
catalog coverage before the tool emits mirror values.

Follow root QA rules before preparing a local registry or installing charts
to exercise generated profiles. Retain source/mirror identities, generated
values and runtime results with the qualification evidence.
