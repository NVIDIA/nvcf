# Function Autoscaler Operations

The self-managed stack deploys the Function Autoscaler for the `control` and
`all` observability profiles. State Metrics must remain enabled for both. See
[Observability Configuration](../observability.md) for profile and metrics
backend settings.

## Verify the deployment

Check State Metrics and the Function Autoscaler:

```bash
kubectl get deployment -n nvcf \
  -l app.kubernetes.io/instance=state-metrics
kubectl get deployment -n nvcf \
  -l app.kubernetes.io/instance=function-autoscaler
kubectl rollout status deployment/function-autoscaler -n nvcf
```

Confirm the resolved PromQL endpoint. This ConfigMap does not contain the
backend credentials:

```bash
kubectl get configmap -n nvcf function-autoscaler-env \
  -o jsonpath='{.data.TIMESERIES_DB__TIMESERIES_DB_URL}{"\n"}'
```

For the bundled backend, the result should point to `vmsingle` in the
configured monitoring namespace. For an existing backend, it should match
`metricsBackend.promqlEndpoint`.

## Health endpoints

The Function Autoscaler exposes three health endpoints:

| Endpoint | Purpose | Use as |
| --- | --- | --- |
| `GET /admin/health/liveness` | Always returns 200. Indicates the process is alive. | Kubernetes liveness probe. |
| `GET /admin/health/readiness` | Returns 200 when all components are healthy, 503 otherwise. | Kubernetes readiness probe. |
| `GET /health` | Returns per-component health for `cassandra_client` and `timeseries_db_client`. | Operator-facing detail and dashboards. |

Inspect the detailed endpoint through the service:

```bash
kubectl port-forward -n nvcf service/function-autoscaler 8181:8181
curl http://127.0.0.1:8181/health
```

The liveness probe does not check Cassandra or the metrics backend. Dependency
failures change readiness instead.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Function Autoscaler is not installed | Use the `control` or `all` profile. Keep `stateMetrics.enabled: true`. |
| `cassandra_client` is unhealthy | Check contact-point DNS, credentials, and the configured TLS files. |
| `timeseries_db_client` is unhealthy | Check the resolved PromQL endpoint, authentication mode, credentials, and backend retention. |
| Scaling decisions are not applied | Check NVCF API authentication and function status. |
| Discovery does not find active functions | Confirm the backend contains the request and State Metrics data listed in [Architecture](./architecture.md#metrics-backend). Check the discovery lock metrics and TTL. |

## See also

- [Function Autoscaler Observability](./observability.md) for the metrics and traces referenced in the symptoms above.
- [Configure Autoscaling](../configure-autoscaling.md) for setting per-function scaling bounds and policy via the NVCF API.
- [Architecture](./architecture.md) for the component layout these symptoms map to.
- [Observability Configuration](../observability.md) for shared stack settings.
