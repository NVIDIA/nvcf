# NvSnap Helm chart

GPU checkpoint/restore for Kubernetes. The chart deploys the nvsnap agent
DaemonSet on GPU nodes (it also serves the mutating admission webhook), the
nvsnap server (REST API, web UI and catalog), the webhook registration and its
cert-manager certificate, and optionally cert-manager, the NVIDIA GPU Operator,
Jaeger, Prometheus and Grafana as subcharts.

Install, configuration choices, upgrade, uninstall and troubleshooting are in
[docs/operate/INSTALL.md](../../../docs/operate/INSTALL.md). Every value is
documented in [values.yaml](values.yaml); what the components do is in
[docs/ARCHITECTURE.md](../../../docs/ARCHITECTURE.md).

## Templates

| Template | Renders |
|---|---|
| `agent-daemonset.yaml`, `agent-rbac.yaml`, `agent-token-secret.yaml` | The agent and its two Services, its RBAC and its API token |
| `server.yaml` | The server Deployment, Service, RBAC and catalog PVC |
| `webhook.yaml` | The webhook Service, the MutatingWebhookConfiguration and its certificate |
| `cachedir-env-configmap.yaml` | The cachedir environment template the webhook reads at capture (when `agent.podCacheDir` is set) |
| `network-policy-restore-pods.yaml` | Lets restore pods in the namespaces listed in `agent.l2.restoreNamespaces` reach the server, and, with `webhook.restorePrepStrategy: init-container`, the agent |
| `post-install-smoke.yaml` | The post-install health probe Job |
| `grafana-dashboard.yaml` | The nvsnap Grafana dashboard |

## Development

```bash
helm dependency build deploy/helm/nvsnap
helm lint deploy/helm/nvsnap
helm template nvsnap deploy/helm/nvsnap --namespace nvsnap-system
scripts/test-helm-chart.sh
```

When an image version moves, bump its tag in `values.yaml`.
