# Example Dashboards Deployment

<Warning>
**These Helm charts are provided as examples only and are not intended for production use.**

The `nvcf-observability-reference-stack` and `nvcf-example-dashboards` Helm charts are
designed to help users understand useful metrics, logs, and traces, and to serve as
inspiration for creating custom dashboards tailored to their observability needs.

**Important limitations:**

- No security hardening
- No SSL/TLS encryption
- No authentication or authorization
- No support for production workloads
- Not supported by NVIDIA for uses beyond example and reference purposes

For production deployments, users should integrate with their own observability infrastructure
following the guidance in [self-hosted-observability](./observability.md).

</Warning>

## Overview

This guide provides step-by-step instructions for deploying the NVCF observability reference
stack and example dashboards in a local or development environment. These example components
demonstrate how to collect and visualize metrics, logs, and traces from a self-hosted NVCF deployment.

The example stack includes:

- **nvcf-observability-reference-stack**: A reference implementation of an observability backend
  (Prometheus, Grafana, Loki, Tempo, and OpenTelemetry Collector)
- **nvcf-example-dashboards**: Pre-configured Grafana dashboards showing key NVCF control-plane metrics

<Note>
The observability reference stack supports a single-cluster deployment or the
control-plane cluster in a split-topology deployment. A separate observability
reference stack for an individual GPU cluster is not currently supported.

</Note>

## Prerequisites

Before deploying the example dashboards, you need:

1. A Kubernetes cluster
2. Self-hosted NVCF control-plane deployed and running
3. `helm` CLI installed
4. `kubectl` configured to access your cluster

<Note>
To populate Prometheus-backed dashboards, deploy the control-plane with
`global.observability.metrics.enabled` set to `true`.

To view traces in the example stack, the control-plane must be deployed
with tracing enabled and configured to send OTLP traces to the
observability collector. See [Tracing Configuration](./observability.md)
for the required Helm overrides under `global.observability.tracing` (e.g.,
`enabled`, `collectorEndpoint`, `collectorPort`,
`collectorProtocol`).

</Note>

### Enabling metrics and tracing after initial deployment

If observability was not configured during the initial self-hosted NVCF cluster
deployment, you can enable metrics later so that control-plane services expose
Prometheus metrics. Enable tracing if you also want the control-plane to send
OTLP traces to the deployed observability collector. Use the following steps.

1. Edit the environment file (`environments/<environment-name>.yaml`) to
   enable metrics and tracing. Set `collectorEndpoint` to the deployed
   collector's service address. For the example observability reference stack,
   the collector runs in the `observability` namespace. Example:

   ```yaml
   global:
     observability:
       metrics:
         enabled: true
       tracing:
         enabled: true
         collectorEndpoint: "otel-collector-gateway-collector.observability.svc.cluster.local"
         collectorPort: 4317
         collectorProtocol: http
   ```

2. Apply the configuration changes from your control-plane deployment directory:

   ```bash
   HELMFILE_ENV=<environment-name> helmfile sync
   ```

   Replace `<environment-name>` with your environment name (e.g., `eks-example`).

## Deployment Steps

### Step 1: Install the Observability Reference Stack

Once your self-managed NVCF control-plane is up and running, install the observability
reference stack:

```bash
# Add and update the public NVCF Helm repository
helm repo add nvcf https://helm.ngc.nvidia.com/nvidia/nvcf --force-update
helm repo update

# Check NGC for the latest version of this Helm chart:
# https://catalog.ngc.nvidia.com/orgs/nvidia/teams/nvcf/helm-charts/nvcf-observability-reference-stack

helm upgrade \
  --install observability \
  nvcf/nvcf-observability-reference-stack \
  --version 1.10.0 \
  --namespace observability \
  --create-namespace
```

For a split-topology deployment, run the command against the control-plane
cluster and disable the NVCA ServiceMonitor. NVCA runs on the GPU cluster, so
the control-plane observability stack does not have an NVCA scrape target.

```bash
helm upgrade \
  --install observability \
  nvcf/nvcf-observability-reference-stack \
  --version 1.10.0 \
  --namespace observability \
  --create-namespace \
  --set nvcfServiceMonitors.nvcaEnabled=false
```

Do not install this reference stack on the GPU cluster. GPU cluster-specific
observability stack support is not currently available.

This will deploy:

- Prometheus for metrics collection
- Grafana for visualization
- Loki for log aggregation
- Tempo for distributed tracing
- OpenTelemetry Collector for telemetry processing
- Fluent Bit for log collection

The OTel Collector and Fluent Bit cluster-scoped config are disabled in this
initial install; they are enabled in Step 2.

<Note>
The Grafana installation in the observability reference stack includes the
`ae3e-plotly-panel` plugin for use with the example dashboards.

</Note>

**Verify the deployment:**

```bash
# Check that all pods are running
kubectl get pods -n observability

# Wait for all pods to be Ready
kubectl wait --for=condition=ready pod --all -n observability --timeout=300s
```

### Step 2: Enable the OTel Collector and Fluent Bit Cluster Config

The initial install does not enable the OTel Collector or the Fluent Bit
cluster-scoped config by default. A second Helm upgrade is required to set
`otel-collector.enabled=true` and `fluentBitClusterConfig.enabled=true` so
that the `observability-gateway-collector` service (and related gateway
services) are deployed and Fluent Bit cluster resources (ClusterFilter,
ClusterOutput, ClusterFluentBitConfig) are managed by Helm. This has to be
split into separate installs due to a chicken and egg problem with the CRDs.
These services are needed for the control-plane to send OTLP traces to Tempo.

