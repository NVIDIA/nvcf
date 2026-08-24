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
present in this public snapshot; current custom checks include generated example
drift, generated config drift, and otelconfig validation. Do not add a subtree
`.gitlab-ci.yml`.

## Collector Version Updates

Use the script instead of editing version strings by hand:

```bash
./scripts/update-collector-version.sh v0.157.0 v1.63.0
```

The script updates version references in `otel-collector-build.yaml`,
`AGENTS.md`, `README.md`, `Makefile`, `Dockerfile`,
`Dockerfile.nvcf-otel-collector`, `scripts/regenerate-otelcol.sh`, and
`.gitlab-ci.yml` when that file exists. Run it from the BYOO collector root.
You can pass versions with or without the `v` prefix (for example, `v0.157.0`
or `0.157.0`). Pass the optional `v1.x.y` provider version when the stable
collector modules need a matching release. After running, regenerate `otelcol/`
if needed, review `git diff`, and run the relevant build or validation command.

GitHub semantic-release calculates the NVIDIA wrapper SemVer from Conventional
Commits. The tag records both sources as
`src/compute-plane-services/byoo-otel-collector/v<upstream>-nv-<wrapper>`.
The upstream part comes from `otel-collector-build.yaml`; do not add a `VERSION`
file or a CI gate that requires one. `RELEASE_SERIES_START` anchors the first
wrapper release and must remain in the repository.

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
