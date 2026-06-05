# Helmfile Installation

This section covers manual Helmfile installation of the NVCF control plane components, which are required for all self-hosted NVCF deployments.

For a fresh install, start with the [Quickstart](./quickstart.md). Use this Helmfile guide when you need explicit release control, partial recovery, upgrades, or direct access to Helmfile values. You can also install each Helm chart individually using `helm install` or `helm upgrade` (see [Standalone Deployment](./standalone-deployment.md)).

<Info>
This guide assumes you have already downloaded and extracted the `nvcf-self-managed-stack` helmfile bundle (see [download-nvcf-self-managed-stack](./image-mirroring.md)). All commands in this guide are run from inside the extracted `nvcf-self-managed-stack/` directory unless otherwise noted. The directory contains the helmfile definitions, environment templates, and sample configurations referenced throughout.

```bash
cd path/to/nvcf-self-managed-stack
ls
# Expected contents: helmfile.d/  environments/  secrets/  global.yaml.gotmpl  ...
```

</Info>

## Namespace Requirements

Each Helm chart in the NVCF stack must be installed into a specific namespace. These namespace
assignments are **fixed** and must not be changed because service-to-service cluster DNS addressing
and Vault (OpenBao) authentication claims depend on this layout.

| Namespace | Services |
| --- | --- |
| `nvcf` | api, invocation-service, grpc-proxy, notary-service, reval, state-metrics |
| `api-keys` | api-keys, admin-issuer-proxy |
| `ess` | ess-api |
| `sis` | sis |
| `vault-system` | openbao-server |
| `cassandra-system` | cassandra |
| `nats-system` | nats |
| `envoy-gateway-system` | ingress (nvcf-gateway-routes) |

<Warning>
Installing a chart into the wrong namespace will cause authentication failures such as
`error validating claims: claim "/kubernetes.io/namespace" does not match any associated bound claim values`.
If you see this error, verify that every release is deployed in the namespace shown above.

</Warning>

## Prerequisites

### Required Tools and Software

The following tools must be installed on your deployment machine:

- **kubectl**
- **helm** >= 3.12
- **helmfile** >= 1.1.0 (recommended: `1.1.x`)
- **helm-diff** plugin >=3.11

<Warning>
**Avoid Helmfile 1.2.x.** Helmfile 1.2.0 removed sequential execution mode, which the NVCF stack requires for ordered deployments. Use version `1.1.x` for compatibility with the commands in this guide.

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
**kubectl version must match your cluster (within one minor version).** Using a kubectl version that is more than one minor version ahead of your Kubernetes cluster will cause `kubectl apply` and `kubectl patch` commands to **fail** -- not just warn -- due to stricter server-side field validation in newer clients.

This is especially common on **macOS with Homebrew**, where `brew install kubectl` or `brew upgrade` can silently install a version much newer than your cluster. Verify before proceeding:

```bash
kubectl version
# Ensure the Client Version and Server Version are within one minor version of each other.
# Example: Client v1.32.x against Server v1.31.x is OK.
#          Client v1.32.x against Server v1.29.x will cause failures.
```

