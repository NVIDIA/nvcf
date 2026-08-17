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

The chart is registered in `tools/ci/github-release-subprojects.json` with no
`initial_version`, so it releases from a `0.0.0` floor on tags of the form
`deploy/helm/vanity-gateway/v<X.Y.Z>`. The first published version is `0.1.0`
for a `feat` commit under this subtree, or `0.0.1` for a `fix`. See
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

This chart pairs with the service image source at
`src/invocation-plane-services/vanity-gateway`, whose service name is
`nvcf-ai-api-gateway-service`. Route configuration for the gateway in front of
it lives in `deploy/helm/gateway-routes`.
