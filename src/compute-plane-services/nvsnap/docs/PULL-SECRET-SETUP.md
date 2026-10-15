# Image pull secret setup

NvSnap's container images live on NVIDIA's private NGC registry at `nvcr.io/0651155215864979/ncp-dev/`. Kubernetes nodes pulling them need a Docker-style pull secret. This doc walks through obtaining one and wiring it in.

## TL;DR

```bash
# 1. Get an NGC API key (one-time, per user)
#    https://ngc.nvidia.com → Setup → API Key → Generate
#    Save the resulting key — NGC won't show it twice.

# 2. Create the pull secret in the nvsnap-system namespace
kubectl create namespace nvsnap-system  # if not already there
kubectl create secret docker-registry nvsnap-pull-secret \
  --docker-server=nvcr.io \
  --docker-username='$oauthtoken' \
  --docker-password='<paste NGC API key here>' \
  --namespace=nvsnap-system

# 3. Same secret for any namespace where NvSnap-managed workloads live
kubectl create namespace your-workload-ns
kubectl create secret docker-registry nvsnap-pull-secret \
  --docker-server=nvcr.io \
  --docker-username='$oauthtoken' \
  --docker-password='<paste NGC API key here>' \
  --namespace=your-workload-ns
```

Manifests reference the secret by name as `nvsnap-pull-secret` (the convention used across `deploy/k8s/*.yaml` and the Helm chart). Override the name via `--set imagePullSecrets[0].name=...` if you've already standardized on a different one.

## Getting an NGC API key

1. Sign in to <https://ngc.nvidia.com> with your NVIDIA SSO account.
2. Click your username in the top-right, choose **Setup**, then **Get API Key**.
3. Click **Generate API Key**. NGC displays the key exactly once — copy it immediately.

The username for `docker login` is the literal string `$oauthtoken` (note the leading `$` and lowercase). The password is the API key.

If you previously generated a key and lost it, you can generate a new one — that revokes the old one.

## What the secret authorizes

The pull secret only lets the cluster *pull* images. It does not grant push access. Push requires a different login flow on the developer machine:

```bash
docker login nvcr.io
  Username: $oauthtoken
  Password: <NGC API key>
```

See [CONTRIBUTING.md](../CONTRIBUTING.md) for the developer setup including push.

## Mirroring the secret across namespaces

NvSnap's image references appear in three places:

| Manifest | Namespace it lands in |
|---|---|
| `deploy/k8s/agent-daemonset.yaml` (nvsnap-agent DaemonSet) | `nvsnap-system` |
| `deploy/k8s/nvsnap-server.yaml` + `nvsnap-blobstore.yaml` | `nvsnap-system` |
| `deploy/k8s/workloads/*.yaml` (vLLM / SGLang / TRT-LLM / NIM pods) | whatever your workloads use |
| `nvsnap-init` (init container injected into restored pods) | matches the workload's namespace |

The pull secret must exist in **every namespace** where one of these pods will land. There's no cluster-wide pull-secret in Kubernetes; namespaces are independent.

For a fresh cluster running the full demo set:

```bash
for ns in nvsnap-system default kube-system; do
  kubectl create secret docker-registry nvsnap-pull-secret \
    --docker-server=nvcr.io \
    --docker-username='$oauthtoken' \
    --docker-password='<NGC API key>' \
    --namespace=$ns \
    --dry-run=client -o yaml | kubectl apply -f -
done
```

(`--dry-run=client | apply` makes the command idempotent — safe to re-run.)

## Alternatives to per-namespace secrets

### Workload Identity (GKE)

On GKE, you can bind a Kubernetes ServiceAccount to a GCP IAM service account that has Artifact Registry Reader on a mirrored copy of the images, so the pull-secret disappears entirely. The mirroring step (source registry → Artifact Registry) is a one-time setup using `gcloud artifacts docker images copy` or a periodic sync job.

### Cluster-wide pull-secret operators

For multi-tenant production clusters, install something like [reflector](https://github.com/emberstack/kubernetes-reflector) and annotate the source secret in `nvsnap-system`. New namespaces auto-inherit the secret. Not needed for bench / dev setups.

## Rotation

NGC API keys don't expire by default, but rotate them on a regular cadence (90 days) and immediately if there's any chance the key leaked.

```bash
# 1. Generate a new key at https://ngc.nvidia.com → Setup → API Key
# 2. For each namespace using the secret:
kubectl delete secret nvsnap-pull-secret -n <namespace>
kubectl create secret docker-registry nvsnap-pull-secret \
  --docker-server=nvcr.io \
  --docker-username='$oauthtoken' \
  --docker-password='<new key>' \
  --namespace=<namespace>
# 3. Restart the pods that were authed by the old key:
kubectl rollout restart daemonset/nvsnap-agent -n nvsnap-system
kubectl rollout restart deployment/nvsnap-server -n nvsnap-system
kubectl rollout restart deployment/nvsnap-blobstore -n nvsnap-system
```

Already-pulled images keep working until the kubelet's image GC runs or the pod is rescheduled to a node that doesn't have the image cached — so a few minutes of overlap during rotation is normal.

## Troubleshooting

### `ImagePullBackOff` with "unauthorized: authentication required"

The secret is either missing or has the wrong API key.

```bash
kubectl describe pod <name> -n <ns>           # confirms which secret was tried
kubectl get secret nvsnap-pull-secret -n <ns>   # confirms it exists
kubectl get secret nvsnap-pull-secret -n <ns> -o json | \
  jq -r '.data.".dockerconfigjson"' | base64 -d
# That output should be a valid Docker auth JSON for nvcr.io
```

### `ErrImagePull` with "not found"

The secret is fine; the image tag doesn't exist on NGC. Check the manifest's image reference and compare to what `scripts/versions.sh` says is the current canonical tag.

### Pod stuck pulling for >5 minutes

NGC has region-specific rate limits during high-traffic windows. If multiple pods are pulling 30 GB CUDA images simultaneously, that's the bottleneck — not the secret. Consider [Image Streaming on GKE](https://cloud.google.com/kubernetes-engine/docs/how-to/image-streaming) or mirroring to a closer registry.
