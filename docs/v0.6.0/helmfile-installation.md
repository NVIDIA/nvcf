# Helmfile Installation

This section covers manual Helmfile installation of the NVCF control plane and
GPU cluster components for self-hosted NVCF deployments.

For a fresh install, start with the [Quickstart](./quickstart.md). Use this Helmfile guide when you need explicit release control, partial recovery, upgrades, or direct access to Helmfile values.

<Info>
This guide assumes you have already downloaded and extracted the
`nvcf-self-managed-stack` Helmfile bundle (see
[download-nvcf-self-managed-stack](./image-mirroring.md)). Control-plane
commands run from inside that directory unless otherwise noted. The directory
contains the control-plane Helmfile definitions, environment templates, and
sample configurations referenced throughout.

GPU clusters use the compute-plane Makefile in
`deploy/stacks/nvcf-compute-plane`. It registers one GPU cluster at a time and
installs the NVCA operator into that cluster.

Clone the public repository before the GPU cluster steps:

```bash
git clone https://github.com/nvidia/nvcf.git
```

```bash
cd path/to/nvcf-self-managed-stack
ls
# Expected contents: helmfile.d/  environments/  secrets/  global.yaml.gotmpl  ...
```

</Info>

## Namespace Requirements

Each control-plane Helm chart must be installed into a specific namespace. These
namespace assignments are fixed and must not be changed because
service-to-service cluster DNS addressing and Vault (OpenBao) authentication
claims depend on this layout.

| Namespace | Services |
| --- | --- |
| `nvcf` | api, invocation-service, grpc-proxy, notary-service, reval, state-metrics |
| `api-keys` | api-keys, admin-issuer-proxy |
| `ess` | ess-api |
| `sis` | sis |
| `vault-system` | openbao-server |
| `cassandra-system` | cassandra |
| `nats-system` | nats |
| `cert-manager` | cert-manager |
| `envoy-gateway-system` | ingress (nvcf-gateway-routes) |

<Warning>
Installing a chart into the wrong namespace will cause authentication failures such as
`error validating claims: claim "/kubernetes.io/namespace" does not match any associated bound claim values`.
If you see this error, verify that every release is deployed in the namespace shown above.

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
- Artifacts must be available in a registry that your Kubernetes cluster can access. This can be the `nvcf-onprem` registry for NVCF control plane service artifacts, but function containers and helm charts must be configured to a user-managed registry. See [self-hosted-artifact-manifest](./manifest.md) and [self-hosted-image-mirroring](./image-mirroring.md).
- The `nvcf-self-managed-stack` repository must be downloaded to your local machine (see [download-nvcf-self-managed-stack](./image-mirroring.md)).

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
  [third-party-registries-self-hosted](./third-party-registries.md) for setup
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
7. Register each GPU cluster with the control plane
8. Install the NVCA operator on each GPU cluster

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

Environment configuration files define how NVCF is deployed in your specific environment. They are YAML files that provide values to the Helm charts.

Set `HELMFILE_ENV` to your environment name and copy the base configuration.
The filename must match `HELMFILE_ENV` because Helmfile uses it to select the
environment file. The template below shows the values to configure for Amazon
EKS ([cp-env-eks-example.yaml](samples/configs/cp-env-eks-example.yaml)).

```bash
cd path/to/nvcf-self-managed-stack
export HELMFILE_ENV="<environment-name>"
cp environments/base.yaml "environments/${HELMFILE_ENV}.yaml"
```

<Accordion title="Configuration Template (Amazon EKS Environment)">

