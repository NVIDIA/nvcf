# Helmfile Installation

This section covers manual Helmfile installation of the NVCF control plane for
self-hosted NVCF deployments. Registering GPU clusters is a separate step
covered by the Compute Plane Stack documentation.

For a fresh install, start with the [Quickstart](/nvcf/overview/quickstart). Use this Helmfile guide when you need explicit release control, partial recovery, upgrades, or direct access to Helmfile values.

<Info>
This guide assumes you have already downloaded and extracted the
`nvcf-self-managed-stack` Helmfile bundle (see
[download-nvcf-self-managed-stack](/nvcf/overview/image-mirroring)). Control-plane
commands run from inside that directory unless otherwise noted. The directory
contains the control-plane Helmfile definitions, environment templates, and
sample configurations referenced throughout.

```bash
cd path/to/nvcf-self-managed-stack
ls
# Expected contents: helmfile.d/  environments/  secrets/  global.yaml.gotmpl  ...
```

</Info>

## Namespace Requirements

Each control-plane Helm chart must be installed into a specific namespace. The
control-plane namespace assignments are fixed because service-to-service DNS
addressing and Vault (OpenBao) authentication claims depend on them. The
observability stack uses `monitoring` by default, but its namespace is
configurable.

| Namespace | Services |
| --- | --- |
| `nvcf` | api, invocation-service, grpc-proxy, notary-service, reval, state-metrics, function-autoscaler |
| `api-keys` | api-keys, admin-issuer-proxy |
| `ess` | ess-api |
| `sis` | sis |
| `vault-system` | openbao-server |
| `cassandra-system` | cassandra |
| `nats-system` | nats |
| `cert-manager` | cert-manager |
| `monitoring` (default) | OpenTelemetry Operator, collector, default monitors, VictoriaMetrics |
| `envoy-gateway-system` | ingress (nvcf-gateway-routes) |

<Warning>
Installing a chart into the wrong namespace will cause authentication failures such as
`error validating claims: claim "/kubernetes.io/namespace" does not match any associated bound claim values`.
If you see this error, verify that each control-plane release uses the required
namespace and each observability release uses its configured namespace.

</Warning>

## Prerequisites

### Required Tools and Software

The following tools must be installed on your deployment machine:

- `kubectl`
- `helm` >= 3.12
- `helmfile` >= 1.1.0 (recommended: `1.1.x`)
- `helm-diff` plugin >=3.11

<Warning>
Avoid Helmfile 1.2.x. Helmfile 1.2.0 removed sequential execution mode, which
the NVCF stack requires for ordered deployments. Use version `1.1.x` for
compatibility with the commands in this guide.

Helmfile `1.3.0+` re-introduced sequential execution via the `--sequential-helmfiles` flag, but the command syntax differs from the `1.1.x` examples shown here. If you choose to use `1.3.0+`, add `--sequential-helmfiles` to every `helmfile apply` and `helmfile sync` command.

</Warning>

