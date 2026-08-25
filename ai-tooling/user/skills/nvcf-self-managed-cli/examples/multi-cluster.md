# Multi-cluster patterns

Examples of split-cluster + many-compute-plane operation.

## One control plane + N compute planes

The canonical multi-cluster topology. Each compute plane is registered separately:

```sh
# 1. Bring up the control plane (one-time, on the control-plane cluster):
KUBECONFIG=cp.yaml nvcf-cli self-hosted install --control-plane | kubectl apply -f -
nvcf-cli self-hosted check --control-plane --wait 5m

# 2. Register + install each compute plane:
for CTX in admin@gpu-east-1 admin@gpu-west-1 admin@gpu-eu-1; do
  NAME=$(echo "$CTX" | cut -d@ -f2)
  nvcf-cli self-hosted up \
    --cluster-name=$NAME \
    --compute-plane-context=$CTX \
    --icms-url=https://icms.nvcf.example.com \
    --token=$NVCF_ADMIN_JWT \
    --non-interactive \
    --json
done

# 3. Verify all compute planes registered:
nvcf-cli cluster registration list --nca-id=$NCA_ID --icms-url=$ICMS --json \
  | jq '.clusters[] | {name: .clusterName, status, nvcaVersion}'
```

## Compute-plane-only operator scenario

The operator has kubectl access to ONE compute plane only — the control plane is operated by someone else (e.g. Yotta runs the control plane for a customer).

```sh
# Mint a token via the public api gateway (the control plane's API Keys
# service is reachable via api.nvcf.example.com — no kubectl access needed):
nvcf-cli init --api-url=https://api.nvcf.example.com

# Run pre-flight scoped to the compute plane only:
nvcf-cli self-hosted check --pre \
  --compute-plane-context=admin@gpu1 \
  --icms-url=https://icms.nvcf.example.com \
  --json

# Bring up just the compute plane:
nvcf-cli self-hosted up \
  --cluster-name=my-compute-1 \
  --compute-plane-context=admin@gpu1 \
  --icms-url=https://icms.nvcf.example.com \
  --token=$ADMIN_JWT \
  --non-interactive

# Status (compute-only — operator can't see control-plane component health):
nvcf-cli self-hosted status \
  --compute-plane-context=admin@gpu1 \
  --icms-url=https://icms.nvcf.example.com
```

## Functions targeting specific compute planes

When you have multiple compute planes and want a function to deploy to one:

```json
{
  "functionId": "<fn_id>",
  "versionId": "<ver_id>",
  "deploymentSpecifications": [{
    "gpu": "H100",
    "instanceType": "NCP.GPU.H100_1x",
    "minInstances": 1,
    "maxInstances": 1,
    "clusters": ["gpu-east-1"]
  }]
}
```

The `clusters` array (using cluster names, not IDs) limits scheduling to those compute planes. Omit to let ICMS choose any compute plane with a matching SKU.

## Status fan-out across compute planes

```sh
# List all registered compute planes:
nvcf-cli cluster registration list --nca-id=$NCA_ID --icms-url=$ICMS --json \
  | jq -r '.clusters[].clusterName' > clusters.txt

# Status snapshot per compute plane (assuming each context name matches):
while read NAME; do
  echo "=== $NAME ==="
  nvcf-cli self-hosted status \
    --cluster-name=$NAME \
    --compute-plane-context=admin@$NAME \
    --json | jq -c '{cluster:.cluster, verdict:.verdict, reconcile:.reconcileAgeSec}'
done < clusters.txt
```

A future M+11 milestone will add native fan-out (`status --all-compute-planes`) so this doesn't need a shell loop.