The following example shows a typical configuration for Amazon EKS:
</Accordion>
```yaml title="environments/eks-example.yaml"
global:

  # Domain for external access (used by Gateway API HTTPRoutes)
  domain: "GATEWAY_ADDR" # Replace with ELB domain

  # =============================================================================
  # Helm Chart Sources Configuration
  # =============================================================================
  # Configure the OCI registry where NVCF Helm charts are stored.
  # This must point to a registry containing the NVCF chart packages.
  # =============================================================================
  helm:
    sources:
      registry: <your-account-id>.dkr.ecr.<your-region>.amazonaws.com
      repository: <your-ecr-repository-name>
      # NGC Example:
      # registry: nvcr.io
      # repository: YOUR_ORG/YOUR_TEAM # e.g. 123456789102/YOUR_TEAM
      # ECR Example:
      # registry: <your-account-id>.dkr.ecr.<your-region>.amazonaws.com
      # repository: <your-ecr-repository-name>

  # =============================================================================
  # Container Image Registry Configuration
  # =============================================================================
  # Configure the container registry where NVCF service images are stored.
  # These images are pulled by Kubernetes when deploying the NVCF stack.
  # =============================================================================
  image:
    registry: <your-account-id>.dkr.ecr.<your-region>.amazonaws.com
    repository: <your-ecr-repository-name>
    # NGC Example:
    # registry: nvcr.io
    # repository: YOUR_ORG/YOUR_TEAM # e.g. 123456789102/YOUR_TEAM
    # ECR Example:
    # registry: <your-account-id>.dkr.ecr.<your-region>.amazonaws.com
    # repository: <your-ecr-repository-name>

  nodeSelectors:
    enabled: true # Set true when using dedicated node labels for NVCF workloads
    vault:
      key: nvcf.nvidia.com/workload
      value: vault
    cassandra:
      key: nvcf.nvidia.com/workload
      value: cassandra
    controlplane:
      key: nvcf.nvidia.com/workload
      value: control-plane

  storageClass: "gp3" # Customize to your storage class
  storageSize: "10Gi" # Customize to your storage size

  # =============================================================================
  # Observability Configuration
  # =============================================================================
  # Enable distributed tracing via OTLP (dsiabled by default).
  # This must point to an OTLP-compatible collector.
  # =============================================================================
  observability:
    tracing:
      enabled: false
      collectorEndpoint: ""
      collectorPort: 4317
      collectorProtocol: http
      # Example:
      # enabled: true
      # collectorEndpoint: <your-collector-endpoint>
      # collectorPort: <your-collector-port>
      # collectorProtocol: <your-collector-protocol>

fakeGpuOperator:
  enabled: false # If deploying locally with no GPUs, true
  ubuntu:
    imageName: alpine-k8s
    tag: 1.30.12

accounts: # Default NVCF account configuration
  limits:
    maxFunctions: 10
    maxTasks: 10 # Note: Tasks (NVCT) are not currently supported for EA
    maxTelemetries: 10 # Note: BYOO is not currently supported for EA
    maxRegistryCreds: 10

# These static global values are processed in the values template
nats:
  enabled: true

cassandra:
  enabled: true

openbao:
  enabled: true
  migrations:
    issuerDiscovery:
      enabled: true # Recommended true for EKS - discovers OIDC issuer automatically

# Ingress Gateway Configuration
ingress:
  gatewayApi:
    enabled: true
    controllerNamespace: "envoy-gateway-system" # must be set by the environment
    routes:
      nvcfApi:
        routeAnnotations: {}
      apiKeys:
        routeAnnotations: {}
      invocation:
        routeAnnotations: {}
      grpc:
        routeAnnotations: {}
    gateways:
      shared:
        name: "nvcf-gateway" # must be set by the environment
        namespace: "envoy-gateway" # must be set by the environment
        listenerName: http
      grpc:
        name: "nvcf-gateway" # must be set by the environment
        namespace: "envoy-gateway" # must be set by the environment
        listenerName: tcp
```


#### `domain` and `ingress` Configuration

The `domain` and `ingress` sections of the environment file are used to configure the external access to the NVCF control plane.

