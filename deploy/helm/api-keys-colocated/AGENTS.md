# API Keys Helm chart

The chart is maintained here. Application image changes belong to
`src/control-plane-services/api-keys`. Pair the numeric container identity
with a compatible application image and validate the released package.

Run from the repository root:

```sh
make -C deploy/helm/api-keys-colocated test
helm lint deploy/helm/api-keys-colocated/api-keys -f tools/ci/helm-validate-values/api-keys-colocated.yaml
```

Use the existing Helm template and Bash conventions. Keep chart version
assignment in the release workflow and use scoped patch releases for
maintenance changes. Do not infer an image version from the chart version.
