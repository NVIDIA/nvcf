/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package nvca

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	"github.com/bombsimon/logrusr/v4"
	"github.com/sirupsen/logrus"
	cli "github.com/urfave/cli/v2"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	nvcaauth "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/nvca/health"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

const (
	DefaultHeartBeatInterval              = 5 * time.Minute
	DefaultCredRenewInterval              = 45 * time.Minute
	DefaultSyncRequestStatusInterval      = 15 * time.Second
	DefaultPeriodicInstanceStatusInterval = 5 * time.Minute
	DefaultRolloverServicesUpdateInterval = 30 * time.Minute
	DefaultACKFrequencyInterval           = 15 * time.Minute
	V1BartOAuthScopes                     = "byoc_registration cluster_heartbeat instance_request_update"
	V1NVCAOAuthScopes                     = "nvca-cluster instance_request_update"
	DefaultNamespaceLabels                = "app.kubernetes.io/instance=nvca,app.kubernetes.io/managed-by=nvca-operator,app.kubernetes.io/name=nvca"

	// DefaultICMSRequestAckRetryTimeout is the default timeout for retrying ICMS request acknowledgements
	DefaultICMSRequestAckRetryTimeout = 5 * time.Minute
	// ICMSRequestAckRetryTimeoutEnvVarKey is the environment variable name for the retry timeout
	ICMSRequestAckRetryTimeoutEnvVarKey = "NVCA_ICMS_REQUEST_ACK_RETRY_TIMEOUT"
)

