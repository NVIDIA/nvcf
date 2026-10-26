# Chart artifact verification

`chart-artifact` validates a packaged Helm chart and compares downloaded chart
content with the package selected for publication. Build it with the repository
Go toolchain:

```sh
go build -C tools/chart-artifact -o /tmp/chart-artifact .
/tmp/chart-artifact validate --metadata path/to/chart/Chart.yaml chart.tgz
/tmp/chart-artifact compare --name example --version 1.0.0 source.tgz downloaded.tgz
```

Validation requires the expected chart name and version, a matching archive root,
and a readable tar/gzip archive. It rejects duplicate paths, path traversal,
links, multiple YAML documents, and missing metadata. Limits are 100,000 entries,
512 MiB of uncompressed file content, 640 MiB including archive headers and
padding, and 1 MiB for `Chart.yaml`.

Comparison hashes sorted file paths and file bytes. Archive order, permissions,
and tar/gzip timestamps do not affect the result. Changed chart metadata,
templates, values, or bundled dependencies fail comparison. The printed content
digest is not an OCI manifest digest.

The required `helm charts` check discovers package roots from `Chart.yaml` under
`deploy/helm`, `deploy/stacks`, and `src/uis`. Nested dependencies are validated
as part of their parent package. The existing CI values mapping selects the
charts that also receive lint and render checks. Dependency builds and packaging
use temporary copies, leaving source files and existing archives unchanged.
Dependency repositories come from `Chart.yaml`; Helm configuration and caches
are isolated from the caller, and the check requires no registry credentials.
Run the same check locally with Helm and Go on `PATH`:

```sh
tools/ci/check-helm-charts
go test -C tools/chart-artifact ./...
```

Pre-merge checks validate source packages. Publishers must compare the actual
registry download after each successful write or existing-version conflict.
An existing version with different content requires a new version. A read-only
preflight cannot prevent a concurrent writer; registry immutability remains
necessary. Use a reviewed verifier revision independent of historical component
tags, and build the binary before entering a runtime that only provides Helm.

## Registry integration

`tools/ci/chart-publication.sh` uses a compiled verifier from
`CHART_ARTIFACT_TOOL` (or `chart-artifact` on `PATH`). Callers configure Helm
credentials and choose the destination. The read-only mode returns 0 for
identical content, 2 for a confirmed absent version, and 1 for mismatch or an
inconclusive read:

```sh
CHART_ARTIFACT_TOOL=/tmp/chart-artifact bash tools/ci/chart-publication.sh \
  check-oci chart.tgz example 1.0.0 oci://registry.example/charts
```

The existing manual chart-push workflow uses `publish-oci`: preflight, push only
an absent version, then mandatory content readback. The same comparison runs on
an upload conflict. Authentication and transport errors fail closed. Registry
stub regressions run in the required Helm job and the Go tooling tests; they
perform no network requests or registry writes.