- A kubernetes cluster (CSP agnostic or on-prem).
- Gateway API ingress prepared as described in [Gateway quickstart](./gateway-routing.md#gateway-quickstart) if you are exposing NVCF through Gateway API
- Artifacts must be available in a registry that your Kubernetes cluster can access. This can be the `nvcf-onprem` registry for NVCF control plane service artifacts, but function containers and helm charts must be configured to a user-managed registry. See [self-hosted-artifact-manifest](/nvcf/overview/manifest) and [self-hosted-image-mirroring](/nvcf/overview/image-mirroring).
- The `nvcf-self-managed-stack` repository must be downloaded to your local machine (see [download-nvcf-self-managed-stack](/nvcf/overview/image-mirroring)).

<Accordion title="Install helm-diff plugin">

```bash
# Install helm-diff plugin (required for helmfile)
helm plugin install https://github.com/databus23/helm-diff
```

</Accordion>

<Warning>
kubectl version must match your cluster within one minor version. Using a
kubectl version that is more than one minor version ahead of your Kubernetes
cluster will cause `kubectl apply` and `kubectl patch` commands to fail, not
just warn, due to stricter server-side field validation in newer clients.

This is especially common on macOS with Homebrew, where `brew install kubectl`
or `brew upgrade` can silently install a version much newer than your cluster.
Verify before proceeding:

```bash
kubectl version
# Ensure the Client Version and Server Version are within one minor version of each other.
# Example: Client v1.32.x against Server v1.31.x is OK.
#          Client v1.32.x against Server v1.29.x will cause failures.
```

If your client is too new, install a matching version directly from the [Kubernetes release page](https://kubernetes.io/docs/tasks/tools/).

</Warning>

### Access Requirements

- `kubectl` configured to the kubernetes cluster you are deploying to

- Personal NGC API Key from [ngc.nvidia.com](https://ngc.nvidia.com)
  authenticated with `nvcf-onprem` organization only if you pull artifacts
  directly from NGC or use NGC as your registry

- Registry credentials for your container registry (ECR, NGC, etc.). See
  [Registries](./registries.md) for setup
  instructions

- Local Helm/Docker authentication to your container registry where NVCF charts
  are stored. Helmfile pulls OCI charts during deployment, so your local
  environment must be authenticated. Examples:

  - AWS ECR: `aws ecr get-login-password --region <region> | helm registry login --username AWS --password-stdin <account-id>.dkr.ecr.<region>.amazonaws.com`
  - NGC: `docker login nvcr.io -u '$oauthtoken' -p <NGC_API_KEY>`
  - Other registries: Use `docker login` or `helm registry login` as appropriate for your registry

<Note>
If you are using NGC as your registry, you will use your NGC API key when generating the base64 registry credential in Step 3. Exporting `NGC_API_KEY` is optional and only needed if you prefer to reuse it in commands.

</Note>

## Installation Steps

The installation flow is as follows.

1. Prepare Gateway API ingress
2. Configure your environment file (`environments/<environment-name>.yaml`)
3. Configure your secrets file (`secrets/<environment-name>-secrets.yaml`)
4. Configure image pull secrets (skip if using a CSP registry with built-in credential helpers)
5. Deploy the NVCF control plane components
6. Verify the control plane

Then register each GPU cluster with the control plane. See
[Register a GPU Cluster](/nvcf/compute-plane/register-gpu-cluster).

### Step 1. Prepare Gateway API ingress

Complete [Gateway quickstart](./gateway-routing.md#gateway-quickstart) before you
configure and apply the Helmfile stack.

Keep these values from the Gateway quickstart:

```bash
echo "$GATEWAY_ADDR"
echo "$HTTP_GATEWAY_NAMESPACE/$HTTP_GATEWAY_NAME"
echo "$GRPC_GATEWAY_NAMESPACE/$GRPC_GATEWAY_NAME"
```

Use `GATEWAY_ADDR` as `global.domain` in your environment file. Use the Gateway
names, namespaces, and listener names from Gateway quickstart in
`ingress.gatewayApi.gateways`.

Split or multi-cluster gRPC invocation is not enabled by default. If you need
workers in a compute cluster to reach grpc-proxy in the control-plane cluster,
complete [gRPC Invocation Enablement](./grpc-invocation-enablement.md) before
you deploy or sync the control plane.

Remote LLM workers use separate gRPC and reverse QUIC paths. Complete
[LLM worker listeners](./gateway-routing.md#llm-worker-listeners) and
[Remote compute clusters and regions](./llm-function-enablement.md#remote-compute-clusters-and-regions)
before applying the control plane.

<Warning>
The Gateway address is embedded throughout your deployment. The `domain` value
in your environment file, the Gateway API HTTPRoutes/TCPRoutes, and service
discovery all depend on this address. If the Gateway or its underlying load
balancer is deleted and recreated (e.g., due to a TCPRoute misconfiguration), a
new address will be assigned.

If the address changes after deployment, you must update the `domain` in your
environment file and re-sync the affected releases. See
[Recovering from Gateway Address Changes](#recovering-from-gateway-address-changes)
for the procedure.

</Warning>

### Step 2. Configure your environment file (`environments/<environment-name>.yaml`)

The environment file provides the values for every control-plane Helm chart.
Set `HELMFILE_ENV` to your environment name and copy the base configuration.
The filename must match `HELMFILE_ENV` because Helmfile uses it to select the
environment file.

```bash
cd path/to/nvcf-self-managed-stack
export HELMFILE_ENV="<environment-name>"
cp environments/base.yaml "environments/${HELMFILE_ENV}.yaml"
```

Edit the copy. If you followed [Gateway quickstart](./gateway-routing.md#gateway-quickstart)
and pull from a single registry, you only need to change:

1. `global.domain`: the Gateway load balancer address (`GATEWAY_ADDR`)
2. `global.helm.sources.registry` and `global.helm.sources.repository`: your Helm chart registry
3. `global.image.registry` and `global.image.repository`: your container image registry

Every other section (`ingress`, `nodeSelectors`, `cassandra`, `observability`,
`accounts`, `addons`, chart value overrides, worker image pins) is documented
in the [Environment File Reference](./environment-reference.md). Review it
before a production install; in particular, keep `cassandra.resourcesPreset`
at `xlarge` or higher for cloud installs and set
`openbao.migrations.issuerDiscovery.enabled: true` on managed Kubernetes.

<Warning>
If you mirrored NVCF artifacts to your own registry, point both
`global.helm.sources` and `global.image` at the mirror, and re-mirror all
artifacts before each stack upgrade. See
[Image Mirroring](/nvcf/overview/image-mirroring).

</Warning>

### Step 3. Configure your secrets file (`secrets/<environment-name>-secrets.yaml`)

Secrets configuration contains any sensitive data required for NVCF operation. The image pull secret credentials you insert here will be used to bootstrap the NVCF API with registry credentials for all worker components (function sidecars), function containers and helm charts.

These credentials will then be used for function deployments. Note that if the registry credentials are not correct you can always update them using the steps in [Registries](./registries.md).

Copy the secrets template using the same `HELMFILE_ENV` value from Step 2. The
filename must match `HELMFILE_ENV` because Helmfile loads the corresponding
secrets file. The example below shows the required structure
([example-secrets.yaml](https://raw.githubusercontent.com/NVIDIA/nvcf/main/docs/overview/samples/configs/cp-example-secrets.yaml)). You must
replace all instances of `REPLACE_WITH_BASE64_DOCKER_CREDENTIAL` with your
actual base64-encoded registry credentials.

```bash
cd path/to/nvcf-self-managed-stack
cp secrets/secrets.yaml.template "secrets/${HELMFILE_ENV}-secrets.yaml"
```

<Accordion title="Configuration Template">
</Accordion>

```yaml title="secrets/example-secrets.yaml"

# Required structure for any environment secrets.

# This is the minimal set of values to provide.

# Notes:

# Cassandra:

# The password should match the value set in the cassandra keyspace migrations

#

# API:

# The value for the registry will be used in three places, as it is

# expected the same registry is used as a single source for all images.

# openbao.migrations.env[1].value

# api.accountBootstrap.registryCredentials[0].secret.value

# api.accountBootstrap.registryCredentials[1].secret.value

openbao:
  migrations:
    env:
      # Stored in OpenBao shared secrets (written by migration job)
      - name: DEFAULT_CASSANDRA_PASSWORD
        value: "ch@ng3m3"
      # Stored in OpenBao KV for nvcf-api (written by migration job)
      - name: NVCF_API_SIDECARS_IMAGE_PULL_SECRET
        value: REPLACE_WITH_BASE64_DOCKER_CREDENTIAL # Replace with base64 credentials (ex. NGC / ECR / etc.) for your registry, refer to Working with Third-Party Registries.
      - name: ADMIN_CLIENT_ID
        value: ncp # <- keep this value

api:
  accountBootstrap:
    registryCredentials:
      - registryHostname: nvcr.io # ECR: <your-account-id>.dkr.ecr.<your-region>.amazonaws.com
        secret:
          name: nvcr-containers # ECR: ecr-containers
          value: REPLACE_WITH_BASE64_DOCKER_CREDENTIAL # Replace with base64 credentials (ex. NGC / ECR / etc.) for your registry, refer to Working with Third-Party Registries.
        artifactTypes: ["CONTAINER"]
        tags: []
        description: "NGC Container registry"
      - registryHostname: helm.ngc.nvidia.com # ECR: <your-account-id>.dkr.ecr.<your-region>.amazonaws.com
        secret:
          name: nvcr-helmcharts # ECR: ecr-helmcharts
          value: REPLACE_WITH_BASE64_DOCKER_CREDENTIAL # Replace with base64 credentials (ex. NGC / ECR / etc.) for your registry, refer to Working with Third-Party Registries.
        artifactTypes: ["HELM"]
        tags: []
        description: "NGC Helm registry"

```

<Note>
NVCF supports these registries for function containers (set in
api.accountBootstrap.registryCredentials): ACR (Azure), ECR (AWS), NVCR
(NVIDIA), VolcEngine CR, JFrog/Artifactory, and Harbor.

</Note>

#### Generating Base64-encoded Registry Credentials

Registry credentials must be base64-encoded in the format `username:password`. For detailed instructions on setting up credentials for specific registries (including IAM user creation for ECR), see [Registries](./registries.md).

<Tabs>
<Tab title="NGC Registry">

```bash
# Replace YOUR_NGC_API_KEY with your actual personal NGC API key from ngc.nvidia.com
printf '%s' '$oauthtoken:YOUR_NGC_API_KEY' | base64 | tr -d '\n'
```

</Tab>

<Tab title="Amazon ECR">

For AWS ECR, NVCF requires permanent IAM credentials. You must first create a
dedicated IAM user with ECR permissions. See
[Registries](./registries.md#adding-aws-ecr-registry-credentials) for complete setup
instructions.

Once you have created the IAM user and obtained the access keys:

```bash
# Replace with your IAM user's access key ID and secret access key
ACCESS_KEY_ID="<access-key-id>"
SECRET_ACCESS_KEY="<secret-access-key>"

printf '%s' "${ACCESS_KEY_ID}:${SECRET_ACCESS_KEY}" | base64 | tr -d '\n'
```

</Tab>

<Tab title="VolcEngine CR">

Once you have your VolcEngine Access Key ID and Secret Access Key (see [Registries](./registries.md#adding-volcano-engine-container-registry-credentials) for full details):

```bash
# Replace with your VolcEngine Access Key ID and Secret Access Key
ACCESS_KEY_ID="<access-key-id>"
SECRET_ACCESS_KEY="<secret-access-key>"

printf '%s' "${ACCESS_KEY_ID}:${SECRET_ACCESS_KEY}" | base64 | tr -d '\n'
```

</Tab>

</Tabs>

Set kubectl to the control-plane cluster context before proceeding. Steps 4
and 5 run kubectl and helmfile commands that target the current context. In a
multi-cluster setup, verify the context is the control-plane cluster to avoid
installing to the wrong cluster.

```bash
kubectl config use-context <control-plane-context>
kubectl config current-context
```

### Step 4. Configure image pull secrets (conditional)

<Note>
Skip this step if you have mirrored NVCF artifacts to a CSP-managed registry
(e.g., ECR) and are using a CSP-managed registry with built-in credential
helpers (e.g., AWS ECR with IAM node roles, GKE Artifact Registry with Workload
Identity, Azure ACR with managed identity). Kubernetes can pull images
automatically in those environments.

</Note>

The secrets file you configured in Step 3 handles API bootstrap registry
credentials. These allow the NVCF API service to pull user function containers
at runtime. Separately, Kubernetes itself needs image pull secrets to pull the
NVCF control plane service images (API, SIS, Cassandra, etc.) from your
registry.

If your `image` registry is private and your cluster nodes do not have built-in credential helpers, you must create Kubernetes `docker-registry` secrets in each NVCF namespace and configure the helmfile to reference them.

1. Create the pull secret in each NVCF namespace
   ([create-nvcr-pull-secrets.sh](https://raw.githubusercontent.com/NVIDIA/nvcf/main/docs/overview/samples/scripts/create-nvcr-pull-secrets.sh)):

```bash
export NGC_API_KEY="<your-ngc-api-key>"

for ns in cassandra-system nats-system nvcf api-keys ess sis \
          vault-system cert-manager; do
  kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
done

for ns in cassandra-system nats-system nvcf api-keys ess sis \
          vault-system cert-manager; do
  kubectl create secret docker-registry nvcr-pull-secret \
    --docker-server=nvcr.io \
    --docker-username='$oauthtoken' \
    --docker-password="$NGC_API_KEY" \
    --namespace="$ns" \
    --dry-run=client -o yaml | kubectl apply -f -
done
```

For registries other than NGC, replace `--docker-server`, `--docker-username`, and `--docker-password` with your registry credentials.

1. Reference the secret in your Helmfile environment. The Helmfile propagates
   `imagePullSecrets` to all NVCF charts automatically. Add the secret name to
   your environment YAML (e.g. `environments/<your-env>.yaml`):

```yaml
global:
  imagePullSecrets:
    - name: nvcr-pull-secret
```

This replaces any need for a separate admission controller or policy engine to inject pull secrets.

### Step 5. Deploy the NVCF control plane components

Confirm your kubectl context is still set to the control-plane cluster (see
above).

<Info>
Ensure your local environment is authenticated to the container registry where
your NVCF Helm charts are stored (see
[Access Requirements](#access-requirements)). Helmfile pulls OCI charts during
deployment and will fail if not authenticated.

</Info>

Before deploying, preview the rendered Kubernetes manifests:

```bash
cd path/to/nvcf-self-managed-stack
HELMFILE_ENV=<environment-name> helmfile template
```

This command will:

1. Render all Helm charts with your environment and secrets
2. Run validation hooks
3. Display the resulting Kubernetes manifests

<Info>
Review the output carefully to ensure:

- Container image references are correct
- Storage classes match your clusters

</Info>

Deploy the self-managed stack:

```bash
HELMFILE_ENV=<environment-name> helmfile sync
```

<Note>
The initial deployment takes approximately 5-10 minutes for local development
and 10-20 minutes for cloud deployments.

</Note>

#### Deployment Progression and Monitoring

Helmfile will deploy services in the correct order with dependencies:

Phase 1: dependency layer (5-10 minutes)

- NATS messaging service
- OpenBao (secrets management)
- Cassandra (database)
- Helmfile selector: `release-group=dependencies`

Phase 2: control-plane services (5-10 minutes)

- NVCF API Service
- SIS (Spot Instance Service)
- gRPC Proxy
- Invocation Service
- API Keys Service
- ESS API
- Notary Service
- Admin Issuer Proxy
- Helmfile selector: `release-group=services`

<Info>
Monitor for account bootstrap failures. Once Helmfile reaches Phase 3, open a
separate terminal and watch events in the `nvcf` namespace:

```bash
kubectl get events -n nvcf -w
```

The account bootstrap job runs as a post-install hook and is the most common
failure point, usually due to environment or secrets misconfiguration. If it
fails, see
[Recovering from Partial Deployments](#recovering-from-partial-deployments) for
recovery steps.

</Info>

Phase 3: ingress configuration (1-2 minutes)

- Gateway API Routes (if enabled)
- Helmfile selector: `release-group=ingress`

GPU clusters are registered after the control plane succeeds. See
[Register a GPU Cluster](/nvcf/compute-plane/register-gpu-cluster).

Open a separate terminal to monitor the deployment progress:

Monitor each deployment phase:

```bash
# Check namespace creation and preparation
kubectl get ns

# Phase 1: Check dependency services (release-group=dependencies)
kubectl get pods -n nats-system        # Should see nats-0, nats-1, nats-2
kubectl get pods -n vault-system       # Should see openbao-server-0, openbao-server-1, openbao-server-2
kubectl get pods -n cassandra-system   # Should see cassandra-0, cassandra-1, cassandra-2
# Note: It's normal to see cassandra-initialize-cluster pods with "Error" status.
# The initialization job retries on failure - as long as one pod shows "Completed"
# and cassandra-migrations is Running/Completed, the deployment is progressing normally.

# Phase 2: Check control plane services (release-group=services)
kubectl get events -n nvcf -w       # Watch for account bootstrap failures
kubectl get pods -n nvcf            # API, invocation-service, grpc-proxy, notary-service
kubectl get pods -n sis             # Spot Instance Service
kubectl get pods -n api-keys        # API Keys service, admin-issuer-proxy
...

# Phase 3: Check ingress (release-group=ingress)
kubectl get httproutes -A          # Gateway API routes (if enabled)
```

<Note>
Cassandra initialization pods showing `Error` is expected. The
`cassandra-initialize-cluster` job runs multiple pods in parallel and retries
on failure. It is normal to see one or more pods with `Error` status. The
deployment is healthy as long as at least one initialization pod reaches
`Completed` and the `cassandra-migrations` job completes successfully.

</Note>

<Tip>
If any pod remains in `Pending`, `ContainerCreating`, or `ImagePullBackOff` state for more than 5 minutes, see [self-hosted-troubleshooting](./troubleshooting.md) for issue identification commands and solutions.

</Tip>

#### Recovering from Partial Deployments

<Warning>
Do not attempt to fix a partially failed deployment by re-running `helmfile sync` or `helmfile apply`. Helm releases in a failed state will skip initialization hooks on subsequent runs, leading to incomplete deployments that appear successful but don't function correctly.

</Warning>

Redeploying dependencies if needed:

If a dependency service (Cassandra, NATS, OpenBao) fails or gets stuck, you can
safely redeploy it individually:

```bash
# Redeploy only Cassandra
HELMFILE_ENV=<environment-name> helmfile --selector name=cassandra apply

# Redeploy all dependencies (NATS, Cassandra, OpenBao)
HELMFILE_ENV=<environment-name> helmfile --selector release-group=dependencies apply
```

Recovering from services failures without destroying dependencies:

If the `release-group=services` deployment hangs or fails (for example, account bootstrap failure due to secrets misconfiguration), you can recover without destroying your dependencies.

1. Monitor for failures:

In a separate terminal, watch events in the nvcf namespace:

```bash
kubectl get events -n nvcf -w
```

1. Check the account bootstrap logs if it failed:

```bash
kubectl logs job/nvcf-api-account-bootstrap -n nvcf
```

<Note>
The bootstrap job auto-deletes after ~5 minutes. Monitor events to catch failures in real-time.

</Note>

1. Check the NVCF API logs for detailed error messages:

```bash
kubectl logs -n nvcf -l app.kubernetes.io/name=nvcf-api --tail=100
```

1. Fix the root cause, for example correct your
   `secrets/<environment-name>-secrets.yaml` file.

2. Destroy the services and downstream releases:

```bash
# Destroy services release group
HELMFILE_ENV=<environment-name> helmfile --selector release-group=services destroy

# Destroy downstream releases (ingress, admin-issuer-proxy)
HELMFILE_ENV=<environment-name> helmfile --selector release-group=ingress destroy
HELMFILE_ENV=<environment-name> helmfile --selector name=admin-issuer-proxy destroy
```

1. Clean up the service namespaces:

```bash
kubectl delete namespace nvcf api-keys ess sis --ignore-not-found
```

1. Recreate namespaces and labels. Gateway API routing requires these labels:

```bash
kubectl create namespace api-keys && \
kubectl create namespace ess && \
kubectl create namespace sis && \
kubectl create namespace nvcf

kubectl label namespace api-keys nvcf/platform=true && \
kubectl label namespace sis nvcf/platform=true && \
kubectl label namespace ess nvcf/platform=true && \
kubectl label namespace nvcf nvcf/platform=true
```

1. Re-sync services. This triggers fresh post-install hooks:

```bash
HELMFILE_ENV=<environment-name> helmfile --selector release-group=services sync
```

1. Sync remaining releases after services succeed:

```bash
HELMFILE_ENV=<environment-name> helmfile --selector name=admin-issuer-proxy sync
HELMFILE_ENV=<environment-name> helmfile --selector release-group=ingress sync
```

Full restart if dependencies are also broken:

If dependencies are corrupted or you prefer a clean slate, follow the complete
[Uninstalling](#uninstalling) steps, fix your configuration, then redeploy from
Step 1.

#### Vanity Gateway and NVCF UI addons

Vanity Gateway and NVCF UI are optional addons that are disabled by default and
present only in stack packages that include them. Enable them after the core
control plane is healthy. See [Gateway Routing](./gateway-routing.md#vanity-gateway-optional)
for Vanity Gateway and [Enabling NVCF UI](./nvcf-ui.md) for NVCF UI.

#### Recovering from Gateway Address Changes

If your Gateway or its underlying load balancer was deleted and recreated (e.g., due to a TCPRoute misconfiguration or infrastructure change), the external address will change. Services that depend on the `domain` value -- including Gateway API routes, SIS cluster registration, API hostname resolution, and the optional Vanity Gateway route -- will break until the new address is propagated.

1. Get the new Gateway address:

```bash
GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')
echo "$GATEWAY_ADDR"
```

1. Update your environment file with the new address:

```bash
# Edit environments/<environment-name>.yaml
# Change: domain: "OLD_ADDRESS"
# To:     domain: "NEW_GATEWAY_ADDR"
```

1. Re-sync ingress and services that depend on the domain:

```bash
# Re-sync gateway routes (picks up new domain)
HELMFILE_ENV=<environment-name> helmfile --selector release-group=ingress sync

# Re-sync services that embed the domain (API, admin-issuer-proxy)
HELMFILE_ENV=<environment-name> helmfile --selector release-group=services sync
HELMFILE_ENV=<environment-name> helmfile --selector name=admin-issuer-proxy sync
```

1. Verify routes are using the new address:

```bash
kubectl get httproutes -A
kubectl get tcproutes -A
```

<Tip>
If you encounter issues during deployment, consult the [self-hosted-troubleshooting](./troubleshooting.md) guide for common problems and solutions.

</Tip>

### Step 6. Verify the Installation

Verify the installation is successful by checking the pods are running and the helm releases are successful.

```bash
# View all pods with node assignment and status, should all be Running or Completed state
kubectl get pods -A -o wide

# Check helm releases status
helm list -A
```

#### Verify API Connectivity (Optional)

If you configured Gateway API ingress, you can verify the NVCF API is accessible by running the following commands.

1. Set up environment variables:

```bash
# Get the Gateway address from Gateway quickstart
export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')
echo "Gateway Address: $GATEWAY_ADDR"
```

1. Generate an admin token:

```bash
# Generate an admin API token
export NVCF_TOKEN=$(curl -s -X POST "http://${GATEWAY_ADDR}/v1/admin/keys" \
  -H "Host: api-keys.${GATEWAY_ADDR}" \
  | grep -o '"value":"[^"]*"' | cut -d'"' -f4)

echo "Token generated: ${NVCF_TOKEN:0:20}..."
```

1. List functions. The list should be empty initially:

```bash
# List all functions
curl -s -X GET "http://${GATEWAY_ADDR}/v2/nvcf/functions" \
  -H "Host: api.${GATEWAY_ADDR}" \
  -H "Authorization: Bearer ${NVCF_TOKEN}" | jq .
```

## Next Steps

The control plane is installed. Continue with
[Register a GPU Cluster](/nvcf/compute-plane/register-gpu-cluster) to connect
each compute plane (GPU cluster) to this control plane. For the end-to-end
sequence, see the [Installation Guide](/nvcf/overview/installation-guide).

## Uninstalling

<Warning>
This will delete all NVCF resources including data stored in persistent volumes. Ensure you have backups of any important data.

</Warning>

To remove the NVCF installation:

```bash
HELMFILE_ENV=<environment-name> helmfile destroy
```

After `helmfile destroy` completes, clean up the namespaces:

```bash
# Delete NVCF namespaces
kubectl delete namespace cassandra-system nats-system vault-system \
  nvcf api-keys ess sis nvcf-ui \
  --ignore-not-found
```

To also remove the Gateway infrastructure created by [Gateway quickstart](./gateway-routing.md#gateway-quickstart):

```bash
# Delete the Gateway and GatewayClass resources
kubectl delete gateway nvcf-gateway -n envoy-gateway --ignore-not-found
kubectl delete gatewayclass eg --ignore-not-found

# Uninstall Envoy Gateway
helm uninstall eg -n envoy-gateway-system

# Delete the gateway namespaces
kubectl delete namespace envoy-gateway envoy-gateway-system --ignore-not-found

# (Optional) Remove Gateway API CRDs if no longer needed
kubectl delete -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.0/experimental-install.yaml
```
