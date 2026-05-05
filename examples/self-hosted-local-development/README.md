# Self-Hosted NVCF Local Development (k3d)

Run the full NVCF self-hosted control plane on your laptop using [k3d](https://k3d.io/) for development, testing, or demos.

> **Note**: This setup uses fake GPUs, a single Cassandra replica, and ephemeral storage. It is not suitable for production.

## Prerequisites

- [Docker](https://www.docker.com/get-started) (running)
- [k3d](https://k3d.io/#installation) v5.x or later
- [kubectl](https://kubernetes.io/docs/tasks/tools/)
- [helm](https://helm.sh/docs/intro/install/) >= 3.12
- [helmfile](https://helmfile.helmfile.sh/) >= 1.1.0, < 1.2.0
- [helm-diff](https://github.com/databus23/helm-diff) plugin
- NGC API Key from [ngc.nvidia.com](https://ngc.nvidia.com)

## Quick Start

`setup.sh` owns local cluster bootstrap and is the source of truth for this workflow. It creates the k3d cluster, installs KWOK, installs the fake GPU operator, installs CSI SMB, and installs Envoy Gateway. Product docs should reference this script instead of repeating every bootstrap command.

### 1. Create the local cluster

```bash
chmod +x setup.sh teardown.sh
./setup.sh
```

This creates a k3d cluster with:
- 6 nodes (1 server + 5 agents)
- 2 nodes with 8 simulated H100 GPUs each (via fake GPU operator)
- CSI SMB driver for shared storage
- Envoy Gateway with `shared-gw` and `grpc-gw` Gateway API resources

### 2. Deploy the NVCF stack

Follow the Local Development Guide for deployment of the helmfile - nvcf-self-managed-stack. This is available in the Self-Hosted NVCF documentation which is currently early access only. Please reach out to your NVIDIA representative for access.

1. Environment file - Download the local development environment template from the docs and save it as `environments/<name>.yaml` in your `nvcf-self-managed-stack` directory. The template uses `nvcr.io/0833294136851237/nvcf-ncp-staging` and is pre-configured for k3d.

2. Secrets - Create `secrets/<name>-secrets.yaml` with your NGC credentials.

3. Pull secrets - Configure Kyverno to inject image pull secrets.

4. **Deploy:**

```bash
helm registry login nvcr.io -u '$oauthtoken' -p "$NGC_API_KEY"
HELMFILE_ENV=<name> helmfile sync
```

### 3. Verify

```bash
# Generate an admin token
export NVCF_TOKEN=$(curl -s -X POST "http://api-keys.localhost:8080/v1/admin/keys" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['value'])")

# List functions (should return empty)
curl -s "http://api.localhost:8080/v2/nvcf/functions" \
  -H "Authorization: Bearer ${NVCF_TOKEN}" | python3 -m json.tool
```

## Accessing Routes

Routes use the `.localhost` TLD which resolves to `127.0.0.1` on most systems. Access via port 8080:

- `http://api.localhost:8080` - NVCF API
- `http://api-keys.localhost:8080` - API Keys
- `http://invocation.localhost:8080` - Function invocation

If `.localhost` doesn't resolve, add to `/etc/hosts`:

```
127.0.0.1 api.localhost
127.0.0.1 api-keys.localhost
127.0.0.1 invocation.localhost
```

## Troubleshooting

### RuntimeClass nvidia exists before fake GPU operator install

If `./setup.sh` fails while installing the fake GPU operator with `RuntimeClass "nvidia" in namespace "" exists and cannot be imported into the current release: invalid ownership metadata`, the cluster already has a `RuntimeClass/nvidia` object that is not owned by the `gpu-operator` Helm release.

For this local k3d workflow, rerun the updated `./setup.sh`. The script removes known stale fake GPU operator resources, including an unowned local `RuntimeClass/nvidia`, before installing the fake GPU operator so Helm can create and manage them.

For manual RuntimeClass recovery, first check the current ownership metadata:

```bash
kubectl get runtimeclass nvidia -o jsonpath='{.metadata.labels.app\.kubernetes\.io/managed-by}{"|"}{.metadata.annotations.meta\.helm\.sh/release-name}{"|"}{.metadata.annotations.meta\.helm\.sh/release-namespace}{"\n"}'
```

Only delete `RuntimeClass/nvidia` if the command shows that it is unowned. If `RuntimeClass/nvidia` is owned by a different Helm release, remove that release instead of deleting the runtime class directly. If Helm reports the same ownership error for another fake GPU operator resource, prefer rerunning `./setup.sh` so the known local fake GPU resources are cleaned consistently.

Then retry the fake GPU operator install:

```bash
kubectl delete runtimeclass nvidia
helm upgrade -i gpu-operator fake-gpu-operator/fake-gpu-operator \
  -n gpu-operator --create-namespace \
  --set 'topology.nodePools.default.gpuCount=8' \
  --set 'topology.nodePools.default.gpuProduct=NVIDIA-H100-80GB-HBM3' \
  --set 'topology.nodePools.default.gpuMemory=81559'
```

## Teardown

```bash
./teardown.sh <name>   # e.g., ./teardown.sh my-local
```

## What's Included

| File | Purpose |
|------|---------|
| `k3d-config.yaml` | k3d cluster configuration (5 agents, fake GPU labels, port mappings) |
| `setup.sh` | Creates cluster, installs KWOK, fake GPU operator, CSI SMB, Envoy Gateway |
| `teardown.sh` | Destroys the NVCF stack and k3d cluster |

## Limitations

- Fake GPUs - Containers deploy but cannot run actual GPU workloads
- Single Cassandra replica - No high availability
- Ephemeral storage - Data lost when the cluster is deleted
- Not for performance testing - Laptop resources do not represent production
