# AGENTS.md - ess helm chart

Native Helm chart subtree. Shared chart rules live in `deploy/helm/AGENTS.md`.

## Chart Facts

- Subproject id: `ess-helm`
- Chart name: `helm-nvcf-ess-api`
- Chart directory: `ess-api`
- CI values: `tools/ci/helm-validate-values/ess.yaml`
- Release service name: `helm-nvcf-ess-api`
- Initial release version: `1.7.0` (continues the version line imported from the
  upstream `ess-colocated-deploy` chart)

## Validate

```bash
helm lint ess-api -f ../../../tools/ci/helm-validate-values/ess.yaml
helm template ess-api ess-api -f ../../../tools/ci/helm-validate-values/ess.yaml
```

The chart requires `ess.image.registry` and `ess.image.repository`, so a values
override is always needed to render. This chart pairs with the ESS service image
source at `src/control-plane-services/ess`.
