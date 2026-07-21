# AGENTS.md - byoo-otel-collector

BYO Observability OpenTelemetry Collector is a native Go subtree with three
main pieces:

- `cmd/byoo-otel-collector/`: wrapper process that extracts secrets, renders
  config, and runs the collector
- `otelcol/`: checked-in OpenTelemetry Collector build output used by the
  Bazel lane
- `generator/`: Python template generator for example configs and docs

## Build and Test

```bash
go test ./...
make lint
make update-config-template
make update-examples
make validate-otelconfig
```

Run `make update-config-template` after changing templates under
`internal/otelconfig/templates/`. Run `make update-examples` after changing
template source, generator logic, or supported backend examples.

CI subproject id: `byoo-otel-collector`. The umbrella CI lane is declared in
`tools/ci/subproject-validations.yaml`, an internal GitLab CI config not
present in this public snapshot; current custom checks include
`check-version-modified`, generated example drift, generated config drift, and
otelconfig validation. Do not add a subtree `.gitlab-ci.yml`.

## Collector Version Updates

Use the script instead of editing version strings by hand:

```bash
./scripts/update-collector-version.sh v0.153.0 v1.59.0
# If service release tags are already ahead of the collector patch:
./scripts/update-collector-version.sh v0.153.0 v1.59.0 0.153.2
```

The script updates version references in `otel-collector-build.yaml`,
`AGENTS.md`, `README.md`, `Makefile`, `Dockerfile`,
`Dockerfile.nvcf-otel-collector`, `scripts/regenerate-otelcol.sh`, and
`VERSION`. It also updates `.gitlab-ci.yml` when that file exists. Run it from
the BYOO collector root. You can pass versions with or without the `v` prefix
(for example, `v0.153.0` or `0.153.0`). Pass the optional `v1.x.y` provider
version when the stable collector modules need a matching release, and pass the
optional app release override when existing service tags require the next BYOO
patch version. The app release major/minor must match the collector
major/minor. After running, regenerate `otelcol/` if needed, review
`git diff`, and run the relevant build or validation command.

The root generated release config must keep `release.version_file: VERSION` and `release.version_major_minor_source_file: otel-collector-build.yaml` for this service so publishing reads the next BYOO release version from `VERSION`, and so `VERSION` major/minor cannot drift from the collector config.

## Local Gotchas

- Generated templates and examples are committed. Regenerate and commit them
  with source changes.
- Test fixtures and example configs must use fake endpoints and fake secrets.
- ESS-derived secrets are split into files; preserve file permission handling
  when touching `internal/secrets/`.
- `otelcol/` is intentionally built through a Bazel genrule shim around
  checked-in collector output. Keep that rationale in sync if the build shape
  changes.

## References

- `README.md`
- `generator/doc/README.md`
- `validator/README.md`