If using the above example directly for EKS, replace `GATEWAY_ADDR` with the Gateway load balancer address from [Gateway quickstart](./gateway-routing.md#gateway-quickstart).

```yaml
domain: "GATEWAY_ADDR" # Replace with ELB domain
```

If using the above example directly for EKS, your ingress configuration would look like this:

```yaml
ingress:
   gatewayApi:
      enabled: true
      controllerNamespace: "envoy-gateway-system"
      routes:
         nvcfApi:
            routeAnnotations: {}
         apiKeys:
            routeAnnotations: {}
         invocation:
            routeAnnotations: {}
         grpc:
            routeAnnotations: {}
      gateways:
         shared:
            name: "nvcf-gateway"
            namespace: "envoy-gateway"
            listenerName: http
         grpc:
            name: "nvcf-gateway"
            namespace: "envoy-gateway"
            listenerName: tcp
```

#### `nodeSelectors` Configuration

The `nodeSelectors` section of the environment file is used to configure the nodes on which the NVCF control plane components are deployed. Disable this unless you have a cluster with node selectors pre-configured on node pools within your cluster.

If your cluster uses dedicated node labels for NVCF workloads, enable this section with the following configuration:

```yaml
nodeSelectors:
  enabled: true
  vault:
    key: nvcf.nvidia.com/workload
    value: vault
  cassandra:
    key: nvcf.nvidia.com/workload
    value: cassandra
  controlplane:
    key: nvcf.nvidia.com/workload
    value: control-plane
```

#### `cassandra` Resource Tuning

Cassandra needs enough memory to complete first boot, commit-log replay, and the schema migration hooks. The default self-managed stack uses `cassandra.resourcesPreset: xlarge`, which maps to a Bitnami Cassandra preset with a 3 GiB memory request and a 6 GiB memory limit. Do not use the `small` preset for cloud installs. It can OOM-kill Cassandra during initialization and cause migration failures.

Common preset values:

| Preset | Requests | Limits |
| --- | --- | --- |
| `small` | 500m CPU, 512Mi memory | 750m CPU, 768Mi memory |
| `large` | 1 CPU, 2048Mi memory | 1.5 CPU, 3072Mi memory |
| `xlarge` | 1 CPU, 3072Mi memory | 3 CPU, 6144Mi memory |
| `2xlarge` | 1 CPU, 3072Mi memory | 6 CPU, 12288Mi memory |

All listed presets include a 50Mi ephemeral-storage request and 2Gi ephemeral-storage limit.

If Cassandra pods restart with `OOMKilled`, or the `cassandra-migrations` job fails with a consistency-level error while Cassandra pods are restarting, increase the preset in your environment file:

```yaml
cassandra:
  resourcesPreset: "2xlarge"
```

Then apply the change to just Cassandra:

```bash
HELMFILE_ENV=<environment-name> helmfile --selector name=cassandra sync
```

<Note>
For local development, a lower preset may be acceptable when the environment also reduces Cassandra to one replica. For cloud installs, start with `xlarge` or higher and tune from there.

</Note>

#### `helm` and `image` Configuration

The `helm` and `image` sections tell NVCF which registries to pull Helm charts and container images from.

- `helm.sources`: The OCI registry where NVCF Helm charts are stored. Helmfile pulls charts from here at deploy time (requires local authentication. See [Access Requirements](#access-requirements)).
- `image`: The container registry where NVCF service images are stored. Kubernetes pulls images from here at runtime.

```yaml
# Helm Chart Sources Configuration
helm:
  sources:
    registry: "nvcr.io"
    repository: "YOUR_ORG/YOUR_TEAM"
    # NGC Example:
    # registry: nvcr.io
    # repository: 123456789102/YOUR_TEAM
    # ECR Example:
    # registry: <your-account-id>.dkr.ecr.<your-region>.amazonaws.com
    # repository: <your-ecr-repository-name>

# Container Image Registry Configuration
image:
  registry: nvcr.io
  repository: YOUR_ORG/YOUR_TEAM
  # NGC Example:
  # registry: nvcr.io
  # repository: 123456789102/YOUR_TEAM
  # ECR Example:
  # registry: <your-account-id>.dkr.ecr.<your-region>.amazonaws.com
  # repository: <your-ecr-repository-name>
```

<Warning>
If you have mirrored NVCF artifacts to your own registry (e.g., ECR), update both `helm.sources` and `image` to point to your mirror. See [self-hosted-image-mirroring](./image-mirroring.md) for details on mirroring artifacts.

When upgrading to a new `nvcf-self-managed-stack` version, re-mirror all artifacts before running `helmfile sync`. Each stack release may introduce new or updated container images and Helm charts. If these are not present in your private registry, pods will fail with `ImagePullBackOff`. For split installs, mirror both the control-plane and compute-plane stack resources listed in the [self-hosted-artifact-manifest](./manifest.md).

</Warning>

<Note>
Pulling directly from NGC is the recommended approach and avoids the need to
manually mirror artifacts on every upgrade. If your environment permits it,
configure `helm.sources` and `image` to point to the NGC registry (`nvcr.io`)
and use your NGC API key for authentication. This ensures you always have access
to the latest artifacts without additional mirroring steps.

</Note>

<Note>
These settings control *where* images are pulled from, not *how* Kubernetes authenticates to pull them. If your `image` registry is private, you may also need to configure image pull secrets -- see [Step 4](./helmfile-installation.md).

</Note>

<Tip>
Quick start summary: If you are using the example EKS environment YAML directly
and followed [Gateway quickstart](./gateway-routing.md#gateway-quickstart), you
only need to change:

1. `domain`: Replace `GATEWAY_ADDR` with the Gateway load balancer address
2. `helm.sources.registry` and `helm.sources.repository`: Point to your Helm chart registry
3. `image.registry` and `image.repository`: Point to your container image registry

</Tip>

#### Overriding Helm Chart Values

<Accordion title="Overriding Helm Chart Values">
The environment file (`environments/<environment-name>.yaml`) controls global
settings like `domain`, `image`, and `nodeSelectors`. However, you may need to
override values for a specific Helm chart, for example to increase Cassandra
memory limits or change an image tag for one service.

Helmfile releases support a `values` property that passes values through to the underlying `helm install`/`helm upgrade` command. To add chart-specific overrides, edit the release definition in the appropriate file under `helmfile.d/` and add a `values` block:

```yaml
# Example: helmfile.d/01-dependencies.yaml.gotmpl
- name: cassandra
  version: 0.9.0
  condition: cassandra.enabled
  namespace: cassandra-system
  <<: *dependency
  values:
    - ../global.yaml.gotmpl
    - ../secrets/{{ requiredEnv "HELMFILE_ENV" }}-secrets.yaml
    - cassandra:
        resources:
          requests:
            cpu: "2"
            memory: 4096Mi
          limits:
            cpu: "8"
            memory: 8192Mi
```

<Note>
When a release inherits from a template (`<<: *dependency`), specifying `values`
on the release replaces the template's `values` list. YAML merge does not append
lists. You must re-include `global.yaml.gotmpl` and the secrets file.

</Note>

The `values` block is a list of YAML mappings. Keys correspond to the chart's `values.yaml` structure. For example, to override a deeply nested value:

```yaml
values:
  - api:
      image:
        tag: 2.223.9
      env:
        NVCF_REGISTRIES_ACCOUNT_PROVISIONING_ARTIFACT_TYPES: "CONTAINER,HELM"
```

Values defined here take the highest precedence, overriding both the environment
file and `global.yaml.gotmpl`. Use `helmfile template` to preview the rendered
manifests after adding overrides, then apply to a single release:

```bash
# Preview changes
HELMFILE_ENV=<environment-name> helmfile --selector name=cassandra template

# Apply changes to just that release
HELMFILE_ENV=<environment-name> helmfile --selector name=cassandra sync
```
</Accordion>

#### Worker Image Version Overrides

The NVCF API uses the worker image versions in its `nvcf.sidecars.*`
configuration. To pin worker sidecars for one Helmfile deployment, add an inline
remote-config override to the `api` release in `helmfile.d/02-core.yaml.gotmpl`:

```yaml
- name: api
  version: 1.19.3
  namespace: nvcf
  inherit:
    - template: service
  values:
    - ../global.yaml.gotmpl
    - ../secrets/{{ requiredEnv "HELMFILE_ENV" }}-secrets.yaml
    - api:
        remoteConfig:
          enabled: true
          configData:
            nvcf:
              sidecars:
                init-container: "${nvcf.sidecars.hostname}/${nvcf.sidecars.repository}/nvcf_worker_init:<tag>"
                utils-container-image:
                  go: "${nvcf.sidecars.hostname}/${nvcf.sidecars.repository}/nvcf_worker_utils:<tag>"
                niclls-container: "${nvcf.sidecars.hostname}/${nvcf.sidecars.repository}/nvcf_worker_niclls:<tag>"
  needs:
    - ess/ess-api
```

The `${nvcf.sidecars.hostname}` and `${nvcf.sidecars.repository}` placeholders
resolve from the stack image registry and repository settings. Re-include
`global.yaml.gotmpl` and the secrets file because defining `values` on the
release replaces the inherited `values` list. Keep the existing `needs` entry on
the release.

### Step 3. Configure your secrets file (`secrets/<environment-name>-secrets.yaml`)

Secrets configuration contains any sensitive data required for NVCF operation. The image pull secret credentials you insert here will be used to bootstrap the NVCF API with registry credentials for all worker components (function sidecars), function containers and helm charts.

These credentials will then be used for function deployments. Note that if the registry credentials are not correct you can always update them using the steps in [third-party-registries-self-hosted](./third-party-registries.md).

Copy the secrets template using the same `HELMFILE_ENV` value from Step 2. The
filename must match `HELMFILE_ENV` because Helmfile loads the corresponding
secrets file. The example below shows the required structure
([example-secrets.yaml](samples/configs/cp-example-secrets.yaml)). You must
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
#   The password should match the value set in the cassandra keyspace migrations
#
# API:
#   The value for the registry will be used in three places, as it is
#   expected the same registry is used as a single source for all images.
#     openbao.migrations.env[1].value
#     api.accountBootstrap.registryCredentials[0].secret.value
#     api.accountBootstrap.registryCredentials[1].secret.value

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

Registry credentials must be base64-encoded in the format `username:password`. For detailed instructions on setting up credentials for specific registries (including IAM user creation for ECR), see [third-party-registries-self-hosted](./third-party-registries.md).

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
[ecr-registry-setup](./third-party-registries.md) for complete setup
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

Once you have your VolcEngine Access Key ID and Secret Access Key (see [vcr-registry-setup](./third-party-registries.md) for full details):

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
   ([create-nvcr-pull-secrets.sh](samples/scripts/create-nvcr-pull-secrets.sh)):

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

2. Reference the secret in your Helmfile environment. The Helmfile propagates
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

GPU clusters are installed after the control plane succeeds. Use
[Step 7. Configure the compute-plane Helmfile environment](#step-7-configure-the-compute-plane-helmfile-environment)
and [Step 8. Register and install each GPU cluster](#step-8-register-and-install-each-gpu-cluster)
for the split compute-plane bundle.

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

2. Check the account bootstrap logs if it failed:

```bash
kubectl logs job/nvcf-api-account-bootstrap -n nvcf
```

<Note>
The bootstrap job auto-deletes after ~5 minutes. Monitor events to catch failures in real-time.

</Note>

3. Check the NVCF API logs for detailed error messages:

```bash
kubectl logs -n nvcf -l app.kubernetes.io/name=nvcf-api --tail=100
```

4. Fix the root cause, for example correct your
   `secrets/<environment-name>-secrets.yaml` file.

5. Destroy the services and downstream releases:

```bash
# Destroy services release group
HELMFILE_ENV=<environment-name> helmfile --selector release-group=services destroy

# Destroy downstream releases (ingress, admin-issuer-proxy)
HELMFILE_ENV=<environment-name> helmfile --selector release-group=ingress destroy
HELMFILE_ENV=<environment-name> helmfile --selector name=admin-issuer-proxy destroy
```

6. Clean up the service namespaces:

```bash
kubectl delete namespace nvcf api-keys ess sis --ignore-not-found
```

7. Recreate namespaces and labels. Gateway API routing requires these labels:

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

8. Re-sync services. This triggers fresh post-install hooks:

```bash
HELMFILE_ENV=<environment-name> helmfile --selector release-group=services sync
```

9. Sync remaining releases after services succeed:

```bash
HELMFILE_ENV=<environment-name> helmfile --selector name=admin-issuer-proxy sync
HELMFILE_ENV=<environment-name> helmfile --selector release-group=ingress sync
```

Full restart if dependencies are also broken:

If dependencies are corrupted or you prefer a clean slate, follow the complete
[Uninstalling](#uninstalling) steps, fix your configuration, then redeploy from
Step 1.

#### Enabling Vanity Gateway

Vanity Gateway is optional and disabled by default. It is available only in
stack packages that include the Vanity Gateway addon. If your extracted stack
package does not contain a `vanity-gateway` release and `vanityGateway` route
values, skip this section until you use a stack package that includes them.

Enable it only when you need a customer-facing hostname or path mapping layer in
front of the standard NVCF service routes.

In stack packages that include the addon, set the value shape in your
environment file:

```yaml
addons:
  vanityGateway:
    enabled: true
    mappingConfig: {}
```

By default, the route host is `vanity.<domain>` and the backend is
`vanity-gateway.nvcf:8080`. Put deployment-specific host and path mappings under
`addons.vanityGateway.mappingConfig`. If your deployment needs custom vanity
hosts, use the route hostname overrides supported by your stack package and
create matching DNS records.

After confirming your stack package includes the `vanity-gateway` release,
preview and apply the service plus gateway routes:

```bash
HELMFILE_ENV=<environment-name> helmfile --selector name=vanity-gateway template
HELMFILE_ENV=<environment-name> helmfile --selector name=vanity-gateway sync
HELMFILE_ENV=<environment-name> helmfile --selector release-group=ingress sync
```

Verify only when the addon is present and enabled:

```bash
kubectl get deploy,svc -n nvcf -l app.kubernetes.io/name=vanity-gateway
kubectl get httproute -A | grep -i vanity
curl -H "Host: vanity.<domain>" "http://<gateway-address>/health"
```

#### Recovering from Gateway Address Changes

If your Gateway or its underlying load balancer was deleted and recreated (e.g., due to a TCPRoute misconfiguration or infrastructure change), the external address will change. Services that depend on the `domain` value -- including Gateway API routes, SIS cluster registration, API hostname resolution, and the optional Vanity Gateway route -- will break until the new address is propagated.

1. Get the new Gateway address:

```bash
GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')
echo "$GATEWAY_ADDR"
```

2. Update your environment file with the new address:

```bash
# Edit environments/<environment-name>.yaml
# Change: domain: "OLD_ADDRESS"
# To:     domain: "NEW_GATEWAY_ADDR"
```

3. Re-sync ingress and services that depend on the domain:

```bash
# Re-sync gateway routes (picks up new domain)
HELMFILE_ENV=<environment-name> helmfile --selector release-group=ingress sync

# Re-sync services that embed the domain (API, admin-issuer-proxy)
HELMFILE_ENV=<environment-name> helmfile --selector release-group=services sync
HELMFILE_ENV=<environment-name> helmfile --selector name=admin-issuer-proxy sync
```

4. Verify routes are using the new address:

```bash
kubectl get httproutes -A
kubectl get tcproutes -A
```

<Tip>
If you encounter issues during deployment, consult the [self-hosted-troubleshooting](./troubleshooting.md) guide for common problems and solutions.

</Tip>

### Step 6: Verify the Installation

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

2. Generate an admin token:

```bash
# Generate an admin API token
export NVCF_TOKEN=$(curl -s -X POST "http://${GATEWAY_ADDR}/v1/admin/keys" \
  -H "Host: api-keys.${GATEWAY_ADDR}" \
  | grep -o '"value":"[^"]*"' | cut -d'"' -f4)

echo "Token generated: ${NVCF_TOKEN:0:20}..."
```

3. List functions. The list should be empty initially:

```bash
# List all functions
curl -s -X GET "http://${GATEWAY_ADDR}/v2/nvcf/functions" \
  -H "Host: api.${GATEWAY_ADDR}" \
  -H "Authorization: Bearer ${NVCF_TOKEN}" | jq .
```

### Step 7. Configure the compute-plane Helmfile environment

Use `deploy/stacks/nvcf-compute-plane` from the source repository for each GPU
cluster. The compute-plane Helmfile installs `helm-nvca-operator` and wires it
to the control plane values returned by cluster registration.

Create an environment file in the compute-plane directory. Use the same Helm
chart and image registry mirror as the control plane. Set the service URLs to
addresses reachable from the GPU cluster. If the GPU cluster reaches the
control plane through one load balancer with hostname-based Gateway routing,
keep the URL pointed at the load balancer and set the host-header overrides to
the route hostnames.

```bash
cd path/to/nvcf
touch deploy/stacks/nvcf-compute-plane/environments/<environment-name>.yaml
```

```yaml title="deploy/stacks/nvcf-compute-plane/environments/<environment-name>.yaml"
global:
  helm:
    sources:
      registry: <your-chart-registry>
      repository: <your-chart-repository>

  image:
    registry: <your-image-registry>
    repository: <your-image-repository>

  imagePullSecrets:
    - name: nvcr-pull-secret

  nvcaOperator:
    selfManaged:
      icmsServiceURL: "http://<GATEWAY_ADDR>"
      icmsServiceHostHeaderOverride: "sis.<STACK_DOMAIN>"
      revalServiceURL: "http://<GATEWAY_ADDR>"
      revalServiceHostHeaderOverride: "reval.<STACK_DOMAIN>"
      natsURL: "nats://<GATEWAY_ADDR>:4222"
      natsHostOverride: "nats.<STACK_DOMAIN>"
```

If your GPU cluster can resolve per-service DNS names directly, set the service
URLs to those names and omit the host-header override fields.

Create the pull secret in `nvca-operator` on the GPU cluster before installing
NVCA. Helmfile references the secret from `global.imagePullSecrets`, and the
operator propagates it to the managed namespaces after installation.

```bash
kubectl --kubeconfig <gpu-cluster-kubeconfig> \
  create namespace nvca-operator --dry-run=client -o yaml | \
  kubectl --kubeconfig <gpu-cluster-kubeconfig> apply -f -
kubectl --kubeconfig <gpu-cluster-kubeconfig> \
  create secret docker-registry nvcr-pull-secret \
  --docker-server=nvcr.io \
  --docker-username='$oauthtoken' \
  --docker-password="<registry-password>" \
  --namespace=nvca-operator \
  --dry-run=client -o yaml | kubectl --kubeconfig <gpu-cluster-kubeconfig> apply -f -
```

### Step 8. Register and install each GPU cluster

Register each GPU cluster before installing NVCA. Registration discovers the
GPU cluster's OIDC issuer and JWKS, records them with SIS/ICMS, and writes the
cluster identity values that the operator chart consumes.

Use `KUBECONFIG_FILE` for multi-cluster installs. It makes both registration and
Helmfile target the GPU cluster instead of the control-plane cluster.
For a complete Amazon EKS example, see the
[CSP End-to-End Example](./csp-end-to-end-example-installation.md).

The compute-plane Makefile runs `nvcf-cli init` before `cluster register`. Point
`NVCF_CLI_CONFIG` at a CLI config that can reach the control-plane gateway.

```yaml title="nvcf-cli-gpu-register.yaml"
base_http_url: "http://<GATEWAY_ADDR>"
invoke_url: "http://<GATEWAY_ADDR>"
api_keys_service_url: "http://<GATEWAY_ADDR>"
icms_url: "http://<GATEWAY_ADDR>"

api_keys_host: "api-keys.<STACK_DOMAIN>"
api_host: "api.<STACK_DOMAIN>"
icms_host: "sis.<STACK_DOMAIN>"
invoke_host: "invocation.<STACK_DOMAIN>"

api_keys_service_id: "nvidia-cloud-functions-ncp-service-id-aketm"
api_keys_issuer_service: "nvcf-api"
api_keys_owner_id: "svc@nvcf-api.local"
client_id: "<nca-id>"
```

Run the compute-plane target from the repository root. The target writes
`deploy/stacks/nvcf-compute-plane/registration/<gpu-cluster-name>-register-values.yaml`.

```bash
make -C deploy/stacks/nvcf-compute-plane register-cluster \
  CLUSTER_NAME=<gpu-cluster-name> \
  NCA_ID=<nca-id> \
  CLUSTER_REGION=<region> \
  ICMS_URL="http://<GATEWAY_ADDR>" \
  KUBECONFIG_FILE=<gpu-cluster-kubeconfig> \
  NVCF_CLI=<path-to-nvcf-cli> \
  NVCF_CLI_CONFIG=<path-to-nvcf-cli-gpu-register.yaml>
```

Install the NVCA operator on that GPU cluster. The `install` target copies the
registration file into `out/` and runs Helmfile with
`HELMFILE_ENV=<environment-name>`.

```bash
make -C deploy/stacks/nvcf-compute-plane install \
  CLUSTER_NAME=<gpu-cluster-name> \
  HELMFILE_ENV=<environment-name> \
  NCA_ID=<nca-id> \
  KUBECONFIG_FILE=<gpu-cluster-kubeconfig>
```

Verify the operator and backend on the GPU cluster:

```bash
kubectl --kubeconfig <gpu-cluster-kubeconfig> \
  rollout status deployment/nvca-operator -n nvca-operator --timeout=10m

kubectl --kubeconfig <gpu-cluster-kubeconfig> \
  get nvcfbackends -n nvca-operator

kubectl --kubeconfig <gpu-cluster-kubeconfig> \
  get secret nvcr-pull-secret -n nvca-system

kubectl --kubeconfig <gpu-cluster-kubeconfig> \
  get pods -n nvca-system
```

For a multi-cluster EKS install, this is the expected GPU cluster validation:
the control plane is installed, each GPU cluster is registered with its own
kubeconfig, the NVCA Operator is installed on that GPU cluster, and the
`NVCFBackend` reports a healthy agent. Repeat registration, install, and health
checks for each GPU cluster. Function execution also requires worker pods on the
GPU cluster to reach the control-plane worker endpoints. Do not use function
deployment or invocation as the acceptance check until those endpoints are
reachable from the GPU cluster.

Repeat Step 8 for each additional GPU cluster. Use a unique `CLUSTER_NAME` for
each cluster.

## Next Steps

After the control plane and GPU clusters are installed, proceed to
[Self-Managed Clusters](./cluster-management/self-managed.md) for NVCA
operations and troubleshooting.

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
  nvcf api-keys ess sis \
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
