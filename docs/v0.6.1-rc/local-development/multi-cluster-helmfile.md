# Multi-cluster Local Development with Helmfile

Install the NVCF self-hosted control plane on one local k3d cluster and the
NVCA operator on a separately registered compute cluster, all driven by the
documented Helmfile workflow.

<Info>
This setup is for local development only. It uses fake GPUs, a single
Cassandra replica, and ephemeral storage. Do not use this for production
workloads.
</Info>

Clone the public repository and run the remaining commands from its root:

```bash
git clone https://github.com/nvidia/nvcf.git
cd nvcf
```

## Topology

| k3d cluster | Role | kubectl context |
|---|---|---|
| `ncp-local-cp` | Control plane | `k3d-ncp-local-cp` |
| `ncp-local-compute-1` | Compute plane (first worker) | `k3d-ncp-local-compute-1` |

Cross-cluster traffic from the compute cluster uses the same service-DNS
hostnames as the local stack values. During topology bootstrap,
`tools/ncp-local-cluster/scripts/configure-control-plane-endpoints.sh`
creates compute-cluster alias Services and Endpoints for those names, and the
Endpoints point at the control-plane k3d load balancer:

- `http://api.sis.svc.cluster.local:8080`
- `http://reval.nvcf.svc.cluster.local:8080`
- `nats://nats.nats-system.svc.cluster.local:4222`
- `http://ess-api.ess.svc.cluster.local:8080`
- `http://invocation.nvcf.svc.cluster.local:8080`
- `api.nvcf.svc.cluster.local:9090`

## Prerequisites

Install the following tools:

