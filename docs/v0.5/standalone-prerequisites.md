# Prerequisites and Configuration

This page covers the shared prerequisites and configuration that apply to all phases of the
standalone chart installation.

## Required Tools

The following tools must be installed on your deployment machine:

- **kubectl** — configured for your target Kubernetes cluster
- **helm** >= 3.12

<Warning>
Ensure your kubectl version matches or is within one minor version of your target Kubernetes cluster version.

</Warning>

<Note>
Unlike the Helmfile-based installation, the standalone approach does **not** require Helmfile
or the helm-diff plugin.

</Note>

## Access Requirements

- **kubectl** configured to the Kubernetes cluster you are deploying to

- Personal **NGC API Key** from [ngc.nvidia.com](https://ngc.nvidia.com) authenticated with `nvcf-onprem` organization **only if** you pull artifacts directly from NGC or use NGC as your registry

- **Registry credentials** for your container registry (ECR, NGC, etc.) — see [third-party-registries-self-hosted](./third-party-registries.md) for setup instructions

- **Local Helm authentication** to your container registry where NVCF charts are stored. `helm upgrade --install` pulls OCI charts during deployment, so your local environment must be authenticated:

  - **AWS ECR**: `aws ecr get-login-password --region <region> | helm registry login --username AWS --password-stdin <account-id>.dkr.ecr.<region>.amazonaws.com`
  - **NGC**: `docker login nvcr.io -u '$oauthtoken' -p <NGC_API_KEY>`
  - **Other registries**: Use `docker login` or `helm registry login` as appropriate for your registry

- Artifacts must be available in a registry that your Kubernetes cluster can access. See [self-hosted-artifact-manifest](./manifest.md) and [self-hosted-image-mirroring](./image-mirroring.md).

<Note>
See [terraform-installation](./terraform-installation.md) for instructions on deploying a Kubernetes cluster on EKS or other CSPs if you don't have one already.

</Note>

## Namespace Requirements

Each Helm chart in the NVCF stack must be installed into a specific namespace. These namespace
assignments are **fixed** and must not be changed — service-to-service cluster DNS addressing
and Vault (OpenBao) authentication claims depend on this layout.

| Namespace | Services |
| --- | --- |
| `nats-system` | nats |
| `vault-system` | openbao-server |
| `cassandra-system` | cassandra |
| `nvcf` | api, invocation-service, grpc-proxy, notary-service |
| `api-keys` | api-keys, admin-issuer-proxy |
| `ess` | ess-api |
| `sis` | sis |
| `envoy-gateway-system` | ingress (nvcf-gateway-routes) |
| `nvca-operator` | nvca-operator |

<Warning>
Installing a chart into the wrong namespace will cause authentication failures such as
`error validating claims: claim "/kubernetes.io/namespace" does not match any associated bound claim values`.
If you see this error, verify that every release is deployed in the namespace shown above.

</Warning>

### Create Namespaces

Create all required namespaces up front:

```bash
kubectl create namespace nats-system
kubectl create namespace vault-system
kubectl create namespace cassandra-system
kubectl create namespace nvcf
kubectl create namespace api-keys
kubectl create namespace ess
kubectl create namespace sis
kubectl create namespace envoy-gateway-system
kubectl create namespace envoy-gateway
```

Label the namespaces that require Gateway API routing:

```bash
kubectl label namespace envoy-gateway nvcf/platform=true
kubectl label namespace api-keys nvcf/platform=true
kubectl label namespace sis nvcf/platform=true
kubectl label namespace ess nvcf/platform=true
kubectl label namespace nvcf nvcf/platform=true
```

## Shared Configuration Variables

Throughout the standalone installation, you will reference your registry and storage settings
repeatedly. Define these shell variables once and reuse them in all subsequent commands:

```bash
# Container image registry (where NVCF service images are stored)
export REGISTRY="<your-registry>"          # e.g. nvcr.io or <account-id>.dkr.ecr.<region>.amazonaws.com
export REPOSITORY="<your-repo>"            # e.g. YOUR_ORG/YOUR_TEAM or <ecr-repo-name>

# Storage configuration
export STORAGE_CLASS="<your-storage-class>" # e.g. gp3, local-path
export STORAGE_SIZE="10Gi"                  # Adjust for production (20Gi+ recommended)
```

<Tip>
Add these exports to a `standalone-env.sh` file and `source` it at the start of each
installation session.

</Tip>

## Image Pull Secrets

Kubernetes needs credentials to pull NVCF container images from your private registry.
Whether you need explicit image pull secrets depends on how your cluster authenticates
to the registry:

- **AWS ECR (same account)**: If you used `nvcf-base` Terraform to create your EKS cluster,
  the node IAM role already includes `AmazonEC2ContainerRegistryReadOnly`. Nodes can pull
  images from ECR in the same account without additional pull secrets. **Skip this step.**
- **NGC or other third-party registries**: You must create an image pull secret in every
  namespace that runs NVCF control plane pods.

```bash
NAMESPACES="nats-system vault-system cassandra-system nvcf api-keys ess sis envoy-gateway-system"

for ns in $NAMESPACES; do
  kubectl create secret docker-registry nvcf-pull-secret \
    --namespace "$ns" \
    --docker-server="$REGISTRY" \
    --docker-username="<username>" \
    --docker-password="<password>" \
    --dry-run=client -o yaml | kubectl apply -f -
done
```

<Note>
For NGC, use `$oauthtoken` as the username and your NGC API key as the password.

</Note>

## Secrets Configuration

Several charts require sensitive values. Prepare these before starting the installation.

### Cassandra Password

The default Cassandra superuser password is used by the OpenBao migration job to store
credentials in the vault. Keep this consistent across the OpenBao and Cassandra configurations:

```bash
export CASSANDRA_PASSWORD="ch@ng3m3"  # Change for production
```

### Registry Credential (Base64)

The NVCF API bootstrap job and OpenBao migrations require a base64-encoded **NGC** registry
credential. This credential is stored in OpenBao and used by the NVCF API to pull function
container images and Helm charts at deployment time.

Generate the credential using your NGC API key:

```bash
# Replace YOUR_NGC_API_KEY with your personal NGC API key from ngc.nvidia.com
export NGC_API_KEY="YOUR_NGC_API_KEY"
export REGISTRY_CREDENTIAL_B64=$(echo -n "\$oauthtoken:${NGC_API_KEY}" | base64 -w 0)
```

<Note>
This credential is used for **function deployments** (pulling user function containers and
charts), not for pulling the NVCF control plane images. Even if your control plane images
are mirrored to ECR, NGC credentials are still needed here as the default source for
function artifacts.

Additional registries (ECR, VolcEngine, Harbor, etc.) can be added after installation
using the NVCF CLI or API. See [third-party-registries-self-hosted](./third-party-registries.md) for details.

</Note>

These variables will be referenced in the values files for individual charts in subsequent
installation phases.

## Next Steps

Once you have completed the prerequisites, proceed to [standalone-infrastructure](./standalone-infrastructure.md) to
install the infrastructure dependencies (NATS, OpenBao, Cassandra).