If your client is too new, install a matching version directly from the [Kubernetes release page](https://kubernetes.io/docs/tasks/tools/).

</Warning>

### Access Requirements

- **kubectl** configured to the kubernetes cluster you are deploying to

- Personal **NGC API Key** from [ngc.nvidia.com](https://ngc.nvidia.com) authenticated with `nvcf-onprem` organization **only if** you pull artifacts directly from NGC or use NGC as your registry

- **Registry credentials** for your container registry (ECR, NGC, etc.) - see [third-party-registries-self-hosted](./third-party-registries.md) for setup instructions

- **Local Helm/Docker authentication** to your container registry where NVCF charts are stored. Helmfile pulls OCI charts during deployment, so your local environment must be authenticated. Examples:

  - **AWS ECR**: `aws ecr get-login-password --region <region> | helm registry login --username AWS --password-stdin <account-id>.dkr.ecr.<region>.amazonaws.com`
  - **NGC**: `docker login nvcr.io -u '$oauthtoken' -p <NGC_API_KEY>`
  - **Other registries**: Use `docker login` or `helm registry login` as appropriate for your registry

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
6. Verify the installation

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

<Warning>
**The Gateway address is embedded throughout your deployment.** The `domain` value in your environment file, the Gateway API HTTPRoutes/TCPRoutes, and service discovery all depend on this address. If the Gateway or its underlying load balancer is deleted and recreated (e.g., due to a TCPRoute misconfiguration), a **new address** will be assigned.

If the address changes after deployment, you must update the `domain` in your environment file and re-sync the affected releases. See [Recovering from Gateway Address Changes] for the procedure.

</Warning>

### Step 2. Configure your environment file (`environments/<environment-name>.yaml`)

Environment configuration files define how NVCF is deployed in your specific environment. They are YAML files that provide values to the Helm charts.

Create your environment file from the template below ([cp-env-eks-example.yaml](samples/configs/cp-env-eks-example.yaml)).

```bash
cd path/to/nvcf-self-managed-stack
touch environments/<environment-name>.yaml
# Copy the template into the file
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

The default Cassandra resource limits may be insufficient for clusters with large instance types (e.g., `p5.48xlarge`), causing Cassandra pods to be OOM-killed during initialization. If you observe Cassandra pods restarting with `OOMKilled` status, increase the Cassandra resource requests and limits using a Helmfile release values override (see [overriding-helm-chart-values](./helmfile-installation.md)).

Add a `values` block to the cassandra release in `helmfile.d/01-dependencies.yaml.gotmpl`:

```yaml
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
          limits:
            cpu: "8"
            memory: 8192Mi
          requests:
            cpu: "2"
            memory: 4096Mi
```

Then apply the change to just Cassandra:

```bash
HELMFILE_ENV=<environment-name> helmfile --selector name=cassandra sync
```

<Note>
When overriding `values` on a release that uses `<<: *dependency`, you must re-include `global.yaml.gotmpl` and the secrets file in your `values` list because YAML merge replaces lists entirely. Adjust CPU and memory values to suit your workload.

</Note>

#### `helm` and `image` Configuration

The `helm` and `image` sections tell NVCF which registries to pull Helm charts and container images from.

- `helm.sources`: The OCI registry where NVCF Helm charts are stored. Helmfile pulls charts from here at deploy time (requires local authentication -- see [Access Requirements]).
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

**When upgrading to a new** `nvcf-self-managed-stack` **version**, you must re-mirror all artifacts before running `helmfile sync`. Each stack release may introduce new or updated container images and Helm charts. If these are not present in your private registry, pods will fail with `ImagePullBackOff`. Check the [self-hosted-artifact-manifest](./manifest.md) for the complete list of required artifacts and versions.

</Warning>

<Note>
**Pulling directly from NGC is the recommended approach** and avoids the need to manually mirror artifacts on every upgrade. If your environment permits it, configure `helm.sources` and `image` to point to the NGC registry (`nvcr.io`) and use your NGC API key for authentication. This ensures you always have access to the latest artifacts without additional mirroring steps.

</Note>

<Note>
These settings control *where* images are pulled from, not *how* Kubernetes authenticates to pull them. If your `image` registry is private, you may also need to configure image pull secrets -- see [Step 4](./helmfile-installation.md).

</Note>

<Tip>
**Quick Start Summary:** If you are using the example EKS environment YAML directly and followed [Gateway quickstart](./gateway-routing.md#gateway-quickstart), you only need to change:

1. `domain`: Replace `GATEWAY_ADDR` with the Gateway load balancer address
2. `helm.sources.registry` and `helm.sources.repository`: Point to your Helm chart registry
3. `image.registry` and `image.repository`: Point to your container image registry

</Tip>

#### Overriding Helm Chart Values

<Accordion title="Overriding Helm Chart Values">
The environment file (`environments/<environment-name>.yaml`) controls global settings like `domain`, `image`, and `nodeSelectors`. However, you may need to override values for a **specific Helm chart** -- for example, to increase Cassandra memory limits or change an image tag for one service.

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
When a release inherits from a template (`<<: *dependency`), specifying `values` on the release **replaces** the template's `values` list (YAML merge does not append lists). You must re-include `global.yaml.gotmpl` and the secrets file.

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

Values defined here take the **highest precedence**, overriding both the environment file and `global.yaml.gotmpl`. Use `helmfile template` to preview the rendered manifests after adding overrides, then apply to a single release:

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

Create your secrets file from the template below ([example-secrets.yaml](samples/configs/cp-example-secrets.yaml)). You must replace all instances of `REPLACE_WITH_BASE64_DOCKER_CREDENTIAL` with your actual base64-encoded registry credentials.

```bash
cd path/to/nvcf-self-managed-stack
touch secrets/<environment-name>-secrets.yaml
# Copy the template into the file
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
NVCF supports these registries for function containers (set in api.accountBootstrap.registryCredentials): **ACR** (Azure), **ECR** (AWS), **NVCR** (NVIDIA),
**VolcEngine CR**, **JFrog/Artifactory**, and **Harbor**.

</Note>

#### Generating Base64-encoded Registry Credentials

Registry credentials must be base64-encoded in the format `username:password`. For detailed instructions on setting up credentials for specific registries (including IAM user creation for ECR), see [third-party-registries-self-hosted](./third-party-registries.md).

<Tabs>
<Tab title="NGC Registry">

```bash
# Replace YOUR_NGC_API_KEY with your actual personal NGC API key from ngc.nvidia.com
echo -n '$oauthtoken:YOUR_NGC_API_KEY' | base64 -w 0
```

</Tab>

<Tab title="Amazon ECR">

For AWS ECR, NVCF requires **permanent IAM credentials**. You must first create a dedicated IAM user with ECR permissions. See [ecr-registry-setup](./third-party-registries.md) for complete setup instructions.

Once you have created the IAM user and obtained the access keys:

```bash
# Replace with your IAM user's access key ID and secret access key
ACCESS_KEY_ID="<access-key-id>"
SECRET_ACCESS_KEY="<secret-access-key>"

echo -n "${ACCESS_KEY_ID}:${SECRET_ACCESS_KEY}" | base64 -w 0
```

</Tab>

<Tab title="VolcEngine CR">

Once you have your VolcEngine Access Key ID and Secret Access Key (see [vcr-registry-setup](./third-party-registries.md) for full details):

```bash
# Replace with your VolcEngine Access Key ID and Secret Access Key
ACCESS_KEY_ID="<access-key-id>"
SECRET_ACCESS_KEY="<secret-access-key>"

echo -n "${ACCESS_KEY_ID}:${SECRET_ACCESS_KEY}" | base64 -w 0
```

</Tab>

</Tabs>

### Step 4. Configure image pull secrets (conditional)

<Note>
**Skip this step** if you have mirrored NVCF artifacts to a CSP-managed registry (e.g., ECR) and are using a CSP-managed registry with built-in credential helpers (e.g., AWS ECR with IAM node roles, GKE Artifact Registry with Workload Identity, Azure ACR with managed identity). Kubernetes can pull images automatically in those environments.

</Note>

The secrets file you configured in Step 3 handles **API bootstrap registry credentials** -- these allow the NVCF API service to pull user function containers at runtime. Separately, Kubernetes itself needs **image pull secrets** to pull the NVCF control plane service images (API, SIS, Cassandra, etc.) from your registry.

If your `image` registry is private and your cluster nodes do not have built-in credential helpers, you must create Kubernetes `docker-registry` secrets in each NVCF namespace and configure the helmfile to reference them.

**1. Create the pull secret** in each NVCF namespace ([create-nvcr-pull-secrets.sh](samples/scripts/create-nvcr-pull-secrets.sh)):

```bash
export NGC_API_KEY="<your-ngc-api-key>"

for ns in cassandra-system nats-system nvcf api-keys ess sis vault-system nvca-operator nvca-system nvcf-backend; do
  kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
done

for ns in cassandra-system nats-system nvcf api-keys ess sis vault-system nvca-operator nvca-system nvcf-backend; do
  kubectl create secret docker-registry nvcr-pull-secret \
    --docker-server=nvcr.io \
    --docker-username='$oauthtoken' \
    --docker-password="$NGC_API_KEY" \
    --namespace="$ns" \
    --dry-run=client -o yaml | kubectl apply -f -
done
```

For registries other than NGC, replace `--docker-server`, `--docker-username`, and `--docker-password` with your registry credentials.

**2. Reference the secret in your helmfile environment.** The helmfile propagates `imagePullSecrets` to all NVCF charts automatically. Add the secret name to your environment YAML (e.g. `environments/<your-env>.yaml`):

```yaml
global:
  imagePullSecrets:
    - name: nvcr-pull-secret
```

This replaces any need for a separate admission controller or policy engine to inject pull secrets.

### Step 5. Deploy the NVCF control plane components

Set kubectl context to your cluster.

<Info>
Ensure your local environment is authenticated to the container registry where your NVCF Helm charts are stored (see [Access Requirements]). Helmfile pulls OCI charts during deployment and will fail if not authenticated.

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
The initial deployment takes approximately **5-10 minutes** for local development and **10-20 minutes** for cloud deployments.

</Note>

#### Deployment Progresssion and Monitoring

Helmfile will deploy services in the correct order with dependencies:

**Phase 1: Dependency Layer (5-10 minutes)**

- NATS messaging service
- OpenBao (secrets management)
- Cassandra (database)
- **Helmfile Selector:** `release-group=dependencies`

**Phase 2: Control Plane Services (5-10 minutes)**

- NVCF API Service
- SIS (Spot Instance Service)
- gRPC Proxy
- Invocation Service
- API Keys Service
- ESS API
- Notary Service
- Admin Issuer Proxy
- **Helmfile Selector:** `release-group=services`

<Info>
**Monitor for account bootstrap failures:** Once helmfile reaches Phase 3, open a separate terminal and watch events in the `nvcf` namespace:

```bash
kubectl get events -n nvcf -w
```

The account bootstrap job runs as a post-install hook and is the most common failure point (usually due to environment or secrets misconfiguration). If it fails, see [Recovering from Partial Deployments] for recovery steps.

</Info>

**Phase 3: Ingress Configuration (1-2 minutes)**

- Gateway API Routes (if enabled)
- **Helmfile Selector:** `release-group=ingress`

**Phase 4: (Optional) GPU Operator (1-2 minutes)**

- Fake GPU Operator (optional, for development environments)
- **Helmfile Selector:** `release-group=workers`

Open a separate terminal to monitor the deployment progress:

**Monitor Each Deployment Phase:**

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
**Cassandra initialization pods showing "Error" is expected.** The `cassandra-initialize-cluster`
job runs multiple pods in parallel and retries on failure. It is normal to see one or more pods
with `Error` status. The deployment is healthy as long as at least one initialization pod
reaches `Completed` and the `cassandra-migrations` job completes successfully.

</Note>

<Tip>
If any pod remains in `Pending`, `ContainerCreating`, or `ImagePullBackOff` state for more than 5 minutes, see [self-hosted-troubleshooting](./troubleshooting.md) for issue identification commands and solutions.

</Tip>

#### Recovering from Partial Deployments

<Warning>
Do not attempt to fix a partially failed deployment by re-running `helmfile sync` or `helmfile apply`. Helm releases in a failed state will skip initialization hooks on subsequent runs, leading to incomplete deployments that appear successful but don't function correctly.

</Warning>

**Redeploying Dependencies (if needed):**

If a **dependency service** (Cassandra, NATS, OpenBao) fails or gets stuck, you can safely redeploy it individually:

```bash
# Redeploy only Cassandra
HELMFILE_ENV=<environment-name> helmfile --selector name=cassandra apply

# Redeploy all dependencies (NATS, Cassandra, OpenBao)
HELMFILE_ENV=<environment-name> helmfile --selector release-group=dependencies apply
```

**Recovering from Services Failures (without destroying dependencies):**

If the `release-group=services` deployment hangs or fails (for example, account bootstrap failure due to secrets misconfiguration), you can recover without destroying your dependencies.

**1. Monitor for failures:**

In a separate terminal, watch events in the nvcf namespace:

```bash
kubectl get events -n nvcf -w
```

**2. Check the account bootstrap logs** (if it failed):

```bash
kubectl logs job/nvcf-api-account-bootstrap -n nvcf
```

<Note>
The bootstrap job auto-deletes after ~5 minutes. Monitor events to catch failures in real-time.

</Note>

**3. Check the NVCF API logs** for detailed error messages:

```bash
kubectl logs -n nvcf -l app.kubernetes.io/name=nvcf-api --tail=100
```

**4. Fix the root cause** (e.g., correct your `secrets/<environment-name>-secrets.yaml` file).

**5. Destroy the services and downstream releases:**

```bash
# Destroy services release group
HELMFILE_ENV=<environment-name> helmfile --selector release-group=services destroy

# Destroy downstream releases (ingress, workers, admin-issuer-proxy)
HELMFILE_ENV=<environment-name> helmfile --selector release-group=ingress destroy
HELMFILE_ENV=<environment-name> helmfile --selector release-group=workers destroy
HELMFILE_ENV=<environment-name> helmfile --selector name=admin-issuer-proxy destroy
```

**6. Clean up the service namespaces:**

```bash
kubectl delete namespace nvcf api-keys ess sis --ignore-not-found
```

**7. Recreate namespaces and labels** (required for Gateway API routing):

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

**8. Re-sync services** (this triggers fresh post-install hooks):

```bash
HELMFILE_ENV=<environment-name> helmfile --selector release-group=services sync
```

**9. Sync remaining releases** after services succeed:

```bash
HELMFILE_ENV=<environment-name> helmfile --selector name=admin-issuer-proxy sync
HELMFILE_ENV=<environment-name> helmfile --selector release-group=ingress sync
HELMFILE_ENV=<environment-name> helmfile --selector release-group=workers sync
```

**Full Restart (if dependencies are also broken):**

If dependencies are corrupted or you prefer a clean slate, follow the complete [Uninstalling] steps, fix your configuration, then redeploy from Step 1.

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

**1. Get the new Gateway address:**

```bash
GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')
echo "$GATEWAY_ADDR"
```

**2. Update your environment file** with the new address:

```bash
# Edit environments/<environment-name>.yaml
# Change: domain: "OLD_ADDRESS"
# To:     domain: "NEW_GATEWAY_ADDR"
```

**3. Re-sync ingress and services** that depend on the domain:

```bash
# Re-sync gateway routes (picks up new domain)
HELMFILE_ENV=<environment-name> helmfile --selector release-group=ingress sync

# Re-sync services that embed the domain (API, admin-issuer-proxy)
HELMFILE_ENV=<environment-name> helmfile --selector release-group=services sync
HELMFILE_ENV=<environment-name> helmfile --selector name=admin-issuer-proxy sync
```

**4. Verify** routes are using the new address:

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

**1. Set up environment variables:**

```bash
# Get the Gateway address from Gateway quickstart
export GATEWAY_ADDR=$(kubectl get gateway nvcf-gateway -n envoy-gateway -o jsonpath='{.status.addresses[0].value}')
echo "Gateway Address: $GATEWAY_ADDR"
```

**2. Generate an admin token:**

```bash
# Generate an admin API token
export NVCF_TOKEN=$(curl -s -X POST "http://${GATEWAY_ADDR}/v1/admin/keys" \
  -H "Host: api-keys.${GATEWAY_ADDR}" \
  | grep -o '"value":"[^"]*"' | cut -d'"' -f4)

echo "Token generated: ${NVCF_TOKEN:0:20}..."
```

**3. List functions (should be empty initially):**

```bash
# List all functions
curl -s -X GET "http://${GATEWAY_ADDR}/v2/nvcf/functions" \
  -H "Host: api.${GATEWAY_ADDR}" \
  -H "Authorization: Bearer ${NVCF_TOKEN}" | jq .
```

## Next Steps

After the control plane installation is successfully complete, proceed to [Self-Managed Clusters](./cluster-management/self-managed.md) to set up GPU cluster operations.

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
