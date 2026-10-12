# LLM request router Helm chart

The chart is maintained here. Keep PKI migration hook changes separate from
Stargate runtime changes. Run the render checks without a cluster. The
`test-k8s-readiness-warmup` target creates a live cluster and requires the
root QA environment authorization.

Run from the repository root:

```sh
make -C deploy/helm/llm-request-router test
helm lint deploy/helm/llm-request-router/llm-request-router -f tools/ci/helm-validate-values/llm-request-router.yaml
```

Use the existing Helm template and Bash conventions. Keep chart version
assignment in the release workflow and use scoped patch releases for
maintenance changes. Do not infer an image version from the chart version.
