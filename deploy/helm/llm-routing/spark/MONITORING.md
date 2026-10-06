# Demo monitoring

The monitoring release collects Prometheus metrics from the gateway, request router, Pylon pods and Pylon Operator. Backend exporters are optional targets. OpenTelemetry Collector sends the samples to local VictoriaMetrics. Grafana loads the data source and LLM routing dashboard from ConfigMaps.

```text
Component /metrics endpoints
  Collector (pod discovery, one replica)
    VictoriaMetrics (three-day retention)
      Grafana (provisioned dashboard)
```

## Install

Run from `deploy/helm/llm-routing/spark` with the same context/configuration selection used for installation. New configurations enable monitoring during `stack`. For an existing routing stack, discover its settings and locate the saved configuration:

```bash
python3 spark.py attach-monitoring
python3 spark.py paths
```

Fresh attachment enables monitoring. If an existing configuration disables it, set:

```json
"monitoring": {"enabled": true}
```

[Preload the monitoring images](#offline-images) if the pull policy is `Never`, then install monitoring without changing the serving workloads:

```bash
python3 spark.py monitoring
```

## Verification

After a model is registered, run:

```bash
python3 spark.py verify-monitoring --verify-traffic
```

The command waits up to 75 seconds for fresh scrapes, then checks the dashboard and metric increases from real model requests. Results are saved in `evidence/monitoring.json`. Omit `--verify-traffic` to check collection only. The default is the first model ID in sorted gateway discovery. Set `monitoring.model` or pass `--model <model-id>` to select another.

Set `apiKeyFile` to an existing caller-key file for traffic verification. Otherwise the command uses a temporary key with the stack-managed static-key Secret.

## Dashboard

Print the Grafana password, then start the local tunnel:

```bash
(
  set -eu
  context="$(python3 spark.py context)"
  : "${context:?Context lookup returned an empty value}"
  kubectl --context "$context" -n llm-spark-poc \
    get secret llm-poc-monitoring-grafana-admin -o json |
    python3 -c 'import base64, json, sys; print(base64.b64decode(json.load(sys.stdin)["data"]["admin-password"]).decode())'
)

python3 spark.py dashboard --port 13000
```

The lookup uses the default namespace and release prefix. If customized, replace `llm-spark-poc` and `llm-poc-monitoring-grafana-admin` (`releasePrefix` plus `-monitoring-grafana-admin`). Pass the same `--context`, `--config` or `--work-dir` options to both recipe commands.

Open `http://127.0.0.1:13000/d/llm-demo` and sign in as `admin` with the printed password. Keep the command running. Ctrl-C closes the tunnel. Installation also saves the password as `grafana-admin-password` under the `workDir` shown by `python3 spark.py paths`.

## Uninstall

Run the whole block from the recipe directory in the same configured terminal used for installation. If you used `--context`, `--config` or `--work-dir`, pass the same options before `context` in the lookup. Replace the default namespace and release name if customized. The monitoring release name is `releasePrefix` plus `-monitoring`.

```bash
(
  set -eu
  context="$(python3 spark.py context)"
  : "${context:?Context lookup returned an empty value}"
  namespace=llm-spark-poc
  : "${namespace:?Set the namespace from your saved configuration}"

  helm --kube-context "$context" -n "$namespace" uninstall llm-poc-monitoring --ignore-not-found --wait --timeout 3m
)
```

This removes monitoring only, skips an absent release and stops on other failures. The metrics PVC and local work files remain. Reinstalling generates a new Grafana password.

For a full demo teardown, remove monitoring above, then follow the [routing uninstall and reinstall instructions](../README.md#uninstall). Model volumes and downloaded files remain. To restore monitoring alone, follow [Install](#install).

## Configuration

The three monitoring Deployments run on `nodes.control`. Default requests total 300 millicores and 896 MiB of memory, with 2304 MiB of memory limits. Grafana requests 512 MiB and allows 1 GiB. VictoriaMetrics has a separate 5 GiB PVC. Grafana reloads its provisioned dashboard and data source after restart. Save dashboard changes in the repository.

Pod discovery uses namespace-scoped read-only permissions. Services are ClusterIP. Grafana credentials are generated once, stored in a Helm-managed Secret and saved locally with mode 0600. Keep generated values and the work directory private.

The recipe accepts these optional `monitoring` settings:

| Setting | Default | Purpose |
| --- | --- | --- |
| `enabled` | false when omitted | Install monitoring during `stack` |
| `model` | Gateway model discovery | Select one model for traffic verification, overridden by `--model` |
| `imagePullPolicy` | Application pull policy | `Never`, `IfNotPresent` or `Always` |
| `images` | Chart version pins | Overrides for `collector`, `victoriaMetrics` and `grafana` |
| `retentionPeriod` | `3d` | VictoriaMetrics retention duration |
| `storageSize` | `5Gi` | Initial metrics PVC size |
| `namespaces` | Installation namespace | Existing namespaces to discover and grant pod-read access in |
| `extraTargets` | `[]` | Additional runtime or relay metric targets |
| `networkPolicy` | Disabled | Restrict monitoring pod egress to cluster pods and explicit API server hosts |

Image pins and default resources are defined in `charts/monitoring/values.yaml`, a JSON-formatted YAML file shared with the Python runner. Image overrides must use explicit version tags or digests. The Docker archive helpers require version tags. The runner reuses the installation's storage class and control node placement.

Each extra target requires a unique `name`, Kubernetes label `selector` and named container port `portName`. Optional `port` overrides the scrape port, and `path` defaults to `/metrics`. Set `runtime="llama.cpp"` only for compatible llama.cpp exporters to enable their backend panels. Other exporters can be scraped without `runtime`, with samples available in Explore and general scrape health. The named port selects one discovery target per pod. Add the recipe namespace to `namespaces` when it differs from the gateway namespace. For example:

```json
{
  "name": "model-runtime",
  "selector": "app.kubernetes.io/name=my-model-server",
  "portName": "http",
  "runtime": "llama.cpp"
}
```

The installed Pylon Operator must already watch any additional recipe namespace. Monitoring does not change its watch configuration. If the topology includes a separate Stargate Kubernetes relay, add its metrics target too.

Setting `enabled=false` stops future installation through the recipe. Use [Uninstall](#uninstall) to remove the existing release while keeping its metrics PVC.

## Offline images

While online, export the pinned ARM64 images:

```bash
python3 spark.py monitoring-images
python3 spark.py export-monitoring-images --archive /path/to/monitoring-arm64-images.tar
```

Transfer the archive to the installation workstation and import it into the control node using the existing recipe importer:

```bash
python3 spark.py import-monitoring-images --archive /path/to/monitoring-arm64-images.tar --allow-containerd-import
python3 spark.py monitoring
```

The importer checks the configured image tags and archive checksum before importing. Monitoring imports allow archives below 2 GiB and reserve a 2 GiB upload volume plus a 2 GiB download volume. Application-only imports retain their 1 GiB limit. It uses the image-loader's Python and K3s helper images, which must already be available for an offline import. Attachment reuses existing importer settings when present. Otherwise configure `containerd` settings or preload images through the cluster's normal runtime tooling. Set `monitoring.imagePullPolicy` to `Never` for an offline startup test. The monitoring chart is local and has no downloadable Helm dependencies. Grafana update checks and automatic plugin preinstallation are disabled.

Review the external artifact terms in [NOTICE](NOTICE), including Grafana OSS's AGPL-3.0 license, before distributing image bundles.

For an isolated offline check, set `monitoring.networkPolicy.enabled=true` and supply `monitoring.networkPolicy.apiServerCIDRs` with the Kubernetes API Service and endpoint IPs as `/32` or `/128` host CIDRs. The policy selects only this monitoring release and permits cluster pod traffic, including DNS, plus API access on TCP 443/6443. It requires a network plugin that enforces egress policies. Confirm a previously reachable external endpoint becomes unreachable, then restart monitoring with pull policy `Never` and repeat verification. Other policies can add allowed egress, so inspect them too. This verifies monitoring pod startup with external egress blocked, not a disconnected-node boot or model download.

## Verification details

Verification matches every running selected pod to fresh collected series and fails on missing components, stale samples or failed targets. Existing gateway pods are scraped directly on port 9464 even when the metrics Service port is disabled.

Traffic verification sends streaming and nonstreaming requests to the selected model. It waits up to 75 seconds for that model's request counters, latency and first-token histogram counts/sums, and prompt/completion token counters to increase. The runtime must support the gateway chat API, SSE streaming and prompt/completion token usage in both modes.

For an authorized failure rehearsal, keep Grafana open during the recipe's recovery workflow. Observe registration, tunnels, router availability and request errors during interruption and recovery. A successful render or CPU fixture does not establish real model recovery or offline Spark startup.

## Dashboard semantics

- Gateway counters provide user-visible request and token totals. Router and backend panels show their own work and are not added to gateway totals.
- Token rates reflect usage reported by completed requests. They are aggregate throughput, not an individual request's decoding speed.
- Gateway request latency includes the response duration. Router proxy latency measures upstream first byte.
- Registration, reverse tunnel connectivity and routability are separate signals. Operator condition panels show aggregate counts, while registration also has a namespace/name endpoint label.
- Health panels hide samples older than 45 seconds. Missing data stays unknown. The scrape-age panel shows stopped collection separately from a failed scrape.
- The model filter discovers gateway/router labels and defaults to All. It applies to model-labelled gateway/router metrics. Operator and Pylon panels retain endpoint/pod labels. Rejected requests can have no model label and remain visible in the errors panel.
- The default dashboard has 17 routing and collection panels. Three llama.cpp backend panels appear only when an extra target explicitly selects that runtime. They retain component/pod labels and are not filtered by model. Other runtime schemas require their own queries.

Logs and Kubernetes Events remain available through kubectl with an explicit context. This metrics release does not provision a log or trace store.
