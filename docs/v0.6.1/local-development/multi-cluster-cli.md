# Multi-cluster Local Development with the CLI

Install a NVCF self-hosted control plane on one local k3d cluster and a
separately registered compute plane on a second cluster, all using
`nvcf-cli`. Useful when you want to exercise the multi-cluster install and
registration paths before targeting real infrastructure.

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

The CLI writes `.localhost` URLs into the control-plane profile and
flows them through to the per-cluster register-values as-is. The NVCA
agent on the compute cluster uses those URLs at runtime to reach cp
services. The docker network shared between the two k3d clusters
(plus the install-time wiring `make build-and-deploy-multicluster`
sets up) is what makes the cross-cluster reach work.

For users coming from the Helmfile install path: that flow is values-driven
and uses service-DNS endpoint values bridged by compute-cluster alias Services
and Endpoints. The CLI path writes a control-plane profile with localhost
URLs and does not depend on those Helmfile environment values.

## Prerequisites

Install the following tools:

- [Docker](https://www.docker.com/get-started) (running)
- [k3d](https://k3d.io/#installation) v5.x or later
- `kubectl`
- `helm` >= 3.12
- An NGC API key from [ngc.nvidia.com](https://ngc.nvidia.com) with
  access to the NVCF chart and image registry.
- The NGC organization and team slugs that hold the chart and image
  repository you have access to. `make build-and-deploy-multicluster`
  reads these from `SAMPLE_NGC_ORG` / `SAMPLE_NGC_TEAM` during its
  credential provider validation step; without them, the build target
  fails and skips its final gateway-API setup.
- `nvcf-cli` built from this repo:

  ```bash
  go build -C src/clis/nvcf-cli -o ../../../nvcf-cli .
  ```

Export the env vars used by the cluster bootstrap and the install steps:

```bash
export NGC_API_KEY="<your-ngc-api-key>"
export SAMPLE_NGC_ORG="<your-ngc-org>"
export SAMPLE_NGC_TEAM="<your-ngc-team>"
```

## Step 1: Bring up the multi-cluster topology

```bash
make -C tools/ncp-local-cluster build-and-deploy-multicluster
```

This creates `ncp-local-cp` plus `ncp-local-compute-1`, installs the fake
GPU operator and CSI SMB driver on the compute cluster, configures
compute-side control-plane service aliases, and validates Envoy Gateway on the
control-plane cluster.

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

<Note>
`build-and-deploy-multicluster` runs `setup-gateway-api`,
`check-gateway-api`, and `validate-gateway` on the control-plane cluster
as its final steps. If any earlier step fails (for example, credential
provider validation when `SAMPLE_NGC_ORG` / `SAMPLE_NGC_TEAM` are not
set), gateway setup is skipped. After fixing the underlying issue,
re-run just the gateway-API setup on the cp cluster:

```bash
make -C tools/ncp-local-cluster setup-gateway-api CLUSTER_NAME=ncp-local-cp
make -C tools/ncp-local-cluster check-gateway-api CLUSTER_NAME=ncp-local-cp
```

</Note>

## Step 2: Author the Helmfile environment files

The CLI passes `--env local-bdd` to Helmfile when it renders each stack.
Create the environment files from the multi-cluster fixtures so Helmfile uses
the NGC organization and team that you can access:

```bash
cp tests/bdd/fixtures/self-managed-local-bdd-multi.yaml \
   deploy/stacks/self-managed/environments/local-bdd.yaml
cp tests/bdd/fixtures/nvcf-compute-plane-local-bdd-multi.yaml \
   deploy/stacks/nvcf-compute-plane/environments/local-bdd.yaml

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

The control-plane profile and registration values produced later in this
workflow supply the compute-reachable endpoint values.

## Step 3: Author the local secrets file

```bash
cp deploy/stacks/self-managed/secrets/secrets.yaml.template \
   deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml

BASE64_CRED=$(printf '%s' "\$oauthtoken:${NGC_API_KEY}" | base64 | tr -d '\n')
sed -i.bak "s|REPLACE_WITH_BASE64_DOCKER_CREDENTIAL|${BASE64_CRED}|g" \
  deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml
rm deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml.bak
```

## Step 4: Create the image pull secrets

`nvcf-cli self-hosted install` renders helmfile manifests that reference
`imagePullSecrets: [{name: nvcr-pull-secret}]`. Create the secret in each
NVCF namespace on the control-plane cluster (`k3d-ncp-local-cp`) before
running install so pods can pull images from `nvcr.io`. Set the kubectl
context to the cp cluster first if you have not already:

```bash
kubectl config use-context k3d-ncp-local-cp

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

The loop is idempotent (uses `kubectl apply`). You must create the same pull
secret on the compute cluster before installing NVCA there. The explicit
`compute-plane install` command runs Helmfile and expects the referenced secret
to already exist.

## Step 5: Install the control plane

The install command needs both contexts so it knows which cluster gets each
plane:

```bash
./nvcf-cli \
  --config tests/bdd/fixtures/nvcf-cli-local.yaml \
  self-hosted \
    --control-plane-stack deploy/stacks/self-managed \
    --compute-plane-stack deploy/stacks/nvcf-compute-plane \
    --env local-bdd \
    --plain \
    --control-plane-context k3d-ncp-local-cp \
    --compute-plane-context k3d-ncp-local-compute-1 \
    --token DUMMY \
  install --control-plane \
    --cluster-name ncp-local-cp \
    --region us-west-1 \
    --nca-id nvcf-default
```

<Note>
`--token DUMMY` skips the install command's `check-cp` auth gate. The
install path itself never consumes the token. See the single-cluster CLI
page for the full explanation.
</Note>

When this completes, a control-plane profile is written to
`deploy/stacks/self-managed/out/control-plane-profile.yaml`. It carries both
URL blocks:

- `controlPlane.endpoints.inCluster.*` - resolves only inside the
  control-plane cluster (for example `http://api.sis.svc.cluster.local:8080`).
- `controlPlane.endpoints.computeReachable.*` - the `.localhost` URLs
  the CLI writes for cluster-external consumers. These flow through
  to the register-values in Step 7 as-is; `compute-plane register`
  does not rewrite them.

`compute-plane register` picks the right block by inspecting
`--kube-context` against the cp context.

## Step 6: Mint the admin JWT

```bash
./nvcf-cli \
  --config tests/bdd/fixtures/nvcf-cli-local.yaml \
  init
```

## Step 7: Register the compute plane

The `--kube-context` flag selects the compute cluster, which causes the CLI
to pick the `computeReachable` URL block from the profile and write those
URLs straight into the register-values file. The NVCA agent on the compute
cluster uses those URLs at runtime to reach cp services.

```bash
./nvcf-cli \
  --config tests/bdd/fixtures/nvcf-cli-local.yaml \
  self-hosted \
    --control-plane-stack deploy/stacks/self-managed \
    --compute-plane-stack deploy/stacks/nvcf-compute-plane \
    --env local-bdd \
    --plain \
  compute-plane register \
    --control-plane-profile deploy/stacks/self-managed/out/control-plane-profile.yaml \
    --cluster-name ncp-local-compute-1 \
    --kube-context k3d-ncp-local-compute-1 \
    --region us-west-1 \
    --output deploy/stacks/nvcf-compute-plane/out/ncp-local-compute-1-register-values.yaml
```

The output file's `selfManaged` block contains the `.localhost`
compute-reachable URLs, not the in-cluster service URLs. For the default local
topology, this is `http://sis.localhost:8080`,
`http://reval.localhost:8080`, and `nats://nats.localhost:4222`.

<Note>
`nvcf-cli cluster register` (run internally during this step) auto-discovers
the target cluster's OIDC issuer and JWKS by running a probe Job in the
cluster identified by `--kube-context`. That identity is what ICMS validates
when the compute agent presents PSAT tokens at runtime. Always set
`--kube-context` to the COMPUTE cluster.
</Note>

## Step 8: Install the compute plane

Create the image pull secret in the compute namespaces first. These commands
target `k3d-ncp-local-compute-1` so the NVCA operator and agent pods can pull
from `nvcr.io` after Helmfile creates them:

```bash
kubectl config use-context k3d-ncp-local-compute-1

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

```bash
./nvcf-cli \
  --config tests/bdd/fixtures/nvcf-cli-local.yaml \
  self-hosted \
    --control-plane-stack deploy/stacks/self-managed \
    --compute-plane-stack deploy/stacks/nvcf-compute-plane \
    --env local-bdd \
    --plain \
  compute-plane install \
    --values deploy/stacks/nvcf-compute-plane/out/ncp-local-compute-1-register-values.yaml \
    --kube-context k3d-ncp-local-compute-1 \
    --cluster-name ncp-local-compute-1
```

## Step 9: Verify

The NVCFBackend resource is created on the compute cluster, not the
control-plane cluster.

```bash
kubectl wait nvcfbackend ncp-local-compute-1 \
  -n nvca-operator \
  --context k3d-ncp-local-compute-1 \
  --for=jsonpath='{.status.agentStatus}'=healthy \
  --timeout=10m
```

Confirm the control-plane API is reachable (from the host, where
`api.localhost` resolves to 127.0.0.1):

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
