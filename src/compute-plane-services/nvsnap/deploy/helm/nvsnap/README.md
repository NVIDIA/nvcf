# NvSnap Helm chart

GPU checkpoint/restore for Kubernetes. Deploys the nvsnap-agent DaemonSet on GPU nodes plus the nvsnap-server REST API/UI, the nvsnap-blobstore durable backstop, and an in-agent mutating admission webhook.

## Prerequisites

- Kubernetes 1.25+
- GPU nodes labeled `nvidia.com/gpu.present=true` (NVIDIA GPU operator sets this; GKE GPU node pools too)
- A pull secret named `nvsnap-pull-secret` in the install namespace, authorized for `nvcr.io/0651155215864979/ncp-dev/*`. See [docs/PULL-SECRET-SETUP.md](../../../docs/PULL-SECRET-SETUP.md) for setup.
- cert-manager — only if you keep `webhook.enabled=true` (default). The chart's webhook block creates a self-signed Issuer + Certificate; cert-manager reconciles them into the TLS Secret the agent mounts.

## Install

```bash
# Namespace (chart does not create it — set up the pull-secret first)
kubectl create namespace nvsnap-system
kubectl create secret docker-registry nvsnap-pull-secret \
  --namespace=nvsnap-system \
  --docker-server=nvcr.io \
  --docker-username='$oauthtoken' \
  --docker-password='<NGC API key>'

# Install
helm install nvsnap deploy/helm/nvsnap --namespace nvsnap-system
```

Verify:

```bash
kubectl -n nvsnap-system get pods
kubectl -n nvsnap-system rollout status ds/nvsnap-agent
kubectl -n nvsnap-system get svc nvsnap-server   # external IP if LoadBalancer
```

## Configuration

The defaults in `values.yaml` are the conventional production setup (all four components + cert-manager-backed webhook). Common overrides:

| Override | What it does |
|---|---|
| `--set global.imagePullSecrets[0]=my-secret` | Use a different pull-secret name |
| `--set agent.enabled=false` | Server-only install (no agents on GPU nodes) |
| `--set server.enabled=false --set blobstore.enabled=false` | Agent-only install (no UI, no durable backstop) |
| `--set webhook.enabled=false` | Skip cert-manager + admission webhook; agent runs without auto-inject |
| `--set server.service.type=ClusterIP` | Use port-forward instead of LoadBalancer |
| `--set agent.runtime=crio` | CRI-O variant (default: containerd) |
| `--set server.persistence.storageClassName=my-sc` | Override storage class for the SQLite DB PVC |
| `--set agent.features.streamCheckpoint=true` | Turn on streaming checkpoint (experimental; see issue #46) |

All values:

```bash
helm show values deploy/helm/nvsnap
```

## What it deploys

With defaults (nvsnap-system namespace, all components enabled):

- 1 DaemonSet (`nvsnap-agent`)
- 2 Deployments (`nvsnap-server`, `nvsnap-blobstore`)
- 4 Services (`nvsnap-agent` headless, `nvsnap-server`, `nvsnap-blobstore`, `nvsnap-webhook`)
- 2 PVCs (`nvsnap-server-db`, `nvsnap-blobstore-data`)
- 2 ServiceAccounts + 2 ClusterRoles + 2 ClusterRoleBindings (agent, server)
- 1 cert-manager Issuer + Certificate
- 1 MutatingWebhookConfiguration

That's 18 resources total. With `agent-only` (`server.enabled=false`, `blobstore.enabled=false`, `webhook.enabled=false`) you get just 4: the agent ServiceAccount, ClusterRole, ClusterRoleBinding, DaemonSet, and headless Service.

## Uninstall

```bash
helm uninstall nvsnap --namespace nvsnap-system
```

PVCs are NOT deleted by `helm uninstall` (Helm leaves them so you don't lose checkpoint data on accident). To wipe storage too:

```bash
kubectl -n nvsnap-system delete pvc nvsnap-server-db nvsnap-blobstore-data
```

## Upgrading

```bash
helm upgrade nvsnap deploy/helm/nvsnap --namespace nvsnap-system
```

Rolling upgrade. The agent DaemonSet does a rolling update by default; nvsnap-server and nvsnap-blobstore use Recreate (PVCs are RWO, can't run two pods on one).

## Development

The templates live in `templates/`. After editing:

```bash
# Lint
helm lint deploy/helm/nvsnap

# Render to stdout (sanity check; no API server needed)
helm template nvsnap deploy/helm/nvsnap --namespace nvsnap-system

# Dry-run install against a real cluster (catches schema errors,
# admission webhook rejections, etc.)
helm install nvsnap deploy/helm/nvsnap --namespace nvsnap-system --dry-run
```

When the agent or any image version moves, bump it in `Chart.yaml`'s `appVersion` (or override per-component via `image.tag`).