Run the following upgrade (use the same chart version and namespace as in Step 1):

For split-topology deployments, `--reuse-values` preserves
`nvcfServiceMonitors.nvcaEnabled=false` from the initial installation.

```bash
helm upgrade observability \
  nvcf/nvcf-observability-reference-stack \
  --version 1.10.0 \
  --namespace observability \
  --reuse-values \
  --set fluentBitClusterConfig.enabled=true \
  --set otel-collector.enabled=true \
  --wait \
  --timeout 5m
```

**Verify the OTel Collector is running:**

```bash
kubectl get pods -n observability | grep observability-gateway
kubectl get svc -n observability | grep observability-gateway
```

You should see the `observability-gateway-collector` pod and the
`observability-gateway-collector` service (and related gateway services).

### Step 3: Install the Example Dashboards

Once the observability reference stack is deployed and the OTel Collector is
enabled, install the example dashboards:

```bash
# Check NGC for the latest version of this Helm chart:
# https://catalog.ngc.nvidia.com/orgs/nvidia/teams/nvcf/helm-charts/nvcf-example-dashboards

helm upgrade \
  --install nvcf-example-dashboards \
  nvcf/nvcf-example-dashboards \
  --version 1.6.0 \
  --namespace observability \
  --create-namespace
```

This will configure Grafana with pre-built dashboards for:

- NVCF API
- Invocation Service
- SPOT Instance Service (SIS)
- Encrypted Secrets Service (ESS)
- Cassandra
- Vault
- Worker Pods (Utils, Init, and Inference containers)

**Access Grafana:**

```bash
# Port-forward to access Grafana UI
kubectl port-forward -n observability \
    svc/$(kubectl get svc -n observability -l app.kubernetes.io/name=grafana -o jsonpath='{.items[0].metadata.name}') \
    3000:80
```

Then open your browser to `http://localhost:3000` and log in to view the dashboards.

### Step 4: Generate Dashboard Data

To populate the dashboards with meaningful data, you need to deploy and invoke functions.
Deploy and invoke functions using your own commands or tools. The example dashboards will
automatically populate as your NVCF control-plane handles function requests.

## Cleanup and Uninstallation

When you're finished testing or want to remove the example observability stack, follow these
steps:

### Step 1: Delete Custom Resources

First, delete any custom resources created by the observability stack:

```bash
# Delete FluentBit custom resources (namespace-scoped)
kubectl delete fluentbits.fluentbit.fluent.io --all -A

# Delete OpenTelemetry Collector custom resources
kubectl delete opentelemetrycollectors.opentelemetry.io --all -A
```

If you enabled `fluentBitClusterConfig.enabled=true` in Step 2 of deployment,
the Fluent Bit cluster-scoped resources (ClusterFilter, ClusterOutput,
ClusterFluentBitConfig) are managed by Helm and will be removed when you
uninstall the Helm release in the next step.

### Step 2: Uninstall Helm Releases

Uninstall both Helm releases:

```bash
# Uninstall example dashboards
helm uninstall nvcf-example-dashboards -n observability

# Uninstall observability reference stack
helm uninstall observability -n observability
```

### Step 3: Delete the Namespace

Finally, delete the observability namespace:

```bash
# Delete the namespace (this will remove any remaining resources)
kubectl delete namespace observability
```

<Note>
If you deployed NVCF to a namespace other than `observability`, make sure to only delete
the observability namespace, not your NVCF control-plane namespace.

</Note>

## Troubleshooting

**Pods not starting:**

Check pod status and logs:

```bash
kubectl get pods -n observability
kubectl describe pod <pod-name> -n observability
kubectl logs <pod-name> -n observability
```

**Dashboards not showing data:**

1. Verify Prometheus is scraping metrics:

   ```bash
   # Port-forward to Prometheus
   kubectl port-forward -n observability svc/prometheus 9090:9090

   # Check targets at http://localhost:9090/targets
   ```

2. Verify NVCF services are exposing metrics:

   ```bash
   # Port-forward to an NVCF service
   kubectl port-forward -n nvcf svc/nvcf-api 8080:8080

   # Curl the metrics endpoint
   curl http://localhost:8080/metrics
   ```

3. Check that ServiceMonitors are created:

   ```bash
   kubectl get servicemonitor -n nvcf
   ```

**Grafana login issues:**

The default credentials are typically `admin/admin` or may be configured via Helm values.
Check the Helm chart documentation or values for the correct credentials.

## Next Steps

After exploring the example dashboards:

1. Review the metrics, logs, and traces being collected
2. Identify which metrics are most relevant to your use case
3. Design and implement your own production-ready observability solution
4. Integrate with your existing enterprise observability platforms
5. Configure alerting based on your operational requirements

For production deployments, see [self-hosted-observability](./observability.md) for guidance on integrating
with your own observability infrastructure.

## Related Documentation

- [self-hosted-observability](./observability.md): Production observability configuration
- [nvcf-observability-reference-stack on NGC](https://catalog.ngc.nvidia.com/orgs/nvidia/teams/nvcf/helm-charts/nvcf-observability-reference-stack)
- [nvcf-example-dashboards on NGC](https://catalog.ngc.nvidia.com/orgs/nvidia/teams/nvcf/helm-charts/nvcf-example-dashboards)
