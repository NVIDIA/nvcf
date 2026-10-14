# NVCF Gateway UI Helm chart

This chart deploys the NVCF Gateway UI: one Deployment running the backend-for-frontend (BFF), which serves the UI and its API, plus a Service, an optional Ingress and a ServiceAccount.

The BFF talks to the LLM API Gateway through the gateway's in-cluster Service, with its own gateway API key. The key stays in the cluster: the browser only ever talks to the BFF.

All values live under `nvcfSparkUi` in [values.yaml](values.yaml).

## Before you install

You need three things.

1. **An image of the UI** that your cluster can pull.
   - Build it from the repository root with `make image-build IMAGE=<registry>/nvcf-spark-ui TAG=<tag>`, then push it. The default platform is `linux/arm64`; set `PLATFORM` for another.
   - For a cluster that pulls from no registry, `make image-tar` saves the image to a tarball. Import it into every node's container runtime, and set `image.pullPolicy: Never`.
2. **A running LLM API Gateway**, and its Service's in-cluster address, such as `https://llm-api-gateway.<stack-namespace>.svc.cluster.local:8080`.
3. **The gateway's key and CA certificate in the UI's namespace.** A pod can only mount Secrets and ConfigMaps from its own namespace, so copy them there if the gateway runs elsewhere.
   - A Secret holding the BFF's gateway API key: by default `demo-ui-api-key`, field `api-key`.
   - A ConfigMap holding the CA certificate the gateway's TLS certificate is signed by: by default `llm-gateway-stack-ca`, key `ca.crt`.

These commands copy both from the stack's namespace without printing the key. Server-side apply also keeps the key out of the `last-applied-configuration` annotation, which `kubectl describe` would show.

```bash
STACK=<stack-namespace>   # where the gateway runs
UI=<ui-namespace>         # where the UI will run
kubectl create namespace "$UI"

kubectl -n "$STACK" get configmap llm-gateway-stack-ca -o json \
  | jq --arg ns "$UI" '{apiVersion, kind, metadata: {name: .metadata.name, namespace: $ns}, data: {"ca.crt": .data["ca.crt"]}}' \
  | kubectl apply --server-side -f -

# <key-secret> is the Secret with the caller key your gateway accepts.
kubectl -n "$STACK" get secret <key-secret> -o json \
  | jq --arg ns "$UI" '{apiVersion, kind, type, metadata: {name: "demo-ui-api-key", namespace: $ns}, data: {"api-key": .data["api-key"]}}' \
  | kubectl apply --server-side -f -
```

