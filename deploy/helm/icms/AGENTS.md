# SIS Helm chart

The chart is maintained here. Application changes belong to
`src/control-plane-services/instance-cluster-management`. The optional LLS
hook uses the OpenBao migration image; validate its identity independently
of the SIS application container.

Run from the repository root:

```sh
make -C deploy/helm/icms test
helm lint deploy/helm/icms/icms-api -f tools/ci/helm-validate-values/icms.yaml
```

Use the existing Helm template and Bash conventions. Keep chart version
assignment in the release workflow and use scoped patch releases for
maintenance changes. Do not infer an image version from the chart version.
