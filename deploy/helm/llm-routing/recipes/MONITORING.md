# Optional monitoring

Monitoring uses an independent Helm release for the collector, VictoriaMetrics and Grafana. The chart defaults scrape the `llm-stack` shared release and its Pylon workloads in namespace `llm-stack`.

Run commands from `deploy/helm/llm-routing`.

## Install and upgrade

List the nodes and choose an IP reachable from your computer. Traefik must serve HTTP on port 80 at that address.

```bash
kubectl get nodes -o wide
```

Set `NODE_IP` to that address, then install monitoring:

```bash
NODE_IP=YOUR_NODE_IP

helm upgrade --install llm-monitoring recipes/charts/monitoring \
  --namespace llm-stack \
  --set-string grafana.rootURL="http://${NODE_IP}/grafana/" \
  --wait --timeout 10m
```

Open `http://NODE_IP/grafana/d/llm-demo` using the same IP. Dashboard viewing is anonymous.

Settings and defaults are in [values.yaml](charts/monitoring/values.yaml).

| Setting | Purpose |
| --- | --- |
| `nodeSelector` | Monitoring pod placement |
| `namespaces` | Scrape namespaces, default `[llm-stack]` |
| `targets` | Pod selectors and named metric ports for gateway, router, operator and Pylon |
| `victoriaMetrics.retentionPeriod` | Metrics retention, default `3d` |
| `victoriaMetrics.storage` | Persistent volume class and size |
| `imagePullPolicy` | Use `Never` only when every monitoring image is preloaded |
| `collector.image`, `victoriaMetrics.image`, `grafana.image` | Versioned image references or mirrors |
| `networkPolicy.enabled`, `networkPolicy.apiServerCIDRs` | Optional egress policy and API server addresses |
| `grafana.rootURL` | Browser URL, set above to `http://NODE_IP/grafana/`. Must end in the ingress path plus `/`. |
| `grafana.ingress.enabled` | Create the ingress, default `true` |
| `grafana.ingress.className` | Ingress controller class, default `traefik` |
| `grafana.ingress.host` | Empty by default for node IP access; use a hostname for DNS access |
| `grafana.ingress.path` | URL prefix, default `/grafana`; the controller forwards it unchanged |

Override `targets` if shared-stack labels or metric ports differ from the defaults. Helm replaces the entire list, so include all targets you want to scrape. Each target needs a unique `name`, pod-label `selector` and `portName`. Optional fields are `path`, `port` and `runtime: llama.cpp`.

The chart generates the Grafana admin credential and reuses it on upgrades. A missing generated credential on upgrade is an error. To use an externally managed credential, set `grafana.adminSecret` to an existing Secret containing `admin-user` and `admin-password`.

For disconnected environments, distribute the pinned monitoring images using your normal registry or node-preload process. Review [image licenses](NOTICE) before distributing bundles.

## Admin login

For administration, open `/grafana/login` at the same ingress address. The generated username is `admin`. Retrieve its password from the release's Secret in your own terminal:

```bash
kubectl --namespace llm-stack get secret llm-monitoring-grafana-admin \
  -o jsonpath='{.data.admin-password}' | base64 --decode
```

Use the corresponding Secret and username if `grafana.adminSecret` is set. Save lasting dashboards and data sources in the chart. Grafana's local data directory is temporary.

## Integration verification

After installation, run the test helper to check fresh scrapes and dashboard Viewer permissions:

```bash
python3 recipes/tests/verify_monitoring.py \
  --namespace llm-stack --release llm-monitoring \
  --output /path/to/private/monitoring-results.json
```

Add `--verify-traffic --model qwen3.8-27b` to send chat and streaming requests and check metric increases. This requires a ready model and a valid gateway caller credential. The test reads installed Helm values and shared-stack access material.

## Uninstall

```bash
helm uninstall llm-monitoring --namespace llm-stack --wait --timeout 10m
```

The metrics PVC and generated Grafana credential Secret are retained. An external Grafana admin Secret remains under its existing owner. Reinstall under the same release name and namespace to reuse the metrics claim. Remove retained metrics data separately after checking its claim and reclaim policy. Routing and model releases remain installed.
