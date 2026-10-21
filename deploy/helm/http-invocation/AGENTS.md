# AGENTS.md - HTTP invocation chart

This subtree packages `helm-nvcf-invocation-service`.

## Validation

Run from this directory with Helm and Mike Farah yq available:

```sh
helm lint nvcf-invocation-service \
  --set-string invocation.image.registry=registry.example.test \
  --set-string invocation.image.repository=nvcf-invocation-service
for test in tests/*_test.sh; do bash "$test"; done
```

## Conventions

Keep chart templates at two-space indentation. Render tests must check actual
Deployment fields, including configured probes and explicit null overrides.
Read expected image versions from Chart.yaml rather than copying tag literals.

Keep `version: 0.0.0` in Chart.yaml; release packaging assigns the chart version.
The service source is in `src/invocation-plane-services/http-invocation`.
The consuming stack is in `deploy/stacks/self-managed`.
