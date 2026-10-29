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

Run `./scripts/update-go-work-sum.sh` after changing `go.mod` here or in
`otelcol/`, and commit `go.work.sum`. The Bazel otelcol genrule declares it as
an input, so a clean checkout must have it. CI fails when it is stale.

Follow the [release workflow](../../../docs/dev/github-release-process.md) for source versions and
producer handoffs. Keep generated example drift, generated config drift and otelconfig checks.
Do not add a subtree `.gitlab-ci.yml`.

## Collector Version Updates

Use the script instead of editing version strings by hand:

```bash
./scripts/update-collector-version.sh v0.160.0 v1.66.0
```

The script updates version references in `otel-collector-build.yaml`,
`AGENTS.md`, `README.md`, `Makefile`, `Dockerfile`,
`Dockerfile.nvcf-otel-collector`, `scripts/regenerate-otelcol.sh`, and
`.gitlab-ci.yml` when that file exists. Run it from the BYOO collector root.
You can pass versions with or without the `v` prefix (for example, `v0.160.0`
or `0.160.0`). Pass the optional `v1.x.y` provider version when the stable
collector modules need a matching release. After running, regenerate `otelcol/`
if needed, run `./scripts/update-go-work-sum.sh`, review `git diff`, and run
the relevant build or validation command.

GitHub semantic-release calculates the NVIDIA wrapper SemVer from Conventional
Commits. The tag records both sources as
`src/compute-plane-services/byoo-otel-collector/v<upstream>-nv-<wrapper>`.
The upstream part comes from `otel-collector-build.yaml`; do not add a `VERSION`
file or a CI gate that requires one. `RELEASE_SERIES_START` anchors the first
wrapper release and must remain in the repository.

Commit a collector version bump as `fix(byoo-otel-collector):`, not
`chore(byoo-otel-collector):`. `chore` commits are not release-worthy (see
`RELEASE_RULES` in `tools/ci/github-release`), so a `chore` bump only
synthesizes a compatibility tag anchor and never triggers the image build and
push. Because the upstream version is embedded directly in the published tag
and image, every bump must ship a real image, not just update the pin in git.

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
