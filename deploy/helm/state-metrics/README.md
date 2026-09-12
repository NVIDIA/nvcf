# NVCF State Metrics Helm chart

This directory is the public source for `helm-nvcf-state-metrics`. It was
restored from the published `1.0.2` chart package so future image and chart
releases can be reviewed and reproduced from this repository.

The chart deploys `nvcf-state-metrics-service` and exposes its Prometheus
endpoint through a Kubernetes Service. A ServiceMonitor is enabled by default.

## Validate the chart

Run the repository-wide chart check from the repository root:

```bash
tools/ci/check-helm-charts
```

To validate only this chart:

```bash
helm lint deploy/helm/state-metrics \
  --values tools/ci/helm-validate-values/state-metrics.yaml

helm template state-metrics deploy/helm/state-metrics \
  --namespace nvcf \
  --values tools/ci/helm-validate-values/state-metrics.yaml
```

The self-managed stack supplies the registry and repository through
`stateMetrics.image`. The chart owns the default image tag.
