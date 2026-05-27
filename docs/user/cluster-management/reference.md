# Helm Values Reference

The `nvca-operator` Helm chart is configured through a standard Helm values file (`values.yaml`)
passed to `helm upgrade -f values.yaml`. This page documents all available parameters.

<Warning>
The parameters listed below are a snapshot and may not reflect the latest chart version.
Always refer to the `values.yaml` and `values.schema.json` included in your chart
version for the authoritative list of parameters and defaults:

</Warning>

## How Values Are Structured

The chart values are organized into two layers:

1. **Shared parameters** (top-level) — These control the operator image, authentication,
   node placement, network policies, observability, agent resources, and agent runtime config.
   Examples: `image`, `ngcConfig`, `nodeSelector`, `networkPolicy`, `agent`,
   `agentConfig`.
2. **\`\`selfManaged.\*\`\`** — Used when `ngcConfig.clusterSource` is `"self-managed"`.
   These define the backend configuration including NVCA version, feature gates, cluster
   attributes, and manual GPU config.

**The key field is \`\`ngcConfig.clusterSource\`\`:**

- `"self-managed"` — The operator reads backend configuration from `selfManaged.*` values.

```yaml
# --- Shared parameters (all modes) ---
image:
  repository: "nvcr.io/nvidia/nvcf-byoc/nvca-operator"
ngcConfig:
  clusterSource: "helm-managed"    # ← This determines which section below is used
  serviceKey: "<your-key>"
nodeSelector:
  key: "node.kubernetes.io/instance-type"
  value: "m5.2xlarge"

# --- Only read when clusterSource is "helm-managed" ---
helmManaged:
  cloudProvider: "aws"
  clusterRegion: "us-west-2"
  nvcaVersion: "2.97.0"
  featureGateValues: ["DynamicGPUDiscovery"]

# --- Only read when clusterSource is "self-managed" ---
# selfManaged:
#   nvcaVersion: "2.97.0"
#   featureGateValues: ["DynamicGPUDiscovery"]
```

<Tip>
The `helmManaged` and `selfManaged` sections share many of the same fields
(`nvcaVersion`, `featureGateValues`, `gpuManualInstanceConfigB64`,
`clusterAttributes`). The difference is that `helmManaged` also requires cluster
identity fields (`cloudProvider`, `clusterRegion`, `clusterGroupID`,
`clusterGroupName`) that self-managed deployments get from their own control plane.

</Tip>

## Shared Parameters

These parameters apply to all deployment modes.

### NVCA Operator

```yaml
## Container images
image:
  repository: "nvcr.io/nvidia/nvcf-byoc/nvca-operator"  # Operator image path
  tag: ""                                                 # Defaults to chart version
  pullPolicy: IfNotPresent

nvcaImage:
  repositoryOverride: ""   # Override NVCA agent image path (staging/testing only)
  pullPolicy: IfNotPresent

## Image pull secrets
generateImagePullSecret: true                  # Auto-generate from ngcConfig.serviceKey
imagePullSecretName: "nvca-operator-image-pull" # Name of the generated secret
imagePullSecrets: []                            # Additional pre-existing pull secrets

## Service account
serviceAccount:
  create: true
  annotations: {}
  name: ""           # Auto-generated if empty

## Operator settings
replicaCount: 1
systemNamespace: nvca-operator
logLevel: info                  # debug, info, warn, error
priorityClassName: ""           # K8s PriorityClassName for eviction preference
k8sVersionOverride: ""          # Override K8s version NVCA registers with
enableGXCache: true             # Enable GXCache support
ddcsIPAllowList: ""             # Comma-separated CIDRs for DDCS access control
nvcaHelmRepositoryPrefix: ""    # Restrict Helm repos to specific org/team
```

### NGC Authentication

```yaml
ngcConfig:
  username: '$oauthtoken'
  serviceKey: ""                   # NGC Cluster Key or NVCF API Key (NAK)
  serviceKeySecretName: "ngc-service-key"   # K8s Secret name if serviceKey not set inline
  serviceKeySecretKeyName: "ngcServiceKey"   # Key within the Secret
  apiURL: https://api.ngc.nvidia.com         # NGC API URL (override for self-hosted)
  clusterSource: ngc-managed      # "ngc-managed", "helm-managed", or "self-managed"
```

### Node Selector

```yaml
nodeSelector:
  key: node.kubernetes.io/instance-type   # Label key for operator pod placement
  value: ""                                # Label value (empty = no constraint)
