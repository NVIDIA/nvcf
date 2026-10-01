# Single-cluster Local Development with Helmfile

Install the NVCF self-hosted control plane and the NVCA operator on a single
local k3d cluster using the documented Helmfile workflow. Useful when you want
to drive the install through the same Make targets used in production.

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
- `nvcf-cli` built from this repo. Steps 7 and 8 pass
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

## Step 1: Bring up the local k3d cluster

```bash
make -C tools/ncp-local-cluster build-and-deploy-cluster
```

<Note>
The single-cluster (`ncp-local`) and multi-cluster
(`ncp-local-cp` + `ncp-local-compute-N`) topologies both claim host
ports 8080/8443/4222 and cannot coexist. The multi-cluster control plane also
claims host ports 9090 and 10081 for worker-facing API gRPC and the stack-owned
grpc-proxy TCP listener. If you already have the
multi-cluster topology running:

```bash
make -C tools/ncp-local-cluster destroy-multicluster
```

</Note>

## Step 2: Author the Helmfile environment file

The single-cluster fixture
`tests/bdd/fixtures/self-managed-local-bdd.yaml` is the canonical
starting point. Its `nvcaOperator.selfManaged.*` URLs use in-cluster DNS
(for example `http://api.sis.svc.cluster.local:8080`), which is correct here
because compute and control plane share the cluster.

```bash
cp tests/bdd/fixtures/self-managed-local-bdd.yaml \
   deploy/stacks/self-managed/environments/local-bdd.yaml
cp tests/bdd/fixtures/nvcf-compute-plane-local-bdd.yaml \
   deploy/stacks/nvcf-compute-plane/environments/local-bdd.yaml
```

Substitute your NGC org and team in for the placeholders:

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

Across the two files, the substitutions update `global.helm.sources.repository`
and `global.image.repository`. The control-plane file also updates
`api.env.NVCF_SIDECARS_LLM_ROUTER_CLIENT_IMAGE`. Set
`global.imagePullSecrets[0].name` if your secret name differs from
`nvcr-pull-secret`. The compute-plane environment provides the NVCA endpoint
settings consumed by its Helmfile.

## Step 3: Author the secrets file

```bash
cp deploy/stacks/self-managed/secrets/secrets.yaml.template \
   deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml

BASE64_CRED=$(printf '%s' "\$oauthtoken:${NGC_API_KEY}" | base64 | tr -d '\n')
sed -i.bak "s|REPLACE_WITH_BASE64_DOCKER_CREDENTIAL|${BASE64_CRED}|g" \
  deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml
rm deploy/stacks/self-managed/secrets/local-bdd-secrets.yaml.bak
```

<Warning>
The secrets file is gitignored. Keep your NGC key out of the working tree.
</Warning>

## Step 4: Pre-create the image pull secret in NVCF namespaces

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

## Step 5: (Optional) Validate the rendered manifests

```bash
make -C deploy/stacks/self-managed template HELMFILE_ENV=local-bdd
```

The command should exit 0 and its output must not contain `Error:`.

## Step 6: Install the control plane

```bash
make -C deploy/stacks/self-managed install HELMFILE_ENV=local-bdd
```

When this succeeds, the following helm releases are deployed:

| Release | Namespace |
|---|---|
| `nats` | `nats-system` |
| `cert-manager` | `cert-manager` |
| `openbao-server` | `vault-system` |
| `cassandra` | `cassandra-system` |
| `api-keys` | `api-keys` |
| `sis` | `sis` |
| `api` | `nvcf` |
| `nvct-api` | `nvcf` |
| `invocation-service` | `nvcf` |
| `grpc-proxy` | `nvcf` |
| `ess-api` | `ess` |
| `notary-service` | `nvcf` |
| `admin-issuer-proxy` | `api-keys` |
| `reval` | `nvcf` |
| `nats-auth-callout-service` | `nats-system` |
| `ingress` | `envoy-gateway-system` |
| `llm-request-router` | `nvcf` |
| `llm-api-gateway` | `nvcf` |

## Step 7: Register the cluster

```bash
make -C deploy/stacks/nvcf-compute-plane register-cluster \
  CLUSTER_NAME=ncp-local \
  NVCF_CLI=$(pwd)/nvcf-cli \
  NVCF_CLI_CONFIG=$(pwd)/tests/bdd/fixtures/nvcf-cli-local.yaml
```

<Note>
`make register-cluster` runs `nvcf-cli init` internally before
`cluster register`, so the Helmfile flow does not need a separate `init`
step (unlike the CLI flow).
</Note>

The target writes the registration handoff file to
`deploy/stacks/nvcf-compute-plane/registration/ncp-local-register-values.yaml`.
The `template`, `install`, and `apply` targets copy that file into
`deploy/stacks/nvcf-compute-plane/out/` before running Helmfile.

## Step 8: Install the NVCA operator

```bash
make -C deploy/stacks/nvcf-compute-plane install \
  CLUSTER_NAME=ncp-local \
  HELMFILE_ENV=local-bdd \
  NVCF_CLI=$(pwd)/nvcf-cli \
  NVCF_CLI_CONFIG=$(pwd)/tests/bdd/fixtures/nvcf-cli-local.yaml
```

## Step 9: Verify

Wait for the NVCA operator to roll out and the backend to become healthy:

```bash
kubectl rollout status deployment/nvca-operator -n nvca-operator --timeout=10m

kubectl wait nvcfbackend ncp-local \
  -n nvca-operator \
  --for=jsonpath='{.status.agentStatus}'=healthy \
  --timeout=10m
```

Confirm the control-plane API is reachable:

```bash
export NVCF_TOKEN=$(curl -s -X POST "http://api-keys.localhost:8080/v1/admin/keys" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['value'])")

curl -s "http://api.localhost:8080/v2/nvcf/functions" \
  -H "Authorization: Bearer ${NVCF_TOKEN}" | python3 -m json.tool
```

## Teardown

Remove the helm releases but keep the cluster (stack-only):

```bash
tests/bdd/scripts/destroy-stack.sh single
```

Or destroy the whole cluster:

```bash
make -C tools/ncp-local-cluster destroy
```
