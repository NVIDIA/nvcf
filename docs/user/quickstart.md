# Quickstart: One-Click Installation

Use the one-click CLI flow for a fresh self-hosted NVCF install. The command installs the control plane, registers a GPU cluster, installs the NVIDIA Cluster Agent, and performs basic health checks.

Use this path when you want the fastest route to a working deployment. Use the [Helmfile installation](./helmfile-installation.md) path when you need manual release control, partial recovery, or upgrade operations.

## What the CLI installs

`nvcf-cli self-hosted up` runs the self-managed stack in ordered phases:

1. Checks local tools and Kubernetes access.
2. Resolves the self-managed stack bundle.
3. Installs the control plane.
4. Initializes CLI authentication and discovers the control-plane issuer.
5. Registers the GPU cluster, recording its OIDC issuer and JWKS with the control plane.
6. Installs the compute-plane components, including NVCA.
7. Prints a final health summary.

The control plane and GPU cluster can be the same Kubernetes cluster or separate clusters. For a single cluster, use your current kubeconfig context. For separate clusters, pass both kube contexts explicitly.

## Prerequisites

Before you run the quickstart, prepare:

- `nvcf-cli`
- `kubectl`
- `helm`
- `helmfile` 1.1.x
- `helm-diff` plugin
- Access to the NVCF Helm charts and container images
- A Kubernetes cluster for the control plane
- A GPU cluster with the NVIDIA GPU Operator, or a local k3d cluster with the fake GPU operator
- A default `StorageClass`
- For remote clusters, Gateway API ingress prepared before install
- For remote clusters, a CLI config that points to the Gateway load balancer

For artifact mirroring, refer to [Image Mirroring](./image-mirroring.md). For local k3d setup, refer to [Local Development](./local-development.md).

## Choose a cluster layout

| Layout | Use when | Required flags |
| --- | --- | --- |
| Single cluster | The control plane and GPU workers run in the same Kubernetes cluster, including local k3d. | Omit context flags, or make the current context the target cluster. |
| Separate GPU cluster | The control plane runs in one Kubernetes cluster and GPU workloads run in another. | Set both `--control-plane-context` and `--compute-plane-context`. |

The two context flags must be set together. Do not set them to the same value. For a single cluster, omit both flags.

## Prepare remote Gateway and CLI config

Skip this section for local k3d. For remote clusters such as Amazon EKS,
complete [Gateway quickstart](./gateway-routing.md#gateway-quickstart) and
[Configure the CLI for one-click](./gateway-routing.md#configure-the-cli-for-one-click)
before running `self-hosted up`.

Run the install commands from the directory that contains `.nvcf-cli.yaml`, or
pass `--config .nvcf-cli.yaml` to each CLI command.

## Run a fresh install

Check prerequisites first:

```bash
nvcf-cli self-hosted check --pre
```

Run the one-click install:

```bash
nvcf-cli self-hosted up \
  --cluster-name "${CLUSTER_NAME}" \
  --nca-id "${NCA_ID}" \
  --region "${REGION}"
```

For separate control-plane and GPU clusters:

```bash
export CONTROL_PLANE_CONTEXT="admin@control-plane"
export COMPUTE_PLANE_CONTEXT="admin@gpu-cluster"
export ICMS_URL="https://sis.example.com"

nvcf-cli self-hosted up \
  --control-plane-context "${CONTROL_PLANE_CONTEXT}" \
  --compute-plane-context "${COMPUTE_PLANE_CONTEXT}" \
  --cluster-name "${CLUSTER_NAME}" \
  --nca-id "${NCA_ID}" \
  --region "${REGION}" \
  --icms-url "${ICMS_URL}"
```

Use `--stack=/path/to/nvcf-self-managed-stack` when testing from a local source-built CLI or when you need to point at a specific stack checkout. Packaged CLI releases can use the packaged stack source unless your release notes say otherwise.

## Local k3d quickstart

After you create the k3d cluster, install the fake GPU operator, and install the CSI SMB driver as described in [Local Development](./local-development.md), use the local route hostnames:

```text
127.0.0.1 api.localhost
127.0.0.1 api-keys.localhost
127.0.0.1 sis.localhost
127.0.0.1 invocation.localhost
```

Set the local endpoint environment:

```bash
export CLUSTER_NAME=ncp-local
export NCA_ID=nvcf-default
export REGION=us-west-1
export SIS_URL=http://sis.localhost:8080
export NVCF_BASE_HTTP_URL=http://api.localhost:8080
export NVCF_INVOKE_URL=http://invocation.localhost:8080
export API_KEYS_SERVICE_URL=http://api-keys.localhost:8080
export API_KEYS_ADMIN_SERVICE_URL=http://api-keys.localhost:8080
export API_KEYS_HOST=api-keys.localhost
export API_HOST=api.localhost
export INVOKE_HOST=invocation.localhost
export NVCF_CLIENT_ID="${NCA_ID}"
export NVCF_ICMS_URL="${SIS_URL}"
```

For direct pulls from private NGC registries, export an API key with access to
the NVCF container images before running `self-hosted up`. The local stack
references `nvcr-pull-secret`, and the CLI creates or updates that secret in
the local NVCF namespaces when `NGC_API_KEY` is set.

```bash
export NGC_API_KEY="<ngc-api-key-with-container-pull-access>"
helm registry login nvcr.io -u '$oauthtoken' -p "${NGC_API_KEY}"
```

The account bootstrap registry credentials in
`deploy/stacks/self-managed/secrets/local-secrets.yaml` must be base64-encoded
`username:password` values. For NGC, encode `$oauthtoken:<api-key>`. Use a
separate Helm-capable key for `helm.ngc.nvidia.com` if your container-pull key
does not have Helm chart access.

Then run:

```bash
nvcf-cli self-hosted up \
  --env=local \
  --cluster-name="${CLUSTER_NAME}" \
  --nca-id="${NCA_ID}" \
  --region="${REGION}" \
  --icms-url="${SIS_URL}" \
  --refresh-token \
  --plain
```

If you are testing a local stack checkout, add:

```bash
--stack=/path/to/nvcf-self-managed-stack
```

## Verify the install

Run the CLI checks:

```bash
nvcf-cli self-hosted check --all \
  --cluster-name="${CLUSTER_NAME}" \
  --icms-url="${SIS_URL}"
nvcf-cli self-hosted status \
  --cluster-name="${CLUSTER_NAME}" \
  --icms-url="${SIS_URL}"
```

Confirm Kubernetes resources:

```bash
kubectl get pods -A
kubectl get httproute -A
kubectl get nvcfbackends -A
```

Then create, deploy, and invoke a function using the [CLI](./cli.md). For local fake GPU clusters, choose a GPU and instance type that match the discovered node labels and GPU count. For the validated k3d H100 fake GPU setup, use:

```text
gpu: H100
instanceType: NCP.GPU.H100_8x
```

## Clean up

To remove the compute-plane components:

```bash
nvcf-cli self-hosted uninstall \
  --compute-plane \
  --cluster-name "${CLUSTER_NAME}"
```

To remove the control plane:

```bash
nvcf-cli self-hosted uninstall --control-plane
```

For manual Helmfile recovery and teardown, refer to [Helmfile Installation](./helmfile-installation.md).

## Troubleshooting

If the quickstart fails, start with [Troubleshooting](./troubleshooting.md).

Common local k3d issues:

- `sis.localhost` must resolve from the machine running `nvcf-cli`.
- `self-hosted check --all` is a health check, not a replacement for function deploy and invoke validation.
- Local source-built CLI runs can need `--stack` until the packaged default stack source is available.