```

### Network Policies

```yaml
networkPolicy:
  clusterNetworkCIDRs:        # CIDRs that workload pods are NOT allowed to access
    - "10.0.0.0/8"
    - "172.16.0.0/12"
    - "192.168.0.0/16"
    - "100.64.0.0/12"
  customPolicies: []           # Custom NetworkPolicy definitions for function namespaces
```

### OpenTelemetry

```yaml
otel:
  enabled: false
  lightstep:
    serviceName: ""        # Lightstep service name
    accessToken: ""        # Lightstep API token
```

### Agent Configuration

```yaml
agent:
  cacheMountOptionsEnabled: true
  cacheMountOptions: "ro,norecovery,nouuid"
  workerDegradationPeriod: ""           # e.g., "90m", "1h30m"
  secretMirrorNamespace: nvca-operator  # Namespace to mirror custom secrets from
  secretMirrorLabelSelector: ""         # Label selector for mirrored secrets
  customAnnotations: {}                 # Extra annotations on the agent pod
  functionEnvOverrides: {}              # Override infra container images for functions
  taskEnvOverrides: {}                  # Override infra container images for tasks
  overrideEnvironmentVariables: {}      # Override env vars on the NVCA agent container
  resources:
    limits:
      cpu: 1000m
      memory: 4Gi
    requests:
      cpu: 100m
      memory: 200Mi

## Merge custom YAML into the generated NVCA agent config at runtime
agentConfig:
  mergeConfig: ""
  # Example:
  # mergeConfig: |
  #   agent:
  #     logLevel: debug

## OTel Collector sidecar (for K8s event collection)
otelCollector:
  enabled: false
  imageRepository: ""       # Auto-calculated if empty
  imageTag: 0.143.2
```

## Helm-Managed Parameters

Only used when `ngcConfig.clusterSource: "helm-managed"`.

```yaml
helmManaged:
  ## Cluster identity (REQUIRED, immutable after initial registration)
  cloudProvider: ""          # e.g., "aws", "gcp", "azure", "ON-PREM", "NCP"
  clusterRegion: ""          # e.g., "us-west-2"
  clusterGroupID: ""         # Unique cluster group identifier
  clusterGroupName: ""       # Human-readable cluster group name

  ## Backend configuration
  nvcaVersion: ""                      # NVCA agent version (REQUIRED)
  clusterDescription: ""              # Defaults to cluster name if empty
  featureGateValues: []               # e.g., ["DynamicGPUDiscovery", "CachingSupport"]
  gpuManualInstanceConfigB64: ""      # Base64-encoded GPU config (manual instance only)
  clusterAttributes: []               # e.g., ["CacheOptimized", "NVLinkOptimized"]

  ## Authentication (optional)
  oAuthClientID: ""                   # OAuth2/OIDC client ID (for internal NVIDIA clusters)
  oAuthClientSecretKey: ""            # Secret key for OAuth client

  ## Image overrides (advanced, usually auto-calculated)
  imageCredHelper:
    imageRepository: ""
    imageTag: 0.5.1
  otelCollector:
    imageRepository: ""
    imageTag: 0.143.2
```

## Self-Managed Parameters

Only used when `ngcConfig.clusterSource: "self-managed"` (self-hosted NVCF).

```yaml
selfManaged:
  nvcaVersion: ""                          # NVCA agent version (REQUIRED)
  featureGateValues: ["DynamicGPUDiscovery"]  # Default includes GPU discovery
  gpuManualInstanceConfigB64: ""           # Base64-encoded GPU config (manual instance only)
  clusterAttributes: []                    # e.g., ["CacheOptimized"]

  ## Image overrides (advanced, usually auto-calculated)
  imageCredHelper:
    imageRepository: ""
    imageTag: 0.5.1
  otelCollector:
    imageRepository: ""
    imageTag: 0.143.2
```

<Note>
The `selfManaged` and `helmManaged` sections share the same backend fields. The key
differences are:

- `selfManaged` does not have cluster identity fields (`cloudProvider`,
  `clusterRegion`, `clusterGroupID`, `clusterGroupName`) — these come from the
  self-hosted control plane.
- `selfManaged.featureGateValues` defaults to `["DynamicGPUDiscovery"]`. To disable a
  feature, remove it from the list (set to `[]`).
- `helmManaged.featureGateValues` defaults to `[]`. To disable a feature, prefix it
  with `-` (e.g., `["-DynamicGPUDiscovery"]`).

</Note>

## Related Documentation

- [NVCA Configuration](./configuration.md) — how to use these values to configure specific features (caching, network policies, manual instance config, etc.)
- [Agent config merging](./configuration.md) — using `agentConfig.mergeConfig` for runtime config overrides
