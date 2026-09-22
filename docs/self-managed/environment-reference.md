# Environment File Reference

The Helmfile environment file `environments/<environment-name>.yaml` in the
`nvcf-self-managed-stack` bundle provides the values for every control-plane
Helm chart. Its name must match `HELMFILE_ENV`. This page documents each
section of the file. For the install procedure that consumes it, see
[Helmfile Installation](./helmfile-installation.md).

Start from `environments/base.yaml` and change only the keys your environment
needs. At minimum set `global.domain`, `global.helm.sources`, and
`global.image`.

## Full example (Amazon EKS)

The following file shows a typical configuration for Amazon EKS
([cp-env-eks-example.yaml](https://raw.githubusercontent.com/NVIDIA/nvcf/main/docs/overview/samples/configs/cp-env-eks-example.yaml)).

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

  workerEndpoints:
    # Optional. Empty uses the cluster-local request-router service. Set a
    # worker-reachable host and port for a split-cluster deployment.
    llmRequestRouterAddress: ""

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
  # Enable distributed tracing via OTLP (disabled by default).
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

# Install control-plane monitors, the bundled metrics backend, and the
# Function Autoscaler.
observability:
  profile: control

victoriaMetrics:
  server:
    persistentVolume:
      enabled: true
      size: 16Gi
      storageClass: "gp3" # Customize to your storage class.

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

When `addons.llm` is enabled, the stack defaults
`global.workerEndpoints.llmRequestRouterAddress` to
`llm-request-router-backend-router.nvcf.svc.cluster.local:50071` when backend
routing is enabled, otherwise `llm-request-router.nvcf.svc.cluster.local:50071`.
For a split deployment, this address alone is not
enough. Configure the paired backend-router gRPC and reverse QUIC dial
addresses, Gateway routes, DNS, and trust described in
[Remote compute clusters and regions](./llm-function-enablement.md#remote-compute-clusters-and-regions).

## `observability` Configuration

The self-managed control-plane stack defaults to
`observability.profile: control`. This installs the shared metrics components,
VictoriaMetrics, State Metrics, and the Function Autoscaler. Set the
VictoriaMetrics storage class for the target cluster.

To use a customer-managed backend or change component ownership, see
[Observability Configuration](/nvcf/observability/observability). For autoscaler health and
backend checks, see
[Function Autoscaler Operations](./autoscaling/operations.md).

## `domain` and `ingress` Configuration

The `domain` and `ingress` sections of the environment file are used to configure the external access to the NVCF control plane.

If using the full example above directly for EKS, replace `GATEWAY_ADDR` with the Gateway load balancer address from [Gateway quickstart](./gateway-routing.md#gateway-quickstart).

```yaml
domain: "GATEWAY_ADDR" # Replace with ELB domain
```

If using the full example above directly for EKS, your ingress configuration would look like this:

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

## `nodeSelectors` Configuration

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

## `cassandra` Resource Tuning

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

## `helm` and `image` Configuration

The `helm` and `image` sections tell NVCF which registries to pull Helm charts and container images from.

- `helm.sources`: The OCI registry where NVCF Helm charts are stored. Helmfile pulls charts from here at deploy time (requires local authentication. See [Access Requirements](./helmfile-installation.md#access-requirements)).
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
If you have mirrored NVCF artifacts to your own registry (e.g., ECR), update both `helm.sources` and `image` to point to your mirror. See [self-hosted-image-mirroring](/nvcf/overview/image-mirroring) for details on mirroring artifacts.

When upgrading to a new `nvcf-self-managed-stack` version, re-mirror all artifacts before running `helmfile sync`. Each stack release may introduce new or updated container images and Helm charts. If these are not present in your private registry, pods will fail with `ImagePullBackOff`. For split installs, mirror both core stack resources listed in the [self-hosted-artifact-manifest](/nvcf/overview/manifest). If you deploy shared observability as a standalone stack, mirror the observability stack resource as well.

</Warning>

<Note>
Pulling directly from NGC is the recommended approach and avoids the need to
manually mirror artifacts on every upgrade. If your environment permits it,
configure `helm.sources` and `image` to point to the NGC registry (`nvcr.io`)
and use your NGC API key for authentication. This ensures you always have access
to the latest artifacts without additional mirroring steps.

</Note>

<Note>
These settings control *where* images are pulled from, not *how* Kubernetes authenticates to pull them. If your `image` registry is private, you may also need to configure image pull secrets -- see [Helmfile Installation](./helmfile-installation.md#step-4-configure-image-pull-secrets-conditional).

</Note>

<Tip>
Quick start summary: If you are using the example EKS environment YAML directly
and followed [Gateway quickstart](./gateway-routing.md#gateway-quickstart), you
only need to change:

1. `domain`: Replace `GATEWAY_ADDR` with the Gateway load balancer address
2. `helm.sources.registry` and `helm.sources.repository`: Point to your Helm chart registry
3. `image.registry` and `image.repository`: Point to your container image registry

</Tip>

## Overriding Helm Chart Values

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

## Worker Image Version Overrides

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
