# NVCA Operator Helm chart

NVCA Operator installs and manages reconfiguration, upgrade and health check of NVCF ClusterAgent Service
used in DGX Cloud and other K8s Clusters to run NVCF Workloads.

## Parameters

### NVCA Operator parameters

| Name                           | Description                                                                                                                                                                                           | Value                                    |
| ------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------- |
| `image.repository`             | NVCA Operator container registry path, without tag                                                                                                                                                    | `nvcr.io/nvidia/nvcf-byoc/nvca-operator` |
| `image.tag`                    | NVCA Operator container image tag. This defaults to the chart version                                                                                                                                 | `""`                                     |
| `image.pullPolicy`             | K8s ImagePullPolicy                                                                                                                                                                                   | `IfNotPresent`                           |
| `nvcaImage.repositoryOverride` | (Optional) Full NVCA container registry path, without tag. Only set this if the default needs to be overridden, for example "stg.nvcr.io/nvidia/nvcf-byoc/nvca". The tag is set in the cluster config | `""`                                     |
| `nvcaImage.pullPolicy`         | K8s ImagePullPolicy                                                                                                                                                                                   | `IfNotPresent`                           |

### OTel Collector Configuration

| Name                                      | Description                                                                                                                                                                                           | Value                      |
| ----------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------- |
| `otelCollector.enabled`                   | Enable OTel collector sidecar for K8s event collection                                                                                                                                                | `false`                    |
| `otelCollector.imageRepository`           | (OPTIONAL) Image repository of OpenTelemetry Collector sidecar. If not specified, it will be calculated based on image.repository (stg vs prod).                                                      | `""`                       |
| `otelCollector.imageTag`                  | Image tag of OpenTelemetry Collector sidecar.                                                                                                                                                         | `0.143.2`                  |
| `otelCollector.resources.limits.cpu`      | CPU limit for the OTel collector container                                                                                                                                                            | `1000m`                    |
| `otelCollector.resources.limits.memory`   | Memory limit for the OTel collector container                                                                                                                                                         | `1Gi`                      |
| `otelCollector.resources.requests.cpu`    | CPU request for the OTel collector container                                                                                                                                                          | `200m`                     |
| `otelCollector.resources.requests.memory` | Memory request for the OTel collector container                                                                                                                                                       | `256Mi`                    |
| `generateImagePullSecret`                 | Use the ngcConfig.serviceKey to generate an image pull secret for nvca and nvca-operator Pods                                                                                                         | `true`                     |
| `imagePullSecretName`                     | Name of the image pull secret to use for nvca and nvca-operator Pods.                                                                                                                                 | `nvca-operator-image-pull` |
| `imagePullSecrets`                        | List of pre-existing imagePullSecret objects in the nvca-operator namespace to use for nvca and nvca-operator Pods. Each object must have a 'name' field. Example: [{name: "foo-bar"}, {name: "baz"}] | `[]`                       |
| `serviceAccount.create`                   | Specifies whether a ServiceAccount should be created                                                                                                                                                  | `true`                     |
| `serviceAccount.annotations`              | Additional custom annotations for the ServiceAccount                                                                                                                                                  | `{}`                       |
| `serviceAccount.name`                     | The name of the ServiceAccount to use.                                                                                                                                                                | `""`                       |
| `replicaCount`                            | Replica count for the operator deployment                                                                                                                                                             | `1`                        |
| `systemNamespace`                         | Namespace in which NVCFBackend objects are created.                                                                                                                                                   | `nvca-operator`            |
| `logLevel`                                | Logging level for the module                                                                                                                                                                          | `info`                     |
| `ncaID`                                   | (REQUIRED) NVIDIA Cloud Account ID of the Primary Account                                                                                                                                             | `""`                       |
| `clusterID`                               | ID of the Cluster for this NVCA instance to manage                                                                                                                                                    | `""`                       |
| `clusterName`                             | for metrics & telemetry (REQUIRED when ngcConfig.clusterSource is "helm-managed")                                                                                                                     | `""`                       |
| `k8sVersionOverride`                      | Override the K8s version that NVCA registers with                                                                                                                                                     | `""`                       |
| `priorityClassName`                       | K8s PriorityClassName for NVCA pods preference during evictions                                                                                                                                       | `""`                       |
| `nvcaHelmRepositoryPrefix`                | Enables Helm repository restrictions to specific org/teams                                                                                                                                            | `""`                       |
| `enableGXCache`                           | Enables GXCache Support in NVCA                                                                                                                                                                       | `true`                     |
| `ddcsIPAllowList`                         | provides comma separated CIDR ranges to allowList                                                                                                                                                     | `""`                       |
| `agentConfig.mergeConfig`                 | Merge fields into the generated NVCA config. Must be a string.                                                                                                                                        | `""`                       |

