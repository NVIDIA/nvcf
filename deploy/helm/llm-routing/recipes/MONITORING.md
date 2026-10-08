# Optional monitoring

Monitoring uses an independent Helm release for the collector, VictoriaMetrics and Grafana. It reads metrics from the `llm-stack` shared release and model workloads in namespace `llm-stack`. It does not install or change the gateway, router or Pylon operator.

Run commands from `deploy/helm/llm-routing` using the kubeconfig context selected for the main installation. Add `--context` to Python commands, `--kube-context` to Helm, or `--context` to kubectl when overriding it.

## Generate values and install

Generate nonsecret monitoring values from the installed shared stack:

```bash
python3 recipes/monitoring.py --namespace llm-stack values \
  --output /path/to/private/monitoring-values.json
```

Review the generated selectors before installing. Set `nodeSelector` and `victoriaMetrics.storage.storageClass` for your storage and placement requirements; generated values do not pin a node or choose a StorageClass. With an empty `grafana.adminSecret`, the chart generates its admin credential and reuses it on upgrades. A missing generated credential on upgrade is an error. To use an externally managed credential, set `grafana.adminSecret` to an existing Secret containing `admin-user` and `admin-password`.

```bash
helm upgrade --install llm-monitoring recipes/charts/monitoring \
  --namespace llm-stack --values /path/to/private/monitoring-values.json \
  --wait --timeout 10m
```

The generated configuration contains the selected stack's scrape targets. Resource defaults and image versions are in [values.yaml](charts/monitoring/values.yaml). Adjust these chart settings in the private file:

| Setting | Purpose |
| --- | --- |
| `nodeSelector` | Monitoring pod placement |
| `namespaces` | Keep the scrape namespace `llm-stack` |
| `targets` | Pod selectors and named metric ports |
| `victoriaMetrics.retentionPeriod` | Metrics retention, default `3d` |
| `victoriaMetrics.storage` | Persistent volume class and size |
| `imagePullPolicy` | Use `Never` only when every monitoring image is preloaded |
| `collector.image`, `victoriaMetrics.image`, `grafana.image` | Versioned image references or mirrors |
| `networkPolicy.enabled`, `networkPolicy.apiServerCIDRs` | Optional egress policy and API server addresses |

Each extra target needs a unique `name`, pod-label `selector` and `portName`. Optional fields are `path`, `port` and `runtime: llama.cpp`. Keep targets and the scrape scope in namespace `llm-stack`.

For disconnected environments, distribute the pinned monitoring images using your normal registry or node-preload process. Review [image licenses](NOTICE) before distributing bundles.

## Dashboard

```bash
python3 recipes/monitoring.py --namespace llm-stack --release llm-monitoring dashboard
```

The default local port is 13000. The dashboard grants anonymous Viewer access through the local tunnel. Use `--admin` for administrative access. Save lasting dashboards and data sources in the chart. Grafana's local data directory is temporary.

## Verification

Check metrics and dashboard access:

```bash
python3 recipes/monitoring.py --namespace llm-stack --release llm-monitoring verify \
  --output /path/to/private/monitoring-results.json
```

Add `--verify-traffic --model qwen3.8-27b` to send real chat and streaming requests and check metric changes. This requires the selected model to be ready and a valid gateway caller credential. The helper uses the installed shared stack directly and needs no saved connection file.

## Uninstall

```bash
helm uninstall llm-monitoring --namespace llm-stack --wait --timeout 10m
```

The metrics PVC and generated Grafana credential Secret are retained. An external Grafana admin Secret remains under its existing owner. Reinstall under the same release name and namespace to reuse the metrics claim. Remove retained metrics data separately after checking its claim and reclaim policy. Routing and model releases remain installed.
