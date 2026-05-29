# Single-cluster Local Development with the CLI

Install a complete NVCF self-hosted control plane and compute plane on a
single local k3d cluster using `nvcf-cli`. Useful for validating the install
and registration workflow before targeting real infrastructure.

<Info>
This setup is for **local development only**. It uses fake GPUs, a single
Cassandra replica, and ephemeral storage. Do not use this for production
workloads.
</Info>

## Prerequisites

Install the following tools:

- [Docker](https://www.docker.com/get-started) (running)
- [k3d](https://k3d.io/#installation) v5.x or later
- `kubectl`
- `helm` >= 3.12
- An **NGC API key** from [ngc.nvidia.com](https://ngc.nvidia.com) with
  access to the NVCF chart and image registry.
- The NGC organization and team slugs that hold the chart and image
  repository you have access to. `make build-and-deploy-cluster` reads
  these from `SAMPLE_NGC_ORG` / `SAMPLE_NGC_TEAM` during its credential
  provider validation step; without them, the build target fails and
  skips its final gateway-API setup.
- `nvcf-cli` built from this repo:

  ```bash
  go build -o nvcf-cli ./src/clis/nvcf-cli
  ```

Export the env vars used by the cluster bootstrap and the install steps:

```bash
export NGC_API_KEY="<your-ngc-api-key>"
export SAMPLE_NGC_ORG="<your-ngc-org>"
export SAMPLE_NGC_TEAM="<your-ngc-team>"
```

## Step 1: Bring up the local k3d cluster

The canonical single-cluster topology lives in `tools/ncp-local-cluster/`.

```bash
make -C tools/ncp-local-cluster build-and-deploy-cluster
```

This creates a k3d cluster named `ncp-local`, installs the fake GPU operator,
the CSI SMB driver, Envoy Gateway, and validates the bootstrap end-to-end.

<Note>
The single-cluster (`ncp-local`) and multi-cluster
(`ncp-local-cp` + `ncp-local-compute-N`) topologies both claim host
ports 8080/8443/4222 and cannot coexist. If you already have the
multi-cluster topology running, destroy it first:

```bash
make -C tools/ncp-local-cluster destroy-multicluster
```

</Note>

<Note>
`build-and-deploy-cluster` runs `setup-gateway-api`, `check-gateway-api`,
and `validate-gateway` as its final steps. If any earlier step fails (for
example, credential provider validation when `SAMPLE_NGC_ORG` /
`SAMPLE_NGC_TEAM` are not set), gateway setup is skipped. After fixing
the underlying issue, re-run just the gateway-API setup:

```bash
make -C tools/ncp-local-cluster setup-gateway-api
make -C tools/ncp-local-cluster check-gateway-api
```

</Note>

## Step 2: Author the local secrets file

`nvcf-cli self-hosted install --env local` reads NGC credentials from
`deploy/stacks/self-managed/secrets/local-secrets.yaml`. Author it from the
canonical template:

```bash
cp deploy/stacks/self-managed/secrets/secrets.yaml.template \
   deploy/stacks/self-managed/secrets/local-secrets.yaml
```

Generate the base64 NGC dockerconfig credential and substitute it into the
file:

```bash
BASE64_CRED=$(echo -n "\$oauthtoken:${NGC_API_KEY}" | base64 -w0)
sed -i.bak "s|REPLACE_WITH_BASE64_DOCKER_CREDENTIAL|${BASE64_CRED}|g" \
  deploy/stacks/self-managed/secrets/local-secrets.yaml
rm deploy/stacks/self-managed/secrets/local-secrets.yaml.bak
```

<Warning>
`local-secrets.yaml` is gitignored. Keep your NGC key out of the working tree.
</Warning>

## Step 3: Create the image pull secrets

`nvcf-cli self-hosted install` renders helmfile manifests that reference
`imagePullSecrets: [{name: nvcr-pull-secret}]`. Create the secret in each
NVCF namespace before running install so pods can pull images from `nvcr.io`.
The loop is idempotent (uses `kubectl apply`):

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

## Step 4: Install the control plane

```bash
nvcf-cli \
  --config tests/bdd/fixtures/nvcf-cli-local.yaml \
  self-hosted \
    --stack deploy/stacks/self-managed \
    --env local \
    --plain \
    --token DUMMY \
  install --control-plane \
    --cluster-name ncp-local \
    --region us-west-1 \
    --nca-id nvcf-default
```

<Note>
`--token DUMMY` is a gate-bypass, not a real credential. The install command's
`check-cp` phase normally requires a JWT, but the api-keys service that mints
that JWT does not exist yet on the first invocation. Pass `--token DUMMY` to
skip the gate; the install path itself never reads the token.
</Note>

When this completes, a control-plane profile is written to
`deploy/stacks/self-managed/out/control-plane-profile.yaml`.

## Step 5: Mint the admin JWT

Now that the api-keys service is reachable, `nvcf-cli init` can mint a real
admin JWT:

```bash
nvcf-cli \
  --config tests/bdd/fixtures/nvcf-cli-local.yaml \
  init
```

The token is written to `~/.nvcf-cli.nvcf-cli-local.state`. Subsequent commands
read it from there; the token never appears in argv or per-command logs.

## Step 6: Register the compute plane

In single-cluster topology, compute and control plane share the same k3d
cluster (`ncp-local`).

```bash
nvcf-cli \
  --config tests/bdd/fixtures/nvcf-cli-local.yaml \
  self-hosted \
    --stack deploy/stacks/self-managed \
    --env local \
    --plain \
  compute-plane register \
    --control-plane-profile deploy/stacks/self-managed/out/control-plane-profile.yaml \
    --cluster-name ncp-local \
    --kube-context k3d-ncp-local \
    --region us-west-1 \
    --output deploy/stacks/self-managed/out/ncp-local-register-values.yaml
```

This emits `out/ncp-local-register-values.yaml`. Because compute and control
plane share a cluster, the in-cluster service URLs (for example
`http://api.sis.svc.cluster.local:8080`) are directly reachable and are
selected automatically.

## Step 7: Install the compute plane

```bash
nvcf-cli \
  --config tests/bdd/fixtures/nvcf-cli-local.yaml \
  self-hosted \
    --stack deploy/stacks/self-managed \
    --env local \
    --plain \
  compute-plane install \
    --values deploy/stacks/self-managed/out/ncp-local-register-values.yaml \
    --kube-context k3d-ncp-local \
    --cluster-name ncp-local
```

## Step 8: Verify

Wait for the NVCA backend to become healthy:

```bash
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
# Expected: {"functions": []}
```

## Optional: Validate the profile

The control-plane profile can be re-validated against the live cluster:

```bash
nvcf-cli \
  --config tests/bdd/fixtures/nvcf-cli-local.yaml \
  self-hosted \
    --stack deploy/stacks/self-managed \
    --env local \
    --plain \
  control-plane profile validate \
    --file deploy/stacks/self-managed/out/control-plane-profile.yaml \
    --require in-cluster
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
