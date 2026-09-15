# AGENTS.md - cassandra chart

Native Helm chart subtree. Shared chart rules live in `deploy/helm/AGENTS.md`.

## Chart Facts

- Subproject id: `cassandra`
- Chart name: `helm-nvcf-cassandra`
- Chart directory: `helm`
- CI values: `tools/ci/helm-validate-values/cassandra.yaml`
- Release service name: `helm-nvcf-cassandra`

## Validate

```bash
tools/ci/validate-helm-chart helm \
  -f tools/ci/helm-validate-values/cassandra.yaml
```

This chart pairs with the migration image source at `migrations/cassandra`.