### resources Resource requests and limits for the nvca-operator container

| Name                        | Description                                    | Value   |
| --------------------------- | ---------------------------------------------- | ------- |
| `resources.limits.cpu`      | CPU limit for the nvca-operator container      | `500m`  |
| `resources.limits.memory`   | Memory limit for the nvca-operator container   | `500Mi` |
| `resources.requests.cpu`    | CPU request for the nvca-operator container    | `50m`   |
| `resources.requests.memory` | Memory request for the nvca-operator container | `50Mi`  |

### Agent Container Resource configuration

| Name                                 | Description                                                                                                                                                                | Value                  |
| ------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------- |
| `agent.resources.limits.cpu`         | CPU limit for the nvca agent container                                                                                                                                     | `1000m`                |
| `agent.resources.limits.memory`      | Memory limit for the nvca agent container                                                                                                                                  | `4Gi`                  |
| `agent.resources.requests.cpu`       | CPU request for the nvca agent container                                                                                                                                   | `100m`                 |
| `agent.resources.requests.memory`    | Memory request for the nvca agent container                                                                                                                                | `200Mi`                |
| `agent.cacheMountOptionsEnabled`     | Enable or disable CSI volume mount options for NVCA caches                                                                                                                 | `true`                 |
| `agent.cacheMountOptions`            | Comma-separated string of CSI volume mount options (e.g., "ro,noatime,nouuid") used when cacheMountOptionsEnabled is true                                                  | `ro,norecovery,nouuid` |
| `agent.workerDegradationPeriod`      | Duration for determining if a worker is degraded (e.g., "90m", "1h30m")                                                                                                    | `""`                   |
| `agent.secretMirrorNamespace`        | Default namespace to mirror custom secrets for nvcf workloads                                                                                                              | `nvca-operator`        |
| `agent.secretMirrorLabelSelector`    | Label selector on the secrets in the sourceNamespace                                                                                                                       | `""`                   |
| `agent.customAnnotations`            | Map of custom annotations to add to the agent pod                                                                                                                          | `{}`                   |
| `agent.functionEnvOverrides`         | Map of environment variable overrides for function workloads (e.g., {"INIT_CONTAINER": "nvcr.io/custom/init:v1.0", "UTILS_CONTAINER": "nvcr.io/custom/utils:v1.0"})        | `{}`                   |
| `agent.taskEnvOverrides`             | Map of environment variable overrides for task workloads (e.g., {"INIT_CONTAINER": "nvcr.io/custom/init:v1.0", "ESS_AGENT_CONTAINER": "nvcr.io/custom/ess:v1.0"})          | `{}`                   |
| `agent.overrideEnvironmentVariables` | Map of environment variables to override on the NVCA agent container. These take precedence over default values. Example: {"LOG_LEVEL": "debug", "CUSTOM_FLAG": "enabled"} | `{}`                   |
| `agent.serviceOAuth`                 | OAuth token and JWKS endpoints used by dependent services                                                                                                                   | See `values.yaml`      |

### Webhook Container Resource configuration

| Name                                | Description                                   | Value   |
| ----------------------------------- | --------------------------------------------- | ------- |
| `webhook.resources.limits.cpu`      | CPU limit for the nvca webhook container      | `200m`  |
| `webhook.resources.limits.memory`   | Memory limit for the nvca webhook container   | `200Mi` |
| `webhook.resources.requests.cpu`    | CPU request for the nvca webhook container    | `50m`   |
| `webhook.resources.requests.memory` | Memory request for the nvca webhook container | `50Mi`  |

### NGC Configuration

| Name                                | Description                                                                                                                                                                                                     | Value                        |
| ----------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------- |
| `ngcConfig.username`                | Username for the registry authentication                                                                                                                                                                        | `$oauthtoken`                |
| `ngcConfig.serviceKey`              | ServiceKey (password) for authentication. If unset, a Secret with name set to ngcConfig.serviceKeySecretName is expected to exist in the cluster in the release namespace.                                      | `""`                         |
| `ngcConfig.serviceKeySecretName`    | Secret containing NGC ServiceKey (password) for authentication (default: ngc-service-key). If the ngcConfig.serviceKey is not set, the secret with this name must be created manually in the release namespace. | `ngc-service-key`            |
| `ngcConfig.serviceKeySecretKeyName` | Key in the secret ngcConfig.serviceKeySecretName containing the NGC ServiceKey (password).                                                                                                                      | `ngcServiceKey`              |
| `ngcConfig.apiURL`                  | NGC API URL for requesting auth tokens                                                                                                                                                                          | `https://api.ngc.nvidia.com` |
| `ngcConfig.clusterSource`           | Source of the cluster configuration:                                                                                                                                                                            | `ngc-managed`                |

### Vault Configuration

