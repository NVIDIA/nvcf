# Function Autoscaler Operations

This page covers operating the function autoscaler after deployment, including health probes, common operational issues, and pointers to the Helm chart values. For log filter syntax, metrics, and traces, see [Function Autoscaler Observability](./observability.md).

## Health endpoints

The function autoscaler exposes three HTTP health endpoints. Their exact paths differ from the rest of the NVCF control plane: liveness and readiness are namespaced under `/admin/health/`.

| Endpoint | Purpose | Use as |
|----------|---------|--------|
| `GET /admin/health/liveness` | Always returns 200. Indicates the process is alive. | Kubernetes liveness probe. |
| `GET /admin/health/readiness` | Returns 200 when all components are healthy, 503 otherwise. | Kubernetes readiness probe. |
| `GET /health` | Returns per-component health for `cassandra_client` and `timeseries_db_client`. | Operator-facing detail and dashboards. |

The liveness probe deliberately does not check Cassandra or the timeseries database. Restarting the pod when those are unreachable does not help, so the function autoscaler stays running and lets readiness flip instead.

## Common operational issues

### Cassandra connection failures

Symptoms: readiness flips to 503, `/health` reports the `cassandra_client` component as unhealthy, log lines from `rs_autoscaler::cassandra` show connection errors.

Checks:

- SSL certificates are mounted at the path expected by `cassandra.ssl`. The function autoscaler container expects the cert directory to exist; create `/etc/app/config` if it is missing.
- Credentials in the secrets file are valid for the configured keyspace.
- The contact points resolve from the pod's network namespace.

### Timeseries database query failures

Symptoms: `nvcf_autoscaler.timeseries_db.requests_total` shows a rising error count, `auth_failure_total` or `server_side_failure_total` is non-zero, log lines from `rs_autoscaler::timeseries_db` show 4xx or 5xx responses.

Checks:

- `timeseries_db.timeseries_db_url` is reachable from the pod.
- The bearer token in the secrets file is current. Token rotation is the most common cause of `auth_failure_total` spikes.
- Query time ranges fit the retention window of the backing store.

### NVCF API errors

Symptoms: `nvcf_autoscaler.nvcf_api.request_duration_milliseconds` shows a sustained rise in 4xx or 5xx, scaling decisions stop applying.

Checks:

- The OAuth2 token endpoint is reachable and the client credentials in the secrets file are valid.
- The functions being scaled are still in a deployable status. Functions in unexpected states are skipped, not retried.
- `nvcf_api.disable_auth` is set as intended for the deployment. Leave it `false` whenever the NVCF API enforces authentication.

### Discovery is stalled

Symptoms: the active function set in Cassandra stops growing despite traffic to new functions, `nvcf_autoscaler.distributed_lock.acquisition_failures_total` is rising across all replicas.

Checks:

- Inspect the `locks` table for the discovery lock row and its TTL. If the row never expires, the previous leader may have stopped refreshing without releasing it.
- Confirm at least one replica's `nvcf_autoscaler.distributed_lock` gauge reports the leader state.
- Restart the holding replica if the cluster is otherwise healthy. The lock expires within `discovery_lock_duration_seconds`.

See [Architecture](./architecture.md#cassandra-lightweight-transactions-lwts) for the lock state machine.

## See also

- [Function Autoscaler Observability](./observability.md) for the metrics and traces referenced in the symptoms above.
- [Configure Autoscaling](../configure-autoscaling.md) for setting per-function scaling bounds and policy via the NVCF API.
- [Architecture](./architecture.md) for the component layout these symptoms map to.
