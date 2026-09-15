# AGENTS.md - vanity-gateway helm chart

Native Helm chart subtree. Shared chart rules live in `deploy/helm/AGENTS.md`.

## Chart Facts

- Subproject id: `vanity-gateway-helm`
- Chart name: `helm-nvcf-vanity-gateway`
- Chart directory: `helm-nvcf-vanity-gateway`
- CI values: `tools/ci/helm-validate-values/vanity-gateway.yaml`
- Release service name: `helm-nvcf-vanity-gateway`
- Release tag format: `deploy/helm/vanity-gateway/v<X.Y.Z>`

## Provenance

This chart was recovered from the published OCI artifact
`helm-nvcf-vanity-gateway:0.1.0-nvcf-10204.1`. No source tree for it existed in
this repo or in any known upstream project, so the imported files are the
unpacked contents of that artifact plus the sibling scaffolding in this
directory. `.helmignore` is not carried in a packaged chart and was added here
to match the other chart subtrees.

## Versioning

The chart is registered in `tools/ci/github-release-subprojects.json` with an
`initial_version` floor of `0.2.0`, so it releases on tags of the form
`deploy/helm/vanity-gateway/v<X.Y.Z>`. The release job creates
`deploy/helm/vanity-gateway/v0.2.0` in its own checkout as a semantic-release
baseline and never pushes it, so `0.2.0` is not a published chart version. The
first published version is `0.3.0` for a `feat` commit under this subtree, or
`0.2.1` for a `fix`. The
floor sits above the `0.1.0-nvcf-10204.x` artifacts the chart was recovered
from, so every published version is unambiguously newer. See
`docs/dev/github-release-process.md`.

`Chart.yaml` carries `version: 0.0.0`. The release pipeline packages the chart
at the version taken from the release tag, so the committed value is only used
by local renders and never ships.

`appVersion` tracks the image this chart deploys and is bumped by hand.

## Validate

```bash
helm lint helm-nvcf-vanity-gateway -f ../../../tools/ci/helm-validate-values/vanity-gateway.yaml
helm template vanity-gateway helm-nvcf-vanity-gateway -f ../../../tools/ci/helm-validate-values/vanity-gateway.yaml
```

The chart renders with defaults alone, but `vanityGateway.image.registry` is
empty by default and yields an unqualified image reference, so a values
override is used for validation.

`values.schema.json` sets `additionalProperties: false` on `vanityGateway` and
on most of its sub-objects. Adding a value key requires a matching schema
change or the render fails.

The CI values only cover renders that are expected to succeed. Chart-specific
render checks run by hand:

```bash
bash tests/chart-render/verify-llm-gateway-routing.sh
bash tests/chart-render/verify-servicemonitor-label.sh
```

This chart pairs with the service image source at
`src/invocation-plane-services/vanity-gateway`, whose service name is
`nvcf-ai-api-gateway-service`. Route configuration for the gateway in front of
it lives in `deploy/helm/gateway-routes`.
