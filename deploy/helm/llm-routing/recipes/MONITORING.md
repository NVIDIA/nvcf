# Advanced monitoring configuration

For installation and dashboard access, see [Monitoring](../README.md#monitoring). Run commands from `deploy/helm/llm-routing/recipes`, using the same `--context`, `--config` or `--work-dir` options as installation.

## Configuration

Edit `monitoring` in the saved configuration, then run `python3 recipe.py monitoring` to apply it.

| Setting | Default | Purpose |
| --- | --- | --- |
| `enabled` | `false` when omitted; `init` enables it | Include monitoring in `stack`. The explicit `monitoring` command enables it. |
| `retentionPeriod` | `3d` | At least `24h`. Use whole numbers with `h`, `d`, `w`, `M` (months) or `y`. |
| `storageSize` | `5Gi` | Initial metrics volume size |
| `namespaces` | Installation namespace | Namespaces to scrape, including the installation namespace |
| `extraTargets` | `[]` | Additional metric exporters |
| `model` | First discovered model, sorted by ID | Model for traffic verification |
| `imagePullPolicy` | `IfNotPresent` | Use cached images or pull them. Use `Never` for preloaded images. |
| `images` | [Chart defaults](charts/monitoring/values.yaml) | Override `collector`, `victoriaMetrics` or `grafana` with versioned images |
| `networkPolicy.enabled` | `false` | Restrict monitoring egress. Requires an enforcing network plugin. |
| `networkPolicy.apiServerCIDRs` | `[]` | API Service and endpoint IPs as `/32` or `/128` CIDRs when restricting egress |

Monitoring runs on `nodes.control` using the installation's storage class. Resource defaults and image versions are in [values.yaml](charts/monitoring/values.yaml). Setting `enabled=false` skips future automatic installation. To remove an installed release, use [Uninstall](#uninstall).

Each `extraTargets` entry needs a unique name, pod label selector and named container port:

```json
{
  "name": "model-runtime",
  "selector": "app.kubernetes.io/name=my-model-server",
  "portName": "http"
}
```

Optional fields: `path` (default `/metrics`), `port`, and `runtime: "llama.cpp"` to add compatible backend panels. Include the target namespace in `namespaces`.

For offline installation, export images on an online Docker workstation, transfer the archive, then import it on the installation workstation:

```bash
python3 recipe.py export-monitoring-images --archive /path/to/monitoring.tar
python3 recipe.py import-monitoring-images --archive /path/to/monitoring.tar --allow-containerd-import
```

The importer needs configured `containerd` access and preloaded image-loader helper images. Set `monitoring.imagePullPolicy` to `Never` before installation. Review [image licenses](NOTICE) before distributing bundles.

## Dashboard and admin access

To reopen the dashboard after installation:

```bash
python3 recipe.py dashboard
```

For admin access, stop the tunnel and run `python3 recipe.py dashboard --admin`. This opens your browser and signs in automatically. Sign out in Grafana to return to Viewer access. Admin manages users, permissions and data sources. Save lasting dashboard and data-source changes in the chart. Keep generated Helm values private.

## Verification

Check fresh metrics and dashboard access:

```bash
python3 recipe.py verify-monitoring
```

To also send real streaming and nonstreaming requests and check request, latency and token metrics:

```bash
python3 recipe.py verify-monitoring --verify-traffic
```

Use `--model <model-id>` to override model selection. The model must support chat completions, streaming and token usage. Verification reuses an available caller key or temporarily adds and removes one. Set `apiKeyFile` to select a key file. Results are saved in `evidence/monitoring.json`.

## Uninstall

```bash
python3 recipe.py uninstall-monitoring
```

Metrics storage is retained. Reinstall with `python3 recipe.py monitoring`, which creates a new Grafana password. For full teardown, continue with [routing uninstall](../README.md#uninstall).
