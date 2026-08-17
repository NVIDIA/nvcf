# AGENTS.md - vanity-gateway helm chart

Native Helm chart subtree. Shared chart rules live in `deploy/helm/AGENTS.md`.

## Chart Facts

- Subproject id: `vanity-gateway-helm`
- Chart name: `helm-nvcf-vanity-gateway`
- Chart directory: `helm-nvcf-vanity-gateway`
- CI values: `tools/ci/helm-validate-values/vanity-gateway.yaml`
- Release service name: `helm-nvcf-vanity-gateway`
- Initial release version: not yet assigned. See "Versioning" below.

## Provenance

This chart was recovered from the published OCI artifact
`helm-nvcf-vanity-gateway:0.1.0-nvcf-10204.1`. No source tree for it existed in
this repo or in any known upstream project, so the imported files are the
unpacked contents of that artifact plus the sibling scaffolding in this
directory. `.helmignore` is not carried in a packaged chart and was added here
to match the other chart subtrees.

## Versioning

The imported `Chart.yaml` still carries the published version
`0.1.0-nvcf-10204.1` and appVersion `1.25.0-nvcf-10204.0`. Neither is a form the
repo release tooling accepts, so no release lane is registered for this chart
and no tag exists. Renumber `version` to a plain `X.Y.Z` before wiring a
release lane.

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
