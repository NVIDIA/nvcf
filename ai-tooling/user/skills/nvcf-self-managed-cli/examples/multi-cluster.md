# Multi-cluster patterns

Examples of split-cluster + many-compute-plane operation.

## One control plane + N compute planes

The canonical multi-cluster topology. Each compute plane is registered separately:

```sh
# 1. Bring up the control plane (one-time, on the control-plane cluster):
KUBECONFIG=cp.yaml nvcf-cli self-hosted install --control-plane | kubectl apply -f -
nvcf-cli self-hosted check --control-plane --wait 5m

# 2. Export the control-plane profile every compute plane registers from.
#    It is written to the stack's out/ directory, and the command prints
#    that path. Copy the file here:
KUBECONFIG=cp.yaml nvcf-cli self-hosted control-plane profile export --cluster-name=ncp-cp
cp <path it printed> control-plane-profile.yaml

# 3. Register + install each compute plane. Both commands name the GPU
#    cluster's context with --kube-context:
for CTX in admin@gpu-east-1 admin@gpu-west-1 admin@gpu-eu-1; do
  NAME=$(echo "$CTX" | cut -d@ -f2)
  nvcf-cli self-hosted compute-plane register \
    --control-plane-profile=control-plane-profile.yaml \
    --cluster-name=$NAME \
    --kube-context=$CTX \
    --icms-url=https://icms.nvcf.example.com \
    --token=$NVCF_ADMIN_JWT \
    --output=$NAME-register-values.yaml
  nvcf-cli self-hosted compute-plane install \
    --values=$NAME-register-values.yaml \
    --kube-context=$CTX \
    --cluster-name=$NAME
done

# 4. Verify all compute planes registered:
nvcf-cli cluster list-registered --nca-id=$NCA_ID --icms-url=$ICMS --json \
  | jq '.clusters[] | {name: .clusterName, status, nvcaVersion}'
```

## Compute-plane-only operator scenario

The operator has kubectl access to ONE compute plane only — the control plane is operated by someone else (e.g. Yotta runs the control plane for a customer).

```sh
# Mint a token via the public api gateway (the control plane's API Keys
# service is reachable via api.nvcf.example.com — no kubectl access needed):
nvcf-cli init --api-url=https://api.nvcf.example.com

# Run pre-flight scoped to the compute plane only. check takes both context
# flags or neither and has no --kubeconfig flag, so give it, through
# KUBECONFIG for this command only, a kubeconfig holding just the compute
# cluster's context. The current context of your kubeconfig stays as it was.
# --compute-plane checks the compute plane as installed, so a docker login
# nvcr.io rejects fails only when NGC_API_KEY does not work in its place:
# treat a registry-credentials row that says the docker login was rejected
# as blocking, since compute-plane install pulls its charts with that login.
# With a control-plane context, check --pre --control-plane checks the new
# compute plane as not yet installed instead; see prompts/add-compute-plane.md.
KC=$(mktemp)
kubectl config view --minify --flatten --context=admin@gpu1 > "$KC"
KUBECONFIG="$KC" nvcf-cli self-hosted check --compute-plane \
  --icms-url=https://icms.nvcf.example.com \
  --json

# Register and install just the compute plane, from the profile the
# control-plane operator exported with `self-hosted control-plane profile export`,
# saved here as control-plane-profile.yaml:
nvcf-cli self-hosted compute-plane register \
  --control-plane-profile=control-plane-profile.yaml \
  --cluster-name=my-compute-1 \
  --kube-context=admin@gpu1 \
  --icms-url=https://icms.nvcf.example.com \
  --token=$ADMIN_JWT \
  --output=my-compute-1-register-values.yaml
nvcf-cli self-hosted compute-plane install \
  --values=my-compute-1-register-values.yaml \
  --kube-context=admin@gpu1 \
  --cluster-name=my-compute-1

# Status of admin@gpu1, from the same kubeconfig (compute-only: the operator
# can't see control-plane component health). Then remove the file, which
# holds the context's credentials:
KUBECONFIG="$KC" nvcf-cli self-hosted status \
  --cluster-name=my-compute-1 \
  --icms-url=https://icms.nvcf.example.com
rm -f "$KC"
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
nvcf-cli cluster list-registered --nca-id=$NCA_ID --icms-url=$ICMS --json \
  | jq -r '.clusters[].clusterName' > clusters.txt

# Status snapshot per compute plane (assuming each context name matches).
# Each run reads a temporary kubeconfig holding only that cluster's context,
# so the loop leaves the current context of your kubeconfig alone:
while read NAME; do
  echo "=== $NAME ==="
  KC=$(mktemp)
  kubectl config view --minify --flatten --context="admin@$NAME" > "$KC" &&
    KUBECONFIG="$KC" nvcf-cli self-hosted status \
      --cluster-name=$NAME \
      --json | jq -c '{cluster:.cluster, verdict:.verdict, reconcile:.reconcileAgeSec}'
  rm -f "$KC"
done < clusters.txt
```

A future M+11 milestone will add native fan-out (`status --all-compute-planes`) so this doesn't need a shell loop.