func NewCommand() *cli.Command {
	// TODO: use Destination in all flags possible from aopts struct fields.
	aopts := &AgentOptions{}
	defaultTimeConfig := (&k8sutil.TimeConfig{}).Complete()
	timeConfig := &k8sutil.TimeConfig{}

	return &cli.Command{
		Name:  types.AppName,
		Usage: "NV Cluster Agent for NVIDIA Cloud Functions",
		Flags: []cli.Flag{
			&cli.GenericFlag{
				Name:  "feature-flags",
				Usage: "Enable or disable features through flags",
				Value: &featureflag.CLIFlag{},
			},
			&cli.StringFlag{
				Name:     "nca-id",
				Usage:    "NVIDIA Cloud Account Id (NCAId)",
				EnvVars:  []string{"NVIDIA_CLOUD_ACCOUNT_ID"},
				Required: true,
			},
			&cli.StringSliceFlag{
				Name:   "authorized-nca-ids",
				Usage:  "DEPRECATED: Comma Separated Authorized NCAId, common across the clusterGroup",
				Hidden: true,
			},
			&cli.StringFlag{
				Name:    "cluster-id",
				Usage:   "ID of this edge K8s cluster (max 32 chars)",
				EnvVars: []string{"CLUSTER_ID"},
			},
			&cli.StringFlag{
				Name:    "cluster-region",
				Usage:   "Region of this edge K8s cluster (max 32 chars)",
				EnvVars: []string{"CLUSTER_REGION"},
			},
			&cli.StringFlag{
				Name:     "cluster-name",
				Usage:    "Name of this edge K8s cluster (max 32 chars)",
				EnvVars:  []string{"CLUSTER_NAME"},
				Required: true,
			},
			&cli.StringFlag{
				Name:     "cluster-description",
				Usage:    "Description of this edge K8s cluster (max 32 chars)",
				EnvVars:  []string{"CLUSTER_DESCRIPTION"},
				Required: true,
			},
			&cli.StringFlag{
				Name:    "cluster-group-id",
				Usage:   "Cluster Group ID (ID common across the customers different backends)",
				EnvVars: []string{"CLUSTER_GROUP_ID"},
			},
			&cli.StringFlag{
				Name:     "cluster-group-name",
				Usage:    "Cluster Group Name (name common across the customers different backends)",
				EnvVars:  []string{"CLUSTER_GROUP_NAME", "CLUSTER_GROUP"},
				Required: true,
				Aliases:  []string{"cluster-group"},
			},
			&cli.GenericFlag{
				Name:    "cluster-attributes",
				Usage:   "Cluster attributes of the form \"KEY=VALUE\"",
				EnvVars: []string{"CLUSTER_ATTRIBUTES"},
				Value:   featureflag.AttrCLIFlag{},
			},
			&cli.StringFlag{
				Name:     "cloud-provider",
				Usage:    "Cloud Provider Name for this backend",
				EnvVars:  []string{"CLOUD_PROVIDER"},
				Required: true,
			},
			&cli.StringFlag{
				Name:     "icms-service-url",
				Usage:    "ICMS Service URL to use for registration and credential refreshes",
				EnvVars:  []string{"ICMS_SERVICE_URL"},
				Required: true,
			},
			&cli.StringFlag{
				Name:    "nvca-rollover-service-url",
				Usage:   "Worker Rollover Service URL to automate worker rollover",
				EnvVars: []string{"NVCA_ROLLOVER_SERVICE_URL"},
				Value:   "https://api.ros.nvidia.com",
			},
			&cli.StringFlag{
				Name:  "system-namespace",
				Usage: "Namespace of NVCA app Pods and objects",
				Value: SystemNamespace,
			},
			&cli.StringFlag{
				Name:  "crowdstrike-namespace",
				Usage: "Namespace name for Crowdstrike injector (enables Crowdstrike network policies if present)",
				Value: k8sutil.CrowdstrikeNamespace,
			},
			&cli.StringFlag{
				Name:  "requests-namespace",
				Usage: "Namespace in which to create ICMS requests and related objects",
				Value: types.DefaultICMSRequestNamespace,
			},
			&cli.StringFlag{
				Name:     "namespace-labels",
				Usage:    "Namespace labels to apply to ICMS request namespaces created by NVCA",
				Required: true,
			},
			&cli.StringFlag{
				Name:    "k8s-version-override",
				Value:   "",
				Usage:   "Backend k8s Version Override to report to ICMS",
				EnvVars: []string{"K8S_VERSION_OVERRIDE"},
			},
			&cli.StringFlag{
				Name:    "oauth-client-id",
				EnvVars: []string{"OAUTH_CLIENT_ID"},
				Usage:   "OAuth Client ID / API Key for ICMS registration and Token refreshes",
			},
			&cli.StringFlag{
				Name:    "oauth-client-secret-key",
				EnvVars: []string{"OAUTH_CLIENT_SECRET_KEY"},
				Usage:   "OAuth Client Secret Key for ICMS registration and Token refreshes",
			},
			&cli.StringFlag{
				Name:  "oauth-client-secrets-env-file",
				Usage: "OAuth Client Secrets env file containing OAUTH_CLIENT_ID and OAUTH_CLIENT_SECRET_KEY values",
			},
			&cli.StringFlag{
				Name:     "oauth-icms-token-url",
				EnvVars:  []string{"OAUTH_ICMS_TOKEN_URL"},
				Usage:    "OAuth ICMS token URL to fetch and renew OAuth Tokens",
				Required: true,
			},
			&cli.StringFlag{
				Name:     "oauth-public-keyset-endpoint",
				Usage:    "Public JWKS Endpoint URL",
				EnvVars:  []string{"OAUTH_PUBLIC_KEYSET_ENDPOINT"},
				Required: true,
			},
			&cli.Uint64Flag{
				Name:    "oauth-token-fetch-failure-threshold",
				EnvVars: []string{"OAUTH_TOKEN_FETCH_FAILURE_THRESHOLD"},
				Usage:   "OAuth token fetch failure threshold before liveness probe failure",
				Value:   health.DefaultUnauthorizedFailureThreshold,
			},
			&cli.StringFlag{
				Name:    "kubeconfig",
				EnvVars: []string{"KUBECONFIG"},
				Value:   "",
				Usage:   "'kubeconfig' is the KUBECONFIG path for backend K8s cluster",
			},
			&cli.DurationFlag{
				Name:    "heartbeat-interval",
				Usage:   "Heartbeat Interval should be Parseable time.Duration, 300s or 5m",
				EnvVars: []string{"ICMS_HEARTBEAT_INTERVAL"},
				Value:   DefaultHeartBeatInterval,
			},
			&cli.DurationFlag{
				Name:    "cred-renew-interval",
				Usage:   "Credentials Renewal Interval should be Parseable time.Duration, 300s or 5m",
				EnvVars: []string{"ICMS_CRED_RENEW_INTERVAL"},
				Value:   DefaultCredRenewInterval,
			},
			&cli.DurationFlag{
				Name:    "sync-queue-interval",
				Usage:   "Sync Queue Interval should be Parseable time.Duration, 300s or 5m",
				EnvVars: []string{"ICMS_SYNC_QUEUE_INTERVAL"},
				Value:   defaultSyncQueueInterval,
			},
			&cli.DurationFlag{
				Name:    "sync-request-status-interval",
				Usage:   "Sync Request Status Interval should be Parseable time.Duration, 300s or 5m",
				EnvVars: []string{"ICMS_SYNC_REQUEST_STATUS_INTERVAL"},
				Value:   DefaultSyncRequestStatusInterval,
			},
			&cli.DurationFlag{
				Name:        "model-cache-idle-period",
				Usage:       "Model Cache Idle Period should be Parseable time.Duration, 10m or 60m",
				EnvVars:     []string{"MODEL_CACHE_IDLE_PERIOD"},
				Value:       defaultTimeConfig.ModelCacheIdlePeriod,
				Destination: &timeConfig.ModelCacheIdlePeriod,
			},
			&cli.DurationFlag{
				Name:    "periodic-instance-status-interval",
				Usage:   "Frequency to send periodic instance status, even if the status has not changed",
				EnvVars: []string{"PERIODIC_INSTANCE_STATUS_INTERVAL"},
				Value:   DefaultPeriodicInstanceStatusInterval,
			},
			&cli.DurationFlag{
				Name:        "worker-degradation-timeout",
				Aliases:     []string{"worker-degradation-period"},
				Usage:       "Timeperiod for determining if a worker is degraded",
				EnvVars:     []string{"WORKER_DEGRADATION_PERIOD", "WORKER_DEGRADATION_TIMEOUT"},
				Value:       defaultTimeConfig.WorkerDegradationTimeout,
				Destination: &timeConfig.WorkerDegradationTimeout,
			},
			&cli.DurationFlag{
				Name:        "worker-startup-timeout",
				Usage:       "Timeperiod for determining if a worker is degraded",
				EnvVars:     []string{"WORKER_STARTUP_TIMEOUT"},
				Value:       defaultTimeConfig.WorkerStartupTimeout,
				Destination: &timeConfig.WorkerStartupTimeout,
			},
			&cli.DurationFlag{
				Name:        "max-running-timeout",
				Usage:       "Maximum Running Timeout for Instances",
				EnvVars:     []string{"MAX_RUNNING_TIMEOUT"},
				Value:       defaultTimeConfig.MaxRunningTimeout,
				Destination: &timeConfig.MaxRunningTimeout,
			},
			&cli.DurationFlag{
				Name:        "volume-attachment-timeout",
				Usage:       "Volume Attachment Timeout should be Parseable time.Duration, 10m or 60m",
				EnvVars:     []string{"VOLUME_ATTACHMENT_TIMEOUT"},
				Value:       defaultTimeConfig.ModelCacheVolumeDetachmentTimeout,
				Destination: &timeConfig.ModelCacheVolumeDetachmentTimeout,
			},
			&cli.DurationFlag{
				Name:    "nvca-rollover-service-update-interval",
				Usage:   "Function Instance Status Update interval for Rollover Service time.Duration, 10m or 60m",
				EnvVars: []string{"NVCA_ROLLOVER_SERVICE_UPDATE_INTERVAL"},
				Value:   DefaultRolloverServicesUpdateInterval,
			},
			&cli.DurationFlag{
				Name:    "icms-request-ack-interval",
				Usage:   "Frequency to send periodic ACKs for ICMS requests, for large model instances",
				EnvVars: []string{"ICMS_REQUEST_ACK_INTERVAL"},
				Value:   DefaultACKFrequencyInterval,
			},
			&cli.StringFlag{
				Name:  "listen",
				Value: ":8000",
				Usage: "Address and port for health check and metrics",
			},
			&cli.StringFlag{
				Name:  "listen-admin",
				Value: "127.0.0.1:8001",
				Usage: "Address and port for admin handler",
			},
			&cli.StringFlag{
				Name:  "listen-debug",
				Value: "127.0.0.1:8181",
				Usage: "Address and port for the debug server",
			},
			&cli.StringFlag{
				Name:  "compute-backend",
				Value: "k8s",
				Usage: "Compute Backend for NVCF Workers (k8s/bcp)",
			},
			&cli.UintFlag{
				Name:  "gpu-capacity",
				Usage: "Number of Discrete GPUs that can be allocated to k8s Pods in the cluster",
			},
			&cli.StringFlag{
				Name:    "endpoint-url",
				Value:   "http://localhost:4566",
				Usage:   "Queue Endpoint URL",
				EnvVars: []string{"ENDPOINT_URL"},
			},
			&cli.StringFlag{
				Name:  "log-level",
				Value: "debug",
				Usage: "Log level for bart",
			},
			&cli.StringFlag{
				Name:    "ngc-endpoint-url",
				Value:   "https://api.stg.ngc.nvidia.com",
				Usage:   "NGC Endpoint URL",
				EnvVars: []string{"NGC_ENDPOINT_URL"},
			},
			&cli.StringFlag{
				Name:    "ngc-org",
				Value:   "nvidia",
				Usage:   "NGC Org Name",
				EnvVars: []string{"NGC_ORG"},
			},
			&cli.StringFlag{
				Name:    "ngc-token-scope",
				Value:   "group/ngc-stg:nvidia",
				Usage:   "NGC Token Scope",
				EnvVars: []string{"NGC_TOKEN_SCOPE"},
			},
			&cli.StringFlag{
				Name:    "ngc-token-url",
				Value:   "https://stg.authn.nvidia.com/token",
				Usage:   "NGC Token URL",
				EnvVars: []string{"NGC_TOKEN_URL"},
			},
			&cli.StringFlag{
				Name:        "ngc-service-api-key",
				DefaultText: "*********",
				Usage:       "NGC Service API Key",
				EnvVars:     []string{"NGC_SERVICE_API_KEY", "NGC_CLI_API_KEY"},
			},
			&cli.StringFlag{
				Name:        "ngc-service-api-key-file",
				DefaultText: "*********",
				Usage:       "NGC Service API Key file path (will take precedence over --ngc-service-api-key)",
				EnvVars:     []string{"NGC_SERVICE_API_KEY_FILE"},
			},
			&cli.StringFlag{
				Name:    "otel-exporter",
				EnvVars: []string{"OTEL_EXPORTER"},
				Value:   string(nvcaconfig.NoExporter),
				Usage:   "The Open Telemetry exporter to use.",
			},
			&cli.StringFlag{
				Name:    "otel-lightstep-service-name",
				EnvVars: []string{"LS_SERVICE_NAME"},
				Usage:   "The service name for lightstep.",
			},
			&cli.StringFlag{
				Name:    "otel-lighstep-access-token",
				EnvVars: []string{"LS_ACCESS_TOKEN"},
				Usage:   "The access token for lightstep.",
			},
			&cli.StringFlag{
				Name:    "helm-repository-prefix",
				EnvVars: []string{"HELM_REPOSITORY_PREFIX"},
				Usage:   "Helm repository restriction enabler",
			},
			&cli.StringFlag{
				Name:    "helm-reval-service-url",
				Usage:   "Helm ReVal service URL",
				EnvVars: []string{"HELM_REVAL_SERVICE_URL"},
				Value:   "https://reval.nvcf.nvidia.com",
			},
			&cli.StringSliceFlag{
				Name:    "csi-volume-mount-options",
				Usage:   "Comma Separated Volume Mount Options to be used during ReadOnly PVC Provisioning for Model Cache",
				EnvVars: []string{"NVCA_CSI_VOLUME_MOUNT_OPTIONS"},
				Value:   cli.NewStringSlice(),
			},
			&cli.StringFlag{
				Name:    "function-deployment-stages-service-url",
				Usage:   "Function Deployment Stages service URL",
				EnvVars: []string{"FUNCTION_DEPLOYMENT_STAGES_SERVICE_URL"},
				Value:   "https://deployment-stages.nvcf.nvidia.com",
			},
			&cli.DurationFlag{
				Name:    "icms-request-ack-retry-timeout",
				Usage:   "ICMS Request Ack Retry Timeout",
				EnvVars: []string{ICMSRequestAckRetryTimeoutEnvVarKey},
				Value:   DefaultICMSRequestAckRetryTimeout,
			},
			&cli.StringFlag{
				Name:    "nvca-operator-version",
				Usage:   "NVCA Operator version",
				EnvVars: []string{"NVCA_OPERATOR_VERSION"},
				Value:   "",
			},
			&cli.BoolFlag{
				Name:    "skip-self-destruct",
				Usage:   "Skip self-destruct mode even if ICMS responds with SELF_DESTRUCT action",
				EnvVars: []string{"NVCA_SKIP_SELF_DESTRUCT"},
			},
			&cli.BoolFlag{
				Name:    "force-self-destruct",
				Usage:   "Force self-destruct mode for testing purposes (overrides ICMS response)",
				EnvVars: []string{"NVCA_FORCE_SELF_DESTRUCT"},
			},
			&cli.StringFlag{
				Name:        "image-credential-helper-image",
				Usage:       "Image reference for 'nvcf-image-credential-helper' to update third party registry pull secrets",
				EnvVars:     []string{"NVCA_IMAGE_CREDENTIAL_HELPER_IMAGE"},
				Destination: &aopts.ImageCredentialHelperImage,
			},
			&cli.StringFlag{
				Name:    "nvca-secret-mirror-source-namespace",
				Usage:   "Namespace to source secrets from for mirroring",
				EnvVars: []string{"NVCA_SECRET_MIRROR_SOURCE_NAMESPACE"},
				Value:   "nvca-operator",
			},
			&cli.StringFlag{
				Name:    "nvca-secret-mirror-label-selector",
				Usage:   "Label selector to filter which secrets to mirror",
				EnvVars: []string{"NVCA_SECRET_MIRROR_LABEL_SELECTOR"},
			},
			&cli.StringFlag{
				Name:    "self-hosted-vault-secrets-json-path",
				Aliases: []string{"self-managed-vault-secrets-json-path"},
				Usage:   "Path to the Vault secrets JSON file for NCP self-hosted NVCF installation",
				EnvVars: []string{"NVCF_SELF_HOSTED_VAULT_SECRETS_JSON_PATH", "NVCF_SELF_MANAGED_VAULT_SECRETS_JSON_PATH"},
				Value:   nvcaauth.DefaultSelfHostedVaultSecretsJSONPath,
			},
			&cli.StringFlag{
				Name:    "token-file-path",
				Usage:   "Path to the projected ServiceAccount token (PSAT) used by the agent for ICMS auth, NATS auth-callout, and ReVal token fetching. The operator templates this onto the agent container; expressed as a flag rather than env-only so it appears in --help and follows the standard CLI > env > default precedence.",
				EnvVars: []string{"NVCF_TOKEN_FILE_PATH"},
			},
			&cli.StringFlag{
				Name:    "identity-source",
				Usage:   "Identity source the agent runs under: 'psat' (default; projected SA token). Empty/unset is treated as 'psat'. Source-specific behaviors (e.g. JWKS pushing) key off this rather than on token-file-path so SPIRE-mode (when re-introduced) can share the same token path.",
				EnvVars: []string{"NVCF_IDENTITY_SOURCE"},
				Value:   "psat",
			},
			&cli.StringFlag{
				Name:    "function-env-overrides-b64",
				Usage:   "Base64 encoded JSON of environment variable overrides for function workloads",
				EnvVars: []string{"FUNCTION_ENV_OVERRIDES_B64"},
			},
			&cli.StringFlag{
				Name:    "task-env-overrides-b64",
				Usage:   "Base64 encoded JSON of environment variable overrides for task workloads",
				EnvVars: []string{"TASK_ENV_OVERRIDES_B64"},
			},
			&cli.DurationFlag{
				Name:    "gpu-poll-interval",
				Usage:   "Interval between GPU availability checks when GracefulNoGPU is enabled",
				EnvVars: []string{"NVCA_GPU_POLL_INTERVAL"},
				Value:   DefaultGPUPollInterval,
			},
			&cli.DurationFlag{
				Name:    "gpu-debounce-time",
				Usage:   "Time GPU state must be stable before triggering a change when GracefulNoGPU is enabled",
				EnvVars: []string{"NVCA_GPU_DEBOUNCE_TIME"},
				Value:   DefaultGPUDebounceTime,
			},
		},
		Action: func(c *cli.Context) error {
			ctx := c.Context
			log := core.GetLogger(ctx)
			err := core.SetLevel(log, c.String("log-level"))
			if err != nil {
				log.WithError(err).Error("failed to set log level")
				return err
			}

			// Move logs from all client-go logs into
			// the default logrus logger
			k8sLogger := logrusr.New(log, logrusr.WithReportCaller())
			ctrllog.SetLogger(k8sLogger)
			klog.SetLogger(k8sLogger)
			ctx = ctrllog.IntoContext(ctx, k8sLogger)

			nsLabels, err := labels.ConvertSelectorToLabelsMap(c.String("namespace-labels"))
			if err != nil {
				return err
			}

			// Allow overriding the Crowdstrike namespace via CLI flag
			k8sutil.CrowdstrikeNamespace = c.String("crowdstrike-namespace")

			timeConfig.Complete()

			cfg := nvcaconfig.Config{}
			if err := setDefaults(&cfg); err != nil {
				return err
			}
			cfg.Tracing.Exporter = nvcaconfig.OTELExporter(c.String("otel-exporter"))
			cfg.Tracing.LightstepServiceName = c.String("otel-lightstep-service-name")
			cfg.Tracing.LightstepAccessToken = c.String("otel-lighstep-access-token")

			cfg.Tracing.Exporter = nvcaconfig.OTELExporter(c.String("otel-exporter"))
			cfg.Tracing.LightstepServiceName = c.String("otel-lightstep-service-name")
			cfg.Tracing.LightstepAccessToken = c.String("otel-lighstep-access-token")
			opts := &AgentOptions{
				Config: cfg,
				TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
					SelfHostedEnabled:               featureflag.SelfHosted.Enabled(),
					TokenURL:                        c.String("oauth-icms-token-url"),
					OAuthClientID:                   c.String("oauth-client-id"),
					OAuthClientSecretKey:            c.String("oauth-client-secret-key"),
					OAuthClientSecretsEnvFile:       c.String("oauth-client-secrets-env-file"),
					OAuthTokenScope:                 V1NVCAOAuthScopes,
					OAuthPublicKeysetEndpoint:       c.String("oauth-public-keyset-endpoint"),
					OAuthTokenFetchFailureThreshold: c.Uint64("oauth-token-fetch-failure-threshold"),
					NGCServiceAPIKey:                c.String("ngc-service-api-key"),
					NGCServiceAPIKeyFile:            c.String("ngc-service-api-key-file"),
					SelfHostedVaultSecretsJSONPath:  c.String("self-hosted-vault-secrets-json-path"),
					PSATTokenFilePath:               c.String("token-file-path"),
				},
				IdentitySource:                      c.String("identity-source"),
				NCAId:                               c.String("nca-id"),
				ClusterName:                         c.String("cluster-name"),
				ClusterID:                           c.String("cluster-id"),
				ClusterRegion:                       c.String("cluster-region"),
				ClusterDescription:                  c.String("cluster-description"),
				ClusterGroupID:                      c.String("cluster-group-id"),
				ClusterGroupName:                    c.String("cluster-group-name"),
				ClusterAttributes:                   featureflag.GetEnabledAttributes(),
				CloudProvider:                       c.String("cloud-provider"),
				ICMSURL:                             c.String("icms-service-url"),
				SystemNamespace:                     c.String("system-namespace"),
				RequestsNamespace:                   c.String("requests-namespace"),
				NamespaceLabels:                     nsLabels,
				ComputeBackend:                      c.String("compute-backend"),
				KubeConfigPath:                      c.String("kubeconfig"),
				NVCASvcAddress:                      c.String("listen"),
				NVCAAdminAddr:                       c.String("listen-admin"),
				NVCADebugAddr:                       c.String("listen-debug"),
				HeartbeatInterval:                   c.Duration("heartbeat-interval"),
				CredRenewInterval:                   c.Duration("cred-renew-interval"),
				SyncQueueInterval:                   c.Duration("sync-queue-interval"),
				SyncRequestStatusInterval:           c.Duration("sync-request-status-interval"),
				PeriodicInstanceStatusInterval:      c.Duration("periodic-instance-status-interval"),
				RolloverServiceUpdateInterval:       c.Duration("nvca-rollover-service-update-interval"),
				EndpointURL:                         c.String("endpoint-url"),
				GPUCapacity:                         c.Uint64("gpu-capacity"),
				K8sVersion:                          c.String("k8s-version-override"),
				SharedStorageServerImage:            c.String("shared-storage-server-image"),
				HelmRepositoryPrefix:                c.String("helm-repository-prefix"),
				SyncAcknowledgeRequestInterval:      ackReqInterval,
				ICMSRequestACKInterval:              c.Duration("icms-request-ack-interval"),
				HelmReValServiceURL:                 c.String("helm-reval-service-url"),
				CSIVolumeMountOptions:               c.StringSlice("csi-volume-mount-options"),
				FunctionDeploymentStagesServiceURL:  c.String("function-deployment-stages-service-url"),
				LogPostingEnabled:                   featureflag.LogPosting.Enabled(),
				CachingSupportEnabled:               featureflag.CachingSupport.Enabled(),
				NVMeshEncryptionEnabled:             featureflag.NVMeshEncryption.Enabled(),
				HelmRBACEnforcementEnabled:          featureflag.HelmRBACEnforcement.Enabled(),
				HelmResourceConstraintsEnabled:      featureflag.HelmResourceConstraints.Enabled(),
				DynamicGPUDiscoveryEnabled:          featureflag.DynamicGPUDiscovery.Enabled(),
				MultipleGPUTypesAllowed:             featureflag.MultipleGPUTypesAllowed.Enabled(),
				PeriodicInstanceStatusUpdateEnabled: featureflag.PeriodicInstanceStatusUpdate.Enabled(),
				UniformInstanceLabelsEnabled:        featureflag.UniformInstanceLabels.Enabled(),
				AutoPurgeDegradedWorkers:            featureflag.AutoPurgeDegradedWorkers.Enabled(),
				ClusterTargetingEnabled:             featureflag.ClusterTargeting.Enabled(),
				HelmSharedStorageEnabled:            featureflag.HelmSharedStorage.Enabled(),
				GXCacheEnabled:                      featureflag.GXCache.Enabled(),
				LowLatencyStreamingEnabled:          featureflag.LowLatencyStreaming.Enabled(),
				PVCRebindEnabled:                    featureflag.PVCRebind.Enabled(),
				MultiNodeWorkloadsEnabled:           featureflag.MultiNodeWorkloads.Enabled(),
				FeatureFlagFetcher:                  featureflag.DefaultFetcher,
				RolloverServiceURL:                  c.String("nvca-rollover-service-url"),
				ICMSRequestAckRetryTimeout:          c.Duration("icms-request-ack-retry-timeout"),
				NVCAOperatorVersion:                 c.String("nvca-operator-version"),
				NVCAAgentVersion:                    version.ReleaseString(),
				K8sTimeConfig:                       timeConfig,
				SkipSelfDestruct:                    c.Bool("skip-self-destruct"),
				ForceSelfDestruct:                   c.Bool("force-self-destruct"),
				ImageCredentialHelperImage:          aopts.ImageCredentialHelperImage,
				SecretMirrorSourceNamespace:         c.String("nvca-secret-mirror-source-namespace"),
				SecretMirrorLabelSelector:           c.String("nvca-secret-mirror-label-selector"),
				FunctionEnvOverrides:                decodeEnvOverridesB64(c.String("function-env-overrides-b64")),
				TaskEnvOverrides:                    decodeEnvOverridesB64(c.String("task-env-overrides-b64")),
				GPUPollInterval:                     c.Duration("gpu-poll-interval"),
				GPUDebounceTime:                     c.Duration("gpu-debounce-time"),
			}

			if err := configureAndCheckCLI(log, opts); err != nil {
				return err
			}

			a, err := NewAgent(ctx, opts)
			if err != nil {
				return err
			}

			if err := a.Start(ctx); err != nil {
				log.WithError(err).Error("failed to start agent")
				return err
			}
			<-c.Context.Done()
			return nil
		},
	}
}