The recipe catalog is optional. Without it the UI still runs, and its recipes page says the catalog is unavailable. See [The recipe catalog](#the-recipe-catalog).

## Install

Put your settings in a values file, `my-values.yaml`:

```yaml
nvcfSparkUi:
  fullnameOverride: demo-ui
  image:
    repository: <registry>/nvcf-spark-ui
    tag: <tag>
  gateway:
    url: https://llm-api-gateway.<stack-namespace>.svc.cluster.local:8080
  ingress:
    enabled: true
    className: traefik
```

Then install from the repository root:

```bash
helm lint helm -f my-values.yaml
helm upgrade --install demo-ui helm -n <ui-namespace> -f my-values.yaml --wait
```

`helm upgrade --install` both installs and upgrades, so the same command applies later changes.

The NOTES printed after the install say how to open the UI:

- With the Ingress on, open `http://<ingress host or any node address>/`.
- With a `NodePort` Service, open the port Kubernetes picked.
- Otherwise, use a port-forward: `kubectl -n <ui-namespace> port-forward svc/demo-ui 8300:8300`, then open `http://localhost:8300`.

## Values

### Image and placement

| Value | Default | What it does |
| --- | --- | --- |
| `image.repository` | `nvcf-spark-ui` | Image to run. |
| `image.tag` | the chart's `appVersion` | Image tag. Set it to the tag you built. |
| `image.pullPolicy` | `IfNotPresent` | Use `Never` for images imported into the nodes, with no registry. |
| `imagePullSecrets` | `[]` | Secrets for a private registry, as `[{name: <secret>}]`. |
| `fullnameOverride` | empty | Name of every resource the chart creates. Without it, they are named `<release>-helm-nvcf-spark-ui`, unless the release name already contains `helm-nvcf-spark-ui`. |
| `replicaCount` | `1` | Pods to run. The BFF keeps no state, so more replicas only add capacity. |
| `resources` | 50m CPU and 64Mi memory requested, 256Mi limit | Ample for a demo; raise them for many concurrent chats. |
| `nodeSelector`, `tolerations`, `affinity` | none | Standard pod placement. |
| `podAnnotations`, `podLabels` | none | Extra metadata on the pods. |

### Gateway

| Value | Default | What it does |
| --- | --- | --- |
| `gateway.url` | `https://llm-api-gateway:8080` | The gateway's in-cluster Service. The short name only resolves from the gateway's own namespace; from another namespace, use the full `<service>.<namespace>.svc.cluster.local` name. Never use an ingress or a port-forward. |
| `gateway.apiKey.secret`, `gateway.apiKey.key` | `demo-ui-api-key`, `api-key` | Secret and field holding the BFF's gateway API key. A missing or empty key stops the BFF. |
| `gateway.ca.configMap`, `gateway.ca.key` | `llm-gateway-stack-ca`, `ca.crt` | ConfigMap and key holding the CA certificate to trust for the gateway. Set `configMap` to empty to trust the system roots instead. |
| `gateway.timeoutSeconds` | `null`, which means 15 | Limit on registry and model reads. |
| `gateway.chatTimeoutSeconds` | `null`, which means 600 | Write deadline of one streamed chat reply. `0` removes it. |

### What the UI shows

| Value | Default | What it does |
| --- | --- | --- |
| `ui.gatewayPublicUrl` | empty | The gateway's address as clients outside the cluster reach it. It appears in the header and in the `curl` snippets. Empty shows a `$GW_URL` placeholder. It must not be this UI's own address plus `/v1`. |
| `ui.grafanaUrl` | empty | Grafana dashboard each endpoint links to, with its model added as `var-model`. Use an absolute URL, or a path such as `/grafana/d/llm-demo` for a Grafana served on the same host. Empty hides the links. |

### The recipe catalog

| Value | Default | What it does |
| --- | --- | --- |
| `recipes.configMap` | empty, which means `<fullname>-recipes` | ConfigMap holding the recipe catalog. The pod starts without it. |
| `recipes.key` | `index.json` | Key of the catalog in that ConfigMap. |

`make recipes-configmap` builds the ConfigMap from the recipe charts' published catalog. It prints YAML, so apply it with kubectl:

```bash
make -s recipes-configmap RECIPES_NAMESPACE=<ui-namespace> | kubectl apply --server-side -f -
```

The chart mounts the ConfigMap as a directory, and the BFF reads the catalog again whenever it changes. Re-running the command updates the recipes page within about a minute, with no restart or upgrade. The root [README](../README.md#deploy-helpers) lists the command's variables.

### Exposure

| Value | Default | What it does |
| --- | --- | --- |
| `service.type` | `ClusterIP` | Use `ClusterIP` behind an Ingress, or `NodePort` on a cluster without one. |
| `service.port` | `8300` | Port of the Service and of the BFF. |
| `service.nodePort` | `null` | With `NodePort`: the port to use, or `null` to let Kubernetes pick. |
| `ingress.enabled` | `false` | Create an Ingress that routes `ingress.path` and everything under it to the BFF. |
| `ingress.className` | empty | IngressClass to use, such as `traefik`; empty uses the cluster's default. |
| `ingress.host` | empty | Host to match; empty matches every host, such as the nodes' addresses. |
| `ingress.path` | `/` | Path prefix. Longer prefixes from other Ingresses, such as a Grafana at `/grafana`, still win. |
| `ingress.annotations` | none | Controller-specific settings. |

The Ingress has no TLS settings yet, so the UI is served over HTTP.

### Server timeouts

`config.timeouts` sets the BFF's connection timeouts, in seconds. Streamed chat replies extend their own write deadline, so `writeSeconds` doesn't need to cover them. A `0` disables that deadline; only use it with a proven need.

| Value | Default |
| --- | --- |
| `config.timeouts.readHeaderSeconds` | `10` |
| `config.timeouts.readSeconds` | `30` |
| `config.timeouts.writeSeconds` | `60` |
| `config.timeouts.idleSeconds` | `120` |

### Security

The defaults are hardened: the pod runs as the non-root user 65532 with a read-only root filesystem, every Linux capability is dropped, and the pod uses the `RuntimeDefault` seccomp profile. The ServiceAccount mounts no API token, because the BFF doesn't call the Kubernetes API. Change `podSecurityContext`, `securityContext` or `serviceAccount.annotations` only if your cluster's policies require it.

### Health probes

The liveness and readiness probes `GET /status`, which has no dependencies. Their timing is in `livenessProbe` and `readinessProbe`.

## Check the install

```bash
kubectl -n <ui-namespace> rollout status deploy/demo-ui
kubectl -n <ui-namespace> logs deploy/demo-ui | grep -i -E 'recipe catalog|error'
```

Through the Ingress or a port-forward:

- `GET /status` answers `200`.
- `GET /api/v1/recipes` answers `200` once the catalog ConfigMap exists, and `503` until then.
- `GET /v1/registry` lists the gateway's models, which proves the key and the CA work.

## When the gateway key rotates

The BFF reads the key once, at startup, and Kubernetes doesn't restart pods when a Secret changes. So copy the new key into the UI's namespace as above, then run the same `helm upgrade --install` command. The chart puts a checksum of the key on the pod template, so the upgrade rolls the pod onto the new key.

The checksum comes from a `lookup` of the Secret. `helm template` and `--dry-run` can't look it up, so their output has no checksum annotation.

## Uninstall

```bash
helm uninstall demo-ui -n <ui-namespace>
```

The Secret and ConfigMaps you created yourself stay; delete them, or the namespace, separately.
