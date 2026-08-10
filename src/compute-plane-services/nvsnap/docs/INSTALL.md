# Installing NvSnap

This is the full prerequisite + install guide for getting NvSnap running on a fresh GPU Kubernetes cluster.

For the one-line install once you have prerequisites met, jump to [Quick install](#quick-install).

## Prerequisites

### 1. Kubernetes cluster with GPU nodes

| Requirement | Why |
|---|---|
| Kubernetes 1.25+ | Some manifests use features from 1.25 (e.g. `seccompProfile` on PodSecurityContext) |
| GPU nodes labeled by the NVIDIA GPU Operator, GKE, or Node Feature Discovery | NvSnap's agent DaemonSet schedules on nodes matching any of: `nvidia.com/gpu.present="true"`, `cloud.google.com/gke-gpu="true"`, `feature.node.kubernetes.io/pci-10de.present="true"` |
| NVIDIA driver **555 or newer** on every GPU node | `cuda-checkpoint` (the binary nvsnap uses for GPU state) requires driver ≥ 555. On GKE this is the default for current node images; on bare-metal, check `nvidia-smi`. |
| containerd 1.7 or 2.x as the container runtime | NvSnap reads from containerd's snapshot directory on disk. CRI-O is also supported (use the CRI-O variant manifest). |
| Linux kernel with io_uring + cgroup v2 | Standard on any modern distro (Ubuntu 22.04+, RHEL 9+, GKE COS, etc.) |

### 2. NGC account + API key

NvSnap's container images live at `nvcr.io/0651155215864979/ncp-dev/`. To pull them you need:

1. An NGC account at <https://ngc.nvidia.com>.
2. An API key from **Setup → API Key → Generate**. Copy the key — NGC shows it only once.

See [PULL-SECRET-SETUP.md](PULL-SECRET-SETUP.md) for the K8s side once you have the key.

### 3. Local tooling

| Tool | Version | Purpose |
|---|---|---|
| `kubectl` | matching your cluster | Apply manifests + verify pods |
| `helm` | 3.x (3.13+ for the chart's APIs) | Install the nvsnap chart |
| `docker` (optional) | any | Only needed if you'll build images locally; not needed for installing pre-built images |

### 4. (Optional) cert-manager

NvSnap's admission webhook (off by default in the chart) needs cert-manager to mint its TLS cert. Skip if you're not using the webhook; install if you want auto-injection of the nvsnap-init container into restored workload pods.

Install once per cluster:

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
kubectl wait --for=condition=Available --timeout=300s -n cert-manager deployment/cert-manager
```

## Quick install

`scripts/install-nvsnap.sh` does everything below in one go — prereq checks, namespace, pull-secret, optional cert-manager, and helm install. Reads the NGC API key from `~/.docker/config.json` if you've already done `docker login nvcr.io`.

```bash
# Bare-minimum install (agent + server + blobstore, no webhook)
./scripts/install-nvsnap.sh

# Full install with webhook (requires cert-manager)
./scripts/install-nvsnap.sh --with-webhook

# Custom namespace
./scripts/install-nvsnap.sh --namespace my-nvsnap

# Provide NGC API key explicitly
NGC_API_KEY=nvapi-... ./scripts/install-nvsnap.sh
```

Verify:

```bash
kubectl -n nvsnap-system get pods
kubectl -n nvsnap-system rollout status ds/nvsnap-agent
kubectl -n nvsnap-system get svc nvsnap-server   # external IP if LoadBalancer
```

## Manual install

If you'd rather do the steps by hand or you need to deviate from the script's choices.

### Step 1: Namespace

```bash
kubectl create namespace nvsnap-system
```

### Step 2: Pull secret

Replace `<NGC_API_KEY>` below with the value from <https://ngc.nvidia.com> → Setup → API Key.

```bash
kubectl create secret docker-registry nvsnap-pull-secret \
  --namespace=nvsnap-system \
  --docker-server=nvcr.io \
  --docker-username='$oauthtoken' \
  --docker-password='<NGC_API_KEY>'
```

The username is the literal string `$oauthtoken` (note the leading `$` and lowercase letters — that's the actual username NGC expects, not a placeholder). The password is the API key. See [PULL-SECRET-SETUP.md](PULL-SECRET-SETUP.md) for rotation + mirroring.

### Step 3: (Optional) cert-manager

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
kubectl wait --for=condition=Available --timeout=300s -n cert-manager deployment/cert-manager deployment/cert-manager-webhook deployment/cert-manager-cainjector
```

### Step 4: Helm install

```bash
# From a checkout of the nvsnap repo:
helm install nvsnap deploy/helm/nvsnap --namespace nvsnap-system
```

With customizations:

```bash
# Without the webhook (skip cert-manager dependency)
helm install nvsnap deploy/helm/nvsnap --namespace nvsnap-system --set webhook.enabled=false

# Agent-only (no UI, no blobstore — for clusters that talk to an
# external nvsnap-server)
helm install nvsnap deploy/helm/nvsnap --namespace nvsnap-system \
  --set server.enabled=false --set blobstore.enabled=false

# CRI-O cluster (instead of containerd)
helm install nvsnap deploy/helm/nvsnap --namespace nvsnap-system --set agent.runtime=crio

# Override storage class
helm install nvsnap deploy/helm/nvsnap --namespace nvsnap-system \
  --set server.persistence.storageClassName=my-sc \
  --set blobstore.persistence.storageClassName=my-sc
```

See [deploy/helm/nvsnap/README.md](../deploy/helm/nvsnap/README.md) for the full values reference.

### Step 5: Verify

```bash
# Pods coming up (give it 1–2 min for image pulls)
kubectl -n nvsnap-system get pods

# Agent on every GPU node
kubectl -n nvsnap-system rollout status ds/nvsnap-agent

# Server reachable
kubectl -n nvsnap-system get svc nvsnap-server   # external IP if LoadBalancer
```

If the agent DaemonSet shows `0/0` (no nodes match), check your GPU node labels:

```bash
kubectl get nodes -L nvidia.com/gpu.present,cloud.google.com/gke-gpu,feature.node.kubernetes.io/pci-10de.present
```

If your cluster uses a different GPU label, override with:

```bash
helm upgrade nvsnap deploy/helm/nvsnap --namespace nvsnap-system \
  --reuse-values \
  --set 'agent.nodeAffinity=null' \
  --set agent.nodeSelector.your\\.label=value
```

## Uninstall

```bash
helm uninstall nvsnap --namespace nvsnap-system

# Optional: drop the PVCs too (Helm leaves them on uninstall to protect data)
kubectl -n nvsnap-system delete pvc nvsnap-server-db nvsnap-blobstore-data

# Optional: drop the namespace + pull-secret
kubectl delete namespace nvsnap-system
```

## Troubleshooting

### Agent pods stuck in `ImagePullBackOff`

```
kubectl describe pod -n nvsnap-system <agent-pod-name> | tail -20
```

Usually means the pull-secret is missing, has the wrong API key, or hasn't been created in the namespace. Run step 2 again.

### Agent pods stuck in `Pending`

Either no nodes match the nodeSelector / nodeAffinity, or all matching nodes have an untolerated taint.

```
kubectl describe pod -n nvsnap-system <agent-pod-name> | grep -A5 "Events\|Conditions"
```

See the section above on overriding `agent.nodeAffinity`.

### nvsnap-server external IP is `<pending>` forever

You're either not on a cloud provider that supports LoadBalancer, or your cluster's network plugin doesn't have a LB controller. Switch to ClusterIP and use port-forward:

```bash
helm upgrade nvsnap deploy/helm/nvsnap --namespace nvsnap-system --reuse-values --set server.service.type=ClusterIP
kubectl -n nvsnap-system port-forward svc/nvsnap-server 8080:8080
```

### Checkpoint times out or fails with `cuda-checkpoint not found`

cuda-checkpoint is bundled in the agent image at `/criu-bundle/cuda-checkpoint`. If it's failing to find it, the agent image didn't pull correctly. Re-check `kubectl describe pod` and `kubectl logs`.

If `cuda-checkpoint` is starting but failing with a kernel-ABI mismatch, your nodes have an NVIDIA driver older than 555. Upgrade the driver.

## Where things live after install

| Object | Where |
|---|---|
| Agent DaemonSet | `nvsnap-system/nvsnap-agent` |
| Server Deployment | `nvsnap-system/nvsnap-server` (Service on port 8080, LoadBalancer by default) |
| Blobstore Deployment | `nvsnap-system/nvsnap-blobstore` (Service on port 9000, ClusterIP) |
| Server SQLite DB | PVC `nvsnap-server-db`, 1 GiB |
| Blobstore data | PVC `nvsnap-blobstore-data`, 1 TiB |
| Webhook (if enabled) | MutatingWebhookConfiguration `nvsnap-rootfs-restore`, Service `nvsnap-webhook`, cert-manager Issuer/Certificate `nvsnap-webhook-*` |
| Checkpoint storage on each node | `/var/lib/containerd/nvsnap-checkpoints/` (CRIU dumps), `/var/lib/nvsnap/cache/` (rootfs-only cache) |