func configureAndCheckCLI(log *logrus.Entry, opts *AgentOptions) error {
	if opts.GPUCapacity != 0 {
		log.Info("Static GPU capacity is non-zero, disabling dynamic GPU discovery")
		opts.DynamicGPUDiscoveryEnabled = false
	}

	// Validate self-destruct flags
	if opts.SkipSelfDestruct && opts.ForceSelfDestruct {
		return fmt.Errorf("cannot enable both skip-self-destruct and force-self-destruct flags simultaneously")
	}

	if featureflag.AttrHostIsolation.Enabled() {
		// Non-CSP host isolation requires Kata VM's, which must be enforced at startup.
		// CSP host isolation VM's already exist as node "hosts".
		if opts.CloudProvider == "ON-PREM" &&
			!featureflag.AttrKataRuntimeIsolation.Enabled() {
			return fmt.Errorf("attribute %s must be enabled and cloud-provider is %s in host isolation mode",
				featureflag.AttrKataRuntimeIsolation.Key, opts.CloudProvider,
			)
		}
		// Cache ecryption must be turned on in host isolation mode.
		if featureflag.CachingSupport.Enabled() && (!featureflag.NVMeshEncryption.Enabled() || !featureflag.HelmPostRender.Enabled()) {
			return fmt.Errorf("feature flags %s and %s must be enabled when %s is enabled in host isolation mode",
				featureflag.NVMeshEncryption.Key, featureflag.HelmPostRender.Key, featureflag.CachingSupport.Key,
			)
		}
	}

	if featureflag.AttrKataRuntimeIsolation.Enabled() && featureflag.AttrPassthroughGPUEnabled.Enabled() {
		log.Warnf("Both %[1]s and %[2]s are enabled but %[2]s is a superset of %[1]s, so skipping enablement of %[2]s",
			featureflag.AttrKataRuntimeIsolation.Key, featureflag.AttrPassthroughGPUEnabled.Key,
		)
	}

	// Validate that CordonMaintenance and CordonAndDrainMaintenance are mutually exclusive
	if featureflag.CordonMaintenance.Enabled() && featureflag.CordonAndDrainMaintenance.Enabled() {
		return fmt.Errorf("feature flags %s and %s are mutually exclusive and cannot be enabled simultaneously",
			featureflag.CordonMaintenance.Key, featureflag.CordonAndDrainMaintenance.Key,
		)
	}

	// Set maintenance mode based on feature flags
	if featureflag.CordonMaintenance.Enabled() {
		opts.MaintenanceMode = types.MaintenanceModeCordon
		log.Infof("NVCA entering %s maintenance mode - creation tasks/functions will be cordoned (paused), terminations and heartbeats will continue", opts.MaintenanceMode)
	} else if featureflag.CordonAndDrainMaintenance.Enabled() {
		opts.MaintenanceMode = types.MaintenanceModeCordonAndDrain
		log.Infof("NVCA entering %s maintenance mode - creation tasks/functions will be cordoned (paused), existing workloads will be drained, terminations and heartbeats will continue", opts.MaintenanceMode)
	} else {
		opts.MaintenanceMode = types.MaintenanceModeNone
	}
	if cfgMMStr := opts.Config.Agent.MaintenanceMode.String(); cfgMMStr != "" && cfgMMStr != opts.MaintenanceMode.String() {
		return fmt.Errorf("config has maintenance mode %s but feature flags force mode to %s", cfgMMStr, opts.MaintenanceMode.String())
	}

	return nil
}

// decodeEnvOverridesB64 decodes a base64-encoded JSON map of environment variable overrides.
// Returns nil if the input is empty or if decoding fails.
func decodeEnvOverridesB64(b64 string) map[string]string {
	if b64 == "" {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil
	}
	var envOverrides map[string]string
	if err := json.Unmarshal(decoded, &envOverrides); err != nil {
		return nil
	}
	return envOverrides
}
