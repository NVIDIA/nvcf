# Optional monitoring

Monitoring uses an independent Helm release for the collector, VictoriaMetrics and Grafana. The chart defaults scrape the `llm-stack` shared release and its Pylon workloads in namespace `llm-stack`.

Run commands from `deploy/helm/llm-routing`. Replace `CONTEXT` with your Kubernetes context.

## Install and upgrade

```bash
helm upgrade --install llm-monitoring recipes/charts/monitoring \
  --kube-context CONTEXT --namespace llm-stack --wait --timeout 10m
```

For placement, storage or ingress overrides, add `--values /path/to/private/monitoring-values.yaml`. Keep these environment settings outside the checkout. Resource defaults, scrape selectors and image versions are in [values.yaml](charts/monitoring/values.yaml).

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
| `grafana.rootURL` | Public URL ending in `/`, required when ingress is enabled |
| `grafana.ingress` | Optional `enabled`, `className`, `host` and `path` settings |

Override `targets` if shared-stack labels or metric ports differ from the defaults. Helm replaces the entire list, so include all targets you want to scrape. Each target needs a unique `name`, pod-label `selector` and `portName`. Optional fields are `path`, `port` and `runtime: llama.cpp`.

The chart generates the Grafana admin credential and reuses it on upgrades. A missing generated credential on upgrade is an error. To use an externally managed credential, set `grafana.adminSecret` to an existing Secret containing `admin-user` and `admin-password`.

For disconnected environments, distribute the pinned monitoring images using your normal registry or node-preload process. Review [image licenses](NOTICE) before distributing bundles.

## Ingress

To expose Grafana through your cluster's ingress controller, add these Helm values:

```yaml
grafana:
  rootURL: http://demo.example.com/grafana/
  ingress:
    enabled: true
    className: traefik
    host: demo.example.com
    path: /grafana
```

Use your public scheme, host and port in `grafana.rootURL` so shared dashboard links resolve correctly. Its path must match `grafana.ingress.path` with a trailing `/`. The chart rejects mismatches. `className` and `host` are optional. An empty host matches all hosts. The chart does not configure ingress TLS.

Open `/grafana/d/llm-demo` through the ingress. Grafana serves the prefix itself, so configure the controller to forward it unchanged. Dashboard viewing is anonymous. Administrative access requires the Grafana credential.

## Local access and admin login

```bash
kubectl --context CONTEXT --namespace llm-stack port-forward \
  svc/llm-monitoring-grafana 13000:3000 --address 127.0.0.1
```

Open `http://127.0.0.1:13000/d/llm-demo`. With ingress enabled, include its path, for example `http://127.0.0.1:13000/grafana/d/llm-demo`.

For administration, open Grafana's login page. The generated username is `admin`. Retrieve its password from the release's Secret in your own terminal:

```bash
kubectl --context CONTEXT --namespace llm-stack get secret llm-monitoring-grafana-admin \
  -o jsonpath='{.data.admin-password}' | base64 --decode
```

Use the corresponding Secret and username if `grafana.adminSecret` is set. Save lasting dashboards and data sources in the chart. Grafana's local data directory is temporary.

For an existing reverse proxy that strips `/grafana/`, set `grafana.rootURL` to its public URL and leave `grafana.ingress.enabled` false. Access that setup through the proxy.

## Integration verification

After installation, run the test helper to check fresh scrapes and dashboard Viewer permissions:

```bash
python3 recipes/tests/verify_monitoring.py --context CONTEXT \
  --namespace llm-stack --release llm-monitoring \
  --output /path/to/private/monitoring-results.json
```

Add `--verify-traffic --model qwen3.8-27b` to send chat and streaming requests and check metric increases. This requires a ready model and a valid gateway caller credential. The test reads installed Helm values and shared-stack access material.

## Uninstall

```bash
helm uninstall llm-monitoring --kube-context CONTEXT --namespace llm-stack --wait --timeout 10m
```

The metrics PVC and generated Grafana credential Secret are retained. An external Grafana admin Secret remains under its existing owner. Reinstall under the same release name and namespace to reuse the metrics claim. Remove retained metrics data separately after checking its claim and reclaim policy. Routing and model releases remain installed.
