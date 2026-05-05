---
name: nvcf-self-managed-installation
description: >-
  Install and deploy the nvcf-self-managed-stack helmfile bundle for NVCF
  self-hosted deployments. Covers clean installation, teardown, helm values
  overrides, image pull secrets, fake GPU operator setup, and debugging
  installation failures. Use when deploying, installing, reinstalling, tearing
  down, or configuring the NVCF self-managed control plane stack, or when the
  user mentions helmfile, self-managed, self-hosted, control plane installation,
  nvcf-self-managed-stack, or fake GPU operator. Do NOT use for local k3d
  development environments. For local NVCF self-hosted or self-managed cluster
  setup with k3d, use the local k3d development workflow instead.
license: Apache-2.0
compatibility: Requires helmfile >= 1.1.0 < 1.2.0, helm >= 3.12, helm-diff plugin, kubectl matching cluster version
author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
version: "1.0.0"
tags: [nvcf, self-managed, helmfile, self-hosted, control-plane, installation, deployment, pull-secrets, fake-gpu-operator]
tools: [Shell, Read, Edit, Grep, Glob]
metadata:
  internal: false
  author: "nvcf-core-eng <nvcf-core-eng@exchange.nvidia.com>"
  version: "1.0"
  tags: [nvcf, self-managed, helmfile, self-hosted, control-plane, installation, deployment, pull-secrets, fake-gpu-operator]
  languages: [bash, yaml]
  frameworks: [helmfile, helm, kubectl]
  domain: cloud-infrastructure
---

# NVCF Self-Managed Stack Operations

Operational guide for the `nvcf-self-managed-stack` helmfile bundle used to deploy the NVCF control plane.

## Instructions

Use this skill for install, upgrade, or teardown work in `nvcf-self-managed-stack`; validate tooling first, follow the documented helmfile flow, and prefer targeted troubleshooting over ad hoc chart edits.

## Prerequisites

Before any operation, ask the user for the path to their extracted `nvcf-self-managed-stack` directory. Verify it contains the expected structure:

```bash
ls <user-provided-path>/helmfile.d/ <user-provided-path>/environments/ <user-provided-path>/secrets/ <user-provided-path>/global.yaml.gotmpl
```

If the directory does not exist or is missing expected files, guide the user to download the stack package from NGC:

```bash
ngc registry resource download-version <org>/nvcf-self-managed-stack:<version>
tar xzf nvcf-self-managed-stack-<version>.tar.gz
```

All subsequent commands assume you are inside this directory.

## Before You Start

Verify tooling and context before any operation:

```bash
helmfile --version   # Must be 1.1.x (1.2.0 removed sequential mode)
helm version         # Must be >= 3.12
helm plugin list     # Must include helm-diff >= 3.11
kubectl version      # Client must be within 1 minor version of cluster
```

All commands run from inside the extracted `nvcf-self-managed-stack/` directory:

```bash
cd path/to/nvcf-self-managed-stack
ls helmfile.d/ environments/ secrets/ global.yaml.gotmpl
```

Identify your environment name -- it corresponds to `environments/<name>.yaml` and `secrets/<name>-secrets.yaml`.

For a commented example of an EKS environment file, see [references/eks-example.yaml](references/eks-example.yaml).

## How Values Flow

Understanding value precedence prevents the most common configuration mistakes.

```
environments/base.yaml          (defaults)
    ↓ merged with
environments/<env>.yaml         (your overrides)
    ↓ consumed by
global.yaml.gotmpl              (Go template, constructs per-chart values)
    ↓ consumed by
secrets/<env>-secrets.yaml      (sensitive values)
    ↓ overridden by
release inline values: blocks   (highest precedence)
```

**Critical**: `global.yaml.gotmpl` only passes through specific keys to each chart (image registries, node selectors, storage, replica counts). Setting an arbitrary chart value in the environment file will be silently ignored if `global.yaml.gotmpl` does not propagate it.