| Name                                       | Description                                                                                                                                             | Value |
| ------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------- | ----- |
| `vaultConfig.oAuthClientMountPathTemplate` | Template for constructing the OAuth client mount path in Vault. Use %s as placeholder for clientID. Example: "nvidia/services/oauth/clients/%s/kv/secret" | `""`  |
| `vaultConfig.oAuthClientMountPath`         | (Optional) Full OAuth client mount path. If set, overrides the computed path from template.                                                             | `""`  |

### Helm Managed NVCF Backend Configuration

| Name                                          | Description                                                                                                                                                                          | Value     |
| --------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------- |
| `helmManaged.cloudProvider`                   | (REQUIRED) Cloud provider for the cluster (e.g., aws, gcp, azure)                                                                                                                    | `""`      |
| `helmManaged.clusterRegion`                   | (REQUIRED) Region where the cluster is deployed                                                                                                                                      | `""`      |
| `helmManaged.clusterGroupID`                  | (REQUIRED) Group ID for the cluster                                                                                                                                                  | `""`      |
| `helmManaged.clusterGroupName`                | (REQUIRED) Name of the cluster group                                                                                                                                                 | `""`      |
| `helmManaged.nvcaVersion`                     | (REQUIRED) Version of the NVCFBackend to use                                                                                                                                         | `""`      |
| `helmManaged.oAuthClientID`                   | (Optional) Client ID for OAuth2/OIDC authentication. Can be blank or omitted.                                                                                                        | `""`      |
| `helmManaged.oAuthClientSecretKey`            | (Optional) Secret key to retrieve the client secret for OAuth2/OIDC client. Leave blank if not needed.                                                                               | `""`      |
| `helmManaged.clusterDescription`              | (Optional) Description of the cluster. Defaults to clusterName if not provided.                                                                                                      | `""`      |
| `helmManaged.featureGateValues`               | (Optional) List of feature gates to enable. Defaults to [] if not specified.                                                                                                         | `[]`      |
| `helmManaged.gpuManualInstanceConfigB64`      | (Optional) Base64 encoded GPU manual instance configuration. Leave blank if not required.                                                                                            | `""`      |
| `helmManaged.clusterAttributes`               | (Optional) List of attributes for the cluster. Defaults to an empty array.                                                                                                           | `[]`      |
| `helmManaged.imageCredHelper.imageRepository` | (OPTIONAL) Image repository of "nvcf-image-credential-helper". Only override this if you know what you are doing. If not specified, it will be calculated based on image.repository. | `""`      |
| `helmManaged.imageCredHelper.imageTag`        | (REQUIRED) Image tag of "nvcf-image-credential-helper". Only override this if you know what you are doing.                                                                           | `0.5.1`   |
| `helmManaged.otelCollector.enabled`           | Enable OTel collector sidecar for helm-managed clusters                                                                                                                              | `false`   |
| `helmManaged.otelCollector.imageRepository`   | (OPTIONAL) Image repository of "otel-collector". Only override this if you know what you are doing. If not specified, it will be calculated based on image.repository.               | `""`      |
| `helmManaged.otelCollector.imageTag`          | (REQUIRED) Image tag of "otel-collector". Only override this if you know what you are doing.                                                                                         | `0.143.2` |

### Self Managed NVCF Backend Configuration

| Name                                          | Description                                                                                                                                                                          | Value                                      |
| --------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------ |
| `selfManaged.nvcaVersion`                     | (REQUIRED) Version of the NVCFBackend to use                                                                                                                                         | `""`                                       |
| `selfManaged.featureGateValues`               | (Optional) List of feature gates to enable. Defaults to ["DynamicGPUDiscovery"] if not specified.                                                                                    | `["DynamicGPUDiscovery"]`                  |
| `selfManaged.gpuManualInstanceConfigB64`      | (Optional) Base64 encoded GPU manual instance configuration. Leave blank if not required.                                                                                            | `""`                                       |
| `selfManaged.clusterAttributes`               | (Optional) List of attributes for the cluster. Defaults to an empty array.                                                                                                           | `[]`                                       |
| `selfManaged.imageCredHelper.imageRepository` | (OPTIONAL) Image repository of "nvcf-image-credential-helper". Only override this if you know what you are doing. If not specified, it will be calculated based on image.repository. | `""`                                       |
| `selfManaged.imageCredHelper.imageTag`        | (REQUIRED) Image tag of "nvcf-image-credential-helper". Only override this if you know what you are doing.                                                                           | `0.5.1`                                    |
| `selfManaged.otelCollector.enabled`           | Enable OTel collector sidecar for self-managed clusters                                                                                                                              | `false`                                    |
| `selfManaged.otelCollector.imageRepository`   | (OPTIONAL) Image repository of "otel-collector". Only override this if you know what you are doing. If not specified, it will be calculated based on image.repository.               | `""`                                       |
| `selfManaged.otelCollector.imageTag`          | (REQUIRED) Image tag of "otel-collector". Only override this if you know what you are doing.                                                                                         | `0.143.2`                                  |
| `selfManaged.icmsServiceURL`                  | URL of the SIS/ICMS service for self-managed clusters. Required when ngcConfig.clusterSource is "self-managed".                                                                      | `""`                                      |
| `selfManaged.revalServiceURL`                 | URL of the ReVal service for self-managed clusters. Required when ngcConfig.clusterSource is "self-managed".                                                                         | `""`                                      |
| `selfManaged.natsURL`                         | URL of the NATS service for self-managed clusters. Required when ngcConfig.clusterSource is "self-managed".                                                                          | `""`                                      |