- [Docker](https://www.docker.com/get-started) (running)
- [k3d](https://k3d.io/#installation) v5.x or later
- `kubectl`
- `helm` >= 3.12
- `helmfile` >= 1.1.0, < 1.2.0
- `helm-diff` plugin: `helm plugin install https://github.com/databus23/helm-diff`
- An NGC API key from [ngc.nvidia.com](https://ngc.nvidia.com) with
  access to the NVCF chart and image registry.
- The NGC organization and team slugs that hold the chart/image repository
  you have access to.
- `nvcf-cli` built from this repo. Steps 9 and 10 pass
  `NVCF_CLI=$(pwd)/nvcf-cli` to the make targets, so the binary must
  exist on disk before those steps run:

  ```bash
  go build -C src/clis/nvcf-cli -o ../../../nvcf-cli .
  ```

Export the env vars used below:

```bash
export NGC_API_KEY="<your-ngc-api-key>"
export SAMPLE_NGC_ORG="<your-ngc-org>"
export SAMPLE_NGC_TEAM="<your-ngc-team>"
```

## Step 1: Bring up the multi-cluster topology

```bash
make -C tools/ncp-local-cluster build-and-deploy-multicluster
```

<Note>
The single-cluster (`ncp-local`) and multi-cluster
(`ncp-local-cp` + `ncp-local-compute-N`) topologies both claim host
ports 8080/8443/4222 and cannot coexist. The multi-cluster control plane also
claims host ports 9090 and 10081 for worker-facing API gRPC and the stack-owned
grpc-proxy TCP listener. If you already have the
single-cluster topology running:

```bash
make -C tools/ncp-local-cluster destroy CLUSTER_NAME=ncp-local
```

</Note>

## Step 2: Author the multi-cluster Helmfile environment files

The values-driven Helmfile path has no control-plane profile; the operator
must author topology-correct URLs in the environment files. Use the
multi-cluster fixtures for this flow:

```bash
cp tests/bdd/fixtures/self-managed-local-bdd-multi.yaml \
   deploy/stacks/self-managed/environments/local-bdd.yaml
cp tests/bdd/fixtures/nvcf-compute-plane-local-bdd-multi.yaml \
   deploy/stacks/nvcf-compute-plane/environments/local-bdd.yaml
```

Substitute your NGC org and team:

```bash
for file in \
  deploy/stacks/self-managed/environments/local-bdd.yaml \
  deploy/stacks/nvcf-compute-plane/environments/local-bdd.yaml; do
  sed -i.bak \
    -e "s|REPLACE_WITH_SAMPLE_NGC_ORG|${SAMPLE_NGC_ORG}|g" \
    -e "s|REPLACE_WITH_SAMPLE_NGC_TEAM|${SAMPLE_NGC_TEAM}|g" \
    "$file"
  rm "${file}.bak"
done
```

<Note>
The multi-cluster fixture intentionally uses service-DNS hostnames such as
`api.sis.svc.cluster.local` and `invocation.nvcf.svc.cluster.local`. In this
split topology those names resolve on the compute cluster because Step 1
created alias Services and Endpoints that forward to the control-plane load
balancer. Do not replace these values with topology-specific `.test`
hostnames.
</Note>

## Step 3: Author the secrets file

```bash
cp deploy/stacks/self-managed/secrets/secrets.yaml.template \
   deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml

BASE64_CRED=$(printf '%s' "\$oauthtoken:${NGC_API_KEY}" | base64 | tr -d '\n')
sed -i.bak "s|REPLACE_WITH_BASE64_DOCKER_CREDENTIAL|${BASE64_CRED}|g" \
  deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml
rm deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml.bak
```

## Step 4: Set kubectl context to the control-plane cluster

Helmfile install runs against the ambient kubectl context. Switch to the
control-plane cluster so the install lands there:

```bash
kubectl config use-context k3d-ncp-local-cp
```

## Step 5: Pre-create the image pull secret in NVCF namespaces (cp cluster)

```bash
for ns in cassandra-system nats-system nvcf api-keys ess sis \
          vault-system nvca-operator nvca-system nvcf-backend cert-manager; do
  kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
  kubectl create secret docker-registry nvcr-pull-secret \
    --docker-server=nvcr.io \
    --docker-username='$oauthtoken' \
    --docker-password="${NGC_API_KEY}" \
    --namespace="$ns" \
    --dry-run=client -o yaml | kubectl apply -f -
done
```

## Step 6: Install the control plane

```bash
make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd
```

The 18 standard helm releases land on `k3d-ncp-local-cp` (see the
single-cluster Helmfile page for the full release list).

## Step 7: Switch kubectl context to the compute cluster (CRITICAL)

```bash
kubectl config use-context k3d-ncp-local-compute-1
```

<Warning>
This single context switch is the most error-prone step in the multi-cluster
flow. The next step's `nvcf-cli cluster register` (run internally by
`make register-cluster`) auto-discovers the target cluster's OIDC issuer
and JWKS by running a probe Job in the CURRENT kubectl context. If you skip
the switch, the control-plane cluster's JWKS gets registered as the compute
cluster's identity, and the compute agent's PSAT tokens 401 against ICMS at
runtime.
</Warning>

## Step 8: Pre-create the image pull secret on the compute cluster

```bash
for ns in nvca-operator nvca-system nvcf-backend; do
  kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
  kubectl create secret docker-registry nvcr-pull-secret \
    --docker-server=nvcr.io \
    --docker-username='$oauthtoken' \
    --docker-password="${NGC_API_KEY}" \
    --namespace="$ns" \
    --dry-run=client -o yaml | kubectl apply -f -
done
```

## Step 9: Register the compute cluster

```bash
make -C deploy/stacks/nvcf-compute-plane register-cluster \
  CLUSTER_NAME=ncp-local-compute-1 \
  NVCF_CLI=$(pwd)/nvcf-cli \
  NVCF_CLI_CONFIG=$(pwd)/tests/bdd/fixtures/nvcf-cli-local.yaml
```

<Note>
`make register-cluster` runs `nvcf-cli init` internally before
`cluster register`, so this flow does not need a separate `init` step.
</Note>

The target writes the registration handoff file to
`deploy/stacks/nvcf-compute-plane/registration/ncp-local-compute-1-register-values.yaml`.
The `template`, `install`, and `apply` targets copy that file into
`deploy/stacks/nvcf-compute-plane/out/` before running Helmfile.

## Step 10: Install the NVCA operator on the compute cluster

```bash
make -C deploy/stacks/nvcf-compute-plane install \
  CLUSTER_NAME=ncp-local-compute-1 \
  HELMFILE_ENV=local-bdd \
  NVCF_CLI=$(pwd)/nvcf-cli \
  NVCF_CLI_CONFIG=$(pwd)/tests/bdd/fixtures/nvcf-cli-local.yaml
```

This uses the compute-plane `local-bdd.yaml` file created in Step 2.

## Step 11: Verify

The NVCFBackend resource is created on the compute cluster, not the
control-plane cluster. Use the compute cluster context for all verification:

```bash
kubectl rollout status deployment/nvca-operator \
  -n nvca-operator \
  --context k3d-ncp-local-compute-1 \
  --timeout=10m

kubectl wait nvcfbackend ncp-local-compute-1 \
  -n nvca-operator \
  --context k3d-ncp-local-compute-1 \
  --for=jsonpath='{.status.agentStatus}'=healthy \
  --timeout=10m
```

Confirm the control-plane API is reachable (from the host):

```bash
export NVCF_TOKEN=$(curl -s -X POST "http://api-keys.localhost:8080/v1/admin/keys" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['value'])")

curl -s "http://api.localhost:8080/v2/nvcf/functions" \
  -H "Authorization: Bearer ${NVCF_TOKEN}" | python3 -m json.tool
```

## Teardown

Remove the helm releases on both clusters but keep the topology (stack-only):

```bash
tests/bdd/scripts/destroy-stack.sh multi
```

Or destroy the whole topology:

```bash
make -C tools/ncp-local-cluster destroy-multicluster
```