To override arbitrary chart values, use a helmfile release `values:` block. See [Overriding Helm Chart Values](#overriding-helm-chart-values).

## Clean Installation

### 1. Create namespaces and image pull secrets (if using a private registry)

```bash
for ns in cassandra-system nats-system nvcf api-keys ess sis nvca-operator vault-system; do
  kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
done

# Only if pulling from a private registry (e.g., NGC nvcr.io)
export NGC_API_KEY="<your-key>"
for ns in cassandra-system nats-system nvcf api-keys ess sis nvca-operator vault-system; do
  kubectl create secret docker-registry nvcr-creds \
    --docker-server=nvcr.io \
    --docker-username='$oauthtoken' \
    --docker-password="$NGC_API_KEY" \
    --namespace="$ns" \
    --dry-run=client -o yaml | kubectl apply -f -
done
```

If using pull secrets, you must also configure each helmfile release to reference them. See [Image Pull Secrets](#image-pull-secrets).

### 2. Authenticate helm to your chart registry

Both `docker login` AND `helm registry login` are required for NGC. Helmfile uses helm (not docker) to pull OCI charts, so the helm credential store must be authenticated separately.

```bash
# NGC -- both commands are required
docker login nvcr.io -u '$oauthtoken' -p "$NGC_API_KEY"
helm registry login nvcr.io -u '$oauthtoken' -p "$NGC_API_KEY"

# ECR
aws ecr get-login-password --region <region> | \
  helm registry login --username AWS --password-stdin <account>.dkr.ecr.<region>.amazonaws.com
```

### 3. Set the gateway address in your environment file

Your environment file (`environments/<env>.yaml`) requires `global.domain` to be set to the external address of your Envoy Gateway or load balancer. How to obtain it depends on timing:

**If Envoy Gateway is already installed** (recommended — install it before the NVCF stack):

```bash
GATEWAY_ADDRESS=$(kubectl get gateway -n envoy-gateway -o jsonpath='{.items[0].status.addresses[0].value}')
echo "$GATEWAY_ADDRESS"
```

Set this value in your environment file:

```yaml
global:
  domain: "<GATEWAY_ADDRESS>"
```

**If Envoy Gateway is not yet installed**: Use a placeholder value (e.g., `placeholder.local`), complete the `helmfile sync`, then update the domain once the gateway address is available and re-sync. See Example 4 in [examples.md](examples.md) for the update flow.

**On AWS EKS**: The gateway address is typically the DNS name of the AWS Network Load Balancer created by the Envoy Gateway controller (e.g., `k8s-envoygateway-xxxxxxxx.us-west-2.elb.amazonaws.com`).

### 4. Preview and deploy

```bash
HELMFILE_ENV=<env-name> helmfile template   # Preview rendered manifests
HELMFILE_ENV=<env-name> helmfile sync       # Deploy
```

### 5. Deployment phases

Helmfile deploys in order with dependencies:

| Phase | Selector | Services | Wait time |
|-------|----------|----------|-----------|
| 1 | `release-group=dependencies` | NATS, OpenBao, Cassandra | 5-10 min |
| 2 | `release-group=services` | API, SIS, ESS, invocation, grpc-proxy, notary, api-keys, optional LLM gateway/router | 5-10 min |
| 3 | `release-group=ingress` | Gateway routes | 1-2 min |
| 4 | `release-group=observability` | Observability stack (if enabled) | 1-2 min |
| 5 | `release-group=workers` | NVCA operator (opt-in, see below) | 1-2 min |

Monitor in a separate terminal:

```bash
kubectl get pods -A -w
```

### 6. Enable and validate the NVCA operator (opt-in)

The `nvca-operator` release is **disabled by default** (the helmfile defaults `nvcaOperator.enabled: false`, and the release carries `condition: nvcaOperator.enabled`). Phase 5 silently deploys nothing until you opt in. Chart: `nvcf/helm-nvca-operator` into namespace `nvca-operator`.

Enable in `environments/<env-name>.yaml`, then apply just this release:

```yaml
nvcaOperator:
  enabled: true
```

```bash
HELMFILE_ENV=<env-name> helmfile --selector name=nvca-operator template
HELMFILE_ENV=<env-name> helmfile --selector name=nvca-operator sync
```

Validate **only the operator Deployment itself** at this point:

```bash
kubectl rollout status deployment/nvca-operator -n nvca-operator --timeout=180s
kubectl get pods -n nvca-operator -l app.kubernetes.io/name=nvca-operator
kubectl logs deployment/nvca-operator -n nvca-operator --tail=100 | grep -iE "error|bootstrap|ready"
kubectl get ns nvca-system nvcf-backend   # auto-created by the operator
```

Operator-Deployment healthy state: `1/1 Ready`, no `CrashLoopBackOff`, no `no backend GPUs were found` in logs. If GPUs are missing, install the fake GPU operator per the section below and re-register.

**Install is not complete yet.** Once the operator has created the `nvca-system` and `nvcf-backend` namespaces, workloads in those namespaces will fail with `ImagePullBackOff` (and, if Kyverno is used, admission denials) until the pull secrets and Kyverno policy are propagated to them. Follow [Post-helmfile-sync: handle nvca-system namespace](#post-helmfile-sync-handle-nvca-system-namespace) before treating the NVCA install as done. Only after pods in `nvca-system` and `nvcf-backend` reach `Running` should you consider the operator end-to-end healthy.

## Clean Teardown

**Scope**: Only destroy releases managed by this helmfile stack. The NVCF releases are: `nats`, `openbao-server`, `cassandra`, `api-keys`, `sis`, `api`, `invocation-service`, `grpc-proxy`, `ess-api`, `notary-service`, `admin-issuer-proxy`, `ingress`, `nvca-operator`. The NVCF namespaces are: `cassandra-system`, `nats-system`, `nvcf`, `api-keys`, `ess`, `sis`, `nvca-operator`, `vault-system`, plus operator-created `nvca-system` and `nvcf-backend`. Do **NOT** delete other helm releases or namespaces on the cluster.

### Standard teardown

Run from inside the `nvcf-self-managed-stack/` directory:

```bash
HELMFILE_ENV=<env-name> helmfile destroy
```

### If NVCA resources hang (finalizers)

The `nvcf-backend` and `nvca-operator` namespaces will get stuck in `Terminating` due to finalizers. Remove the finalizers and force-delete the stuck namespaces:

```bash
# Remove finalizers from NVCFBackend custom resources
kubectl get nvcfbackends -A -o json | \
  jq '.items[] | .metadata.namespace + "/" + .metadata.name' -r | \
  xargs -I{} sh -c 'ns="${1%%/*}"; name="${1##*/}"; kubectl patch nvcfbackend "$name" -n "$ns" --type=merge -p "{\"metadata\":{\"finalizers\":[]}}"' _ {}

# Force-delete stuck namespaces by clearing their finalizers
for ns in nvca-operator nvcf-backend; do
  kubectl get namespace "$ns" -o json 2>/dev/null | \
    jq '.spec.finalizers = []' | \
    kubectl replace --raw "/api/v1/namespaces/$ns/finalize" -f - 2>/dev/null
done
```

### Delete namespaces

```bash
for ns in cassandra-system nats-system nvcf api-keys ess sis nvca-operator vault-system nvca-system nvcf-backend; do
  kubectl delete namespace "$ns" --ignore-not-found
done
```

### Verify clean

```bash
kubectl get ns | grep -E '(cassandra|nats|vault|nvcf|api-keys|ess|sis|nvca)'
# Should return empty (nvca-modelcache-init is unrelated and can be ignored)
```

## Overriding Helm Chart Values

### Environment file (limited)

Works only for keys that `global.yaml.gotmpl` propagates (e.g., `cassandra.replicaCount`, `global.storageClass`, `global.image`).

### Release values block (any chart value)

Edit the release in `helmfile.d/*.yaml.gotmpl` and add a `values:` block. In the examples below, `<private-values>` refers to the `secrets/` directory at the helmfile stack root.

```yaml
- name: cassandra
  version: 0.8.0
  condition: cassandra.enabled
  namespace: cassandra-system
  <<: *dependency
  values:
    - ../global.yaml.gotmpl
    - ../<private-values>/{{ requiredEnv "HELMFILE_ENV" }}-secrets.yaml
    - cassandra:
        resources:
          limits:
            cpu: "8"
            memory: 8192Mi
          requests:
            cpu: "2"
            memory: 4096Mi
```

**YAML merge gotcha**: When a release uses `<<: *dependency` or `inherit`, specifying `values:` **replaces** the template's values list. You must re-include `global.yaml.gotmpl` and the secrets file.

### Preview and apply a single release

```bash
HELMFILE_ENV=<env> helmfile --selector name=cassandra template  # Preview
HELMFILE_ENV=<env> helmfile --selector name=cassandra sync      # Apply
```

## Image Pull Secrets

There are two distinct credential types:

| | Control Plane Pull Secrets | API Bootstrap Registry Creds |
|---|---|---|
| Purpose | K8s pulls NVCF service images | NVCF API pulls user function images |
| Config | K8s Secrets + Kyverno ClusterPolicy | `<private-values>/<env>-secrets.yaml` |

### Configuring with Kyverno (recommended)

Use a Kyverno mutating admission policy to automatically inject `imagePullSecrets` into all pods in NVCF namespaces. This works uniformly for all charts -- no per-chart configuration or helmfile modifications needed.

```bash
# 1. Install Kyverno
helm repo add kyverno https://kyverno.github.io/kyverno/
helm repo update
helm install kyverno kyverno/kyverno -n kyverno --create-namespace

# 2. Create pull secret in each namespace
export NGC_API_KEY="<your-key>"
for ns in cassandra-system nats-system nvcf api-keys ess sis nvca-operator vault-system; do
  kubectl create secret docker-registry nvcr-pull-secret \
    --docker-server=nvcr.io \
    --docker-username='$oauthtoken' \
    --docker-password="$NGC_API_KEY" \
    --namespace="$ns" \
    --dry-run=client -o yaml | kubectl apply -f -
done

# 3. Apply the Kyverno ClusterPolicy
kubectl apply -f kyverno-imagepullsecret-policy.yaml
```

The policy mutates every pod at admission time, adding `imagePullSecrets: [{name: nvcr-pull-secret}]`. Verify with:

```bash
kubectl get pod -n <namespace> <pod-name> -o jsonpath='{.spec.imagePullSecrets}'
# Should show: [{"name":"nvcr-pull-secret"}]
```

Not needed if using a CSP built-in credential helper (e.g., ECR with IAM node roles).

For the Kyverno policy YAML and pull secret creation script, see [references/pull-secrets.md](references/pull-secrets.md).

## Fake GPU Operator (Non-GPU Clusters)

When deploying on clusters without real NVIDIA GPUs (load test clusters, dev/staging environments), the NVCA agent will crash-loop with:

```
no backend GPUs were found. Ensure gpu-operator is installed and at least one node has GPU resources (nvidia.com/gpu)
```

Install the RunAI fake-gpu-operator **before** running `helmfile sync` (or after, followed by a cluster re-registration). The fake GPU operator is **not** part of the helmfile stack -- it is a separate helm install.

### Prerequisites

KWOK (Kubernetes Without Kubelet) must be installed first. The fake-gpu-operator relies on KWOK to manage virtual GPU device plugins on nodes.

```bash
kubectl apply -f https://github.com/kubernetes-sigs/kwok/releases/download/v0.7.0/kwok.yaml
kubectl get pods -n kube-system -l app=kwok-controller  # Wait for Running
```

The FlowSchema error during KWOK install (`creation or update of FlowSchema ... is not allowed`) is non-critical and can be ignored.

### Install the fake-gpu-operator

```bash
helm repo add fake-gpu-operator https://runai.jfrog.io/artifactory/api/helm/fake-gpu-operator-charts-prod --force-update

helm upgrade -i gpu-operator fake-gpu-operator/fake-gpu-operator \
  -n gpu-operator --create-namespace \
  --set 'topology.nodePools.default.gpuCount=8' \
  --set 'topology.nodePools.default.gpuProduct=NVIDIA-H100-80GB-HBM3'
```

### Critical: topology.nodePools is a map, not an array

The chart expects `topology.nodePools` as a **map** with named keys (e.g., `default`), not an array. Using `--set 'topology.nodePools[0].gpuCount=8'` will create a YAML array and cause the status-updater to fail with:

```
yaml: unmarshal errors: cannot unmarshal !!seq into map[string]topology.NodePoolTopology
```

### Label existing nodes for fake GPU assignment

The operator watches for nodes with label `run.ai/simulated-gpu-node-pool=<pool-name>` and patches their status with fake GPU extended resources. For existing nodes:

```bash
kubectl label node <node-name> run.ai/simulated-gpu-node-pool=default
```

For clusters with pre-labeled GPU nodes (e.g., `nvidia.com/gpu=true`), label those nodes specifically. You can also add GPU metadata labels to suppress NVCA warnings:

```bash
kubectl label node <node-name> \
  nvidia.com/gpu.family=hopper \
  nvidia.com/gpu.machine=NVIDIA-DGX-H100 \
  nvidia.com/cuda.driver.major=535 \
  --overwrite
```

### Verify fake GPUs

```bash
kubectl get nodes -o custom-columns="NAME:.metadata.name,GPU:.status.allocatable.nvidia\.com/gpu"
# Labeled nodes should show GPU count (e.g., 8)
```

### Post-helmfile-sync: handle nvca-system namespace

The NVCA operator auto-creates `nvca-system` and `nvcf-backend` namespaces. These are **not** in the initial namespace list for pull secrets or Kyverno policy. After the first `helmfile sync`:

1. Create pull secrets in operator-managed namespaces:
   ```bash
   for ns in nvca-system nvcf-backend; do
     kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
     kubectl create secret docker-registry nvcr-pull-secret \
       --docker-server=nvcr.io \
       --docker-username='$oauthtoken' \
       --docker-password="$NGC_API_KEY" \
       --namespace="$ns" \
       --dry-run=client -o yaml | kubectl apply -f -
   done
   ```

2. Update the Kyverno ClusterPolicy to include `nvca-system` and `nvcf-backend` in the namespace match list.

3. Delete any stuck NVCA pods so they are recreated with the injected pull secret.

### Re-register the cluster after adding fake GPUs

If the fake GPU operator was installed **after** the helmfile stack, the cluster bootstrap ran without GPU information. Re-register:

```bash
kubectl exec -n nvca-operator deploy/nvca-operator -c nvca-operator -- \
  /usr/bin/nvca-self-managed bootstrap --system-namespace nvca-operator

kubectl rollout restart deployment nvca-operator -n nvca-operator
kubectl rollout status deployment nvca-operator -n nvca-operator --timeout=120s
```

The operator caches cluster IDs at startup and does not watch the bootstrap ConfigMap for changes, so the restart is required.

### Recommended installation order

For the smoothest experience on non-GPU clusters:

1. Install KWOK
2. Install fake-gpu-operator and label target nodes
3. Verify `nvidia.com/gpu` appears in node allocatable
4. Create namespaces and pull secrets (include `nvca-system` and `nvcf-backend` upfront)
5. Install Kyverno with policy covering all namespaces (include `nvca-system` and `nvcf-backend`)
6. Authenticate helm to chart registry
7. Run `helmfile sync`

This avoids the post-install re-registration step and NVCA crash-loop entirely.

## Debugging

### Quick status check

```bash
kubectl get pods -A -o wide                        # All pods
helm list -A                                        # All helm releases
kubectl get events -n <ns> --sort-by='.lastTimestamp'  # Recent events
```

### Common failure patterns

| Symptom | Cause | Fix |
|---------|-------|-----|
| `ImagePullBackOff` + `401 Unauthorized` | Missing or wrong pull secret | Check secret exists, check SA has imagePullSecrets |
| `Init:0/1` stuck on service pods | Vault-agent waiting for OpenBao | Check OpenBao pods + migration job status |
| `OOMKilled` on Cassandra | Default resources too small | Override `cassandra.resources` via values block |
| `Pending` pods | Node selector mismatch or no storage class | `kubectl describe pod`, check labels and storage |
| Helm release in `failed` state | First install failed partway | `helmfile destroy` the release, then `sync` again |
| Account bootstrap timeout | Wrong base64 credentials in secrets file | Check `kubectl logs job/nvcf-api-account-bootstrap -n nvcf` |
| NVCA agent `CrashLoopBackOff` + "no backend GPUs found" | No GPU operator or fake GPUs on cluster | Install fake-gpu-operator, see [Fake GPU Operator](#fake-gpu-operator-non-gpu-clusters) |
| `ImagePullBackOff` in `nvca-system` | Pull secret missing in operator-created namespace | Create secret + update Kyverno policy to include `nvca-system` |
| Services fail to read vault secrets; `secrets.json` not found | Vault path hardcoded to `/home/app/vault/` in `_helpers.tpl`; the runtime resolves the mounted path relative to the working directory and drops the leading `/` | Override `podAnnotations` and set `JAVA_TOOL_OPTIONS: "-Duser.dir=/"` in release values |
| NATS connection fails at startup; placement tag mismatch | NATS server tags hardcoded to `dc:ncp`; app derives tag from `AWS_REGION` (e.g., `us-gov-west-1`) | Set `AWS_REGION=ncp` and `NVCF_AWS_REGION=ncp` in env config |
| `helmfile sync` finishes but no `nvca-operator` Deployment exists | `nvcaOperator.enabled` is `false` (the default); phase 5 skipped the release | Set `nvcaOperator.enabled: true` in `environments/<env>.yaml` and run `helmfile --selector name=nvca-operator sync`, see [Enable and validate the NVCA operator](#6-enable-and-validate-the-nvca-operator-opt-in) |
| `nvca-operator` Deployment not ready after sync | Chart synced but pod failing (pull secret, GPU discovery, or bootstrap error) | `kubectl describe deploy nvca-operator -n nvca-operator`, check pod logs, verify pull secret in `nvca-operator`, ensure GPUs (real or fake) are visible |

For expanded debugging recipes, see [references/debugging.md](references/debugging.md).

## Additional Resources

- For worked examples, see [examples.md](examples.md)
- For a commented EKS environment file, see [references/eks-example.yaml](references/eks-example.yaml)
- For helmfile structure details, see [references/helmfile-structure.md](references/helmfile-structure.md)
- For per-chart imagePullSecrets keys, see [references/pull-secrets.md](references/pull-secrets.md)
- For debugging recipes, see [references/debugging.md](references/debugging.md)

After deployment, use the `nvcf-self-managed-cli` skill to create functions, manage API keys, and invoke endpoints.