### Node Selector Configuration

| Name                 | Description               | Value                              |
| -------------------- | ------------------------- | ---------------------------------- |
| `nodeSelector.key`   | Node-selector Label key   | `node.kubernetes.io/instance-type` |
| `nodeSelector.value` | Node-selector Label value | `""`                               |

### OpenTelemetry configuration

| Name                         | Description                                                 | Value   |
| ---------------------------- | ----------------------------------------------------------- | ------- |
| `otel.enabled`               | Enable OpenTelemetry.                                       | `false` |
| `otel.lightstep.serviceName` | the name of the lightstep service to push telemetry data to | `""`    |
| `otel.lightstep.accessToken` | the access token for accessing the lightstep API            | `""`    |

### Graceful Shutdown Configuration

| Name                                             | Description                                                                                                                         | Value |
| ------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------- | ----- |
| `gracefulShutdown.terminationGracePeriodSeconds` | Maximum time (in seconds) for pod termination and cleanup (K8s hard limit)                                                          | `600` |
| `gracefulShutdown.cleanupTimeoutSeconds`         | HTTP handler timeout (in seconds). Must be less than terminationGracePeriodSeconds to ensure response is sent before K8s kills pod. | `540` |

### Network Policy Configuration

| Name                                | Description                                                                                           | Value                                                             |
| ----------------------------------- | ----------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------- |
| `networkPolicy.clusterNetworkCIDRs` | List of IPv4 CIDRs that workload pods are NOT allowed to access (typically cluster-internal networks) | `["10.0.0.0/8","172.16.0.0/12","192.168.0.0/16","100.64.0.0/12"]` |
| `networkPolicy.customPolicies`      | Array of custom network policy definitions to apply to nvca function namespaces                       | `[]`                                                              |

### Cluster Validator Configuration

| Name                                                  | Description                                                                       | Value                                  |
| ----------------------------------------------------- | --------------------------------------------------------------------------------- | -------------------------------------- |
| `clusterValidator.enabled`                            | Enable the cluster-validator CronJob and init container                            | `false`                                |
| `clusterValidator.image.repository`                   | Cluster Validator container registry path, without tag                            | `""`                                   |
| `clusterValidator.image.tag`                          | Cluster Validator container image tag                                             | `v2.0.0`                               |
| `clusterValidator.image.pullPolicy`                   | K8s ImagePullPolicy for cluster-validator                                         | `IfNotPresent`                         |
| `clusterValidator.schedule`                           | CronJob schedule (cron expression)                                                | `0 */3 * * *`                          |
| `clusterValidator.configMapName`                      | ConfigMap name for user-defined network checks                                    | `cluster-validator-network-checks`     |
| `clusterValidator.networkChecks`                      | Network check configuration (creates the ConfigMap automatically when set)        | `{}`                                   |
| `clusterValidator.networkChecks.reachability`         | Reachability check config; when set, endpoints replace built-in checks            | `{}`                                   |
| `clusterValidator.networkChecks.networkPolicies`      | Network policy validation config with namespace pairs                             | `{}`                                   |
| `clusterValidator.networkChecks.enforcement`          | Live enforcement testing config (deploys test pods)                               | `{}`                                   |
| `clusterValidator.networkChecks.enforcement.enabled`  | Enable live enforcement testing                                                   | `false`                                |
| `clusterValidator.networkChecks.enforcement.testImage`| Container image for enforcement test pods                                         | `busybox:1.36`                         |
| `clusterValidator.networkChecks.enforcement.timeoutSeconds` | Timeout in seconds for enforcement test operations                          | `90`                                   |
| `clusterValidator.resources.limits.cpu`               | CPU limit for the cluster-validator container                                     | `200m`                                 |
| `clusterValidator.resources.limits.memory`            | Memory limit for the cluster-validator container                                  | `128Mi`                                |
| `clusterValidator.resources.requests.cpu`             | CPU request for the cluster-validator container                                   | `100m`                                 |
| `clusterValidator.resources.requests.memory`          | Memory request for the cluster-validator container                                | `64Mi`                                 |
