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

package operator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	nvidiaiov1 "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/apis/nvcf/v1"
	nvcaoperatorerrors "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/internal/errors"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/tracing"
	cmnsecret "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/secret"
	nvversion "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	cli "github.com/urfave/cli/v2"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/mirror"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/reconcile/clustermgmt"
	nvcaoptypes "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/operator/types"

	"github.com/bombsimon/logrusr/v4"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	klog "k8s.io/klog/v2"
)

var (
	NVCAOperatorNamespace      = nvcaoptypes.NVCAOperatorName
	NodeSelectorKey            = "node.kubernetes.io/instance-type"
	AppName                    = nvcaoptypes.NVCAOperatorName
	DefaultNVCARunAsUserID     = int64(1000770002)
	DefaultNVCARunAsGroupID    = int64(1000770002)
	DefaultClusterNetworkCIDRs = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/12"}
)

func NewOperatorCommand() *cli.Command {
	return &cli.Command{
		Name:  AppName,
		Usage: "NVCF ClusterAgent Operator",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "system-namespace",
				Value: NVCAOperatorNamespace,
				Usage: "Namespace where NVCA Operator will watch for NVCFBackend types",
			},
			&cli.StringFlag{
				Name:     "nca-id",
				Usage:    "NVIDIA Cloud Account Id (NCAId)",
				EnvVars:  []string{"NVIDIA_CLOUD_ACCOUNT_ID"},
				Required: true,
			},
			&cli.StringFlag{
				Name:    "cluster-id",
				EnvVars: []string{"NVCA_CLUSTER_ID"},
				Usage:   "NVCF Cluster ID for this NVCA instance to fetch and manage",
			},
			&cli.StringFlag{
				Name:    "nvca-image-repo",
				EnvVars: []string{"NVCA_IMAGE_REPO"},
				Usage:   "the repository to pull the NVCA container image from",
				Value:   defaultNVCAImageRepo,
			},
			&cli.StringFlag{
				Name:    "kubeconfig",
				EnvVars: []string{"KUBECONFIG"},
				Value:   "",
				Usage:   "'kubeconfig' is the KUBECONFIG path for backend K8s cluster",
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
				Name:  "listen-shutdown",
				Value: ":8002",
				Usage: "Address and port for shutdown handler (must be accessible by kubelet for preStop hook)",
			},
			&cli.StringFlag{
				Name:    "pod-name",
				EnvVars: []string{"POD_NAME"},
				Usage:   "Name of this pod (from Downward API)",
			},
			&cli.StringFlag{
				Name:    "pod-namespace",
				EnvVars: []string{"POD_NAMESPACE"},
				Usage:   "Namespace of this pod (from Downward API)",
			},
			&cli.StringFlag{
				Name:    "deployment-name",
				EnvVars: []string{"DEPLOYMENT_NAME"},
				Usage:   "Name of the deployment that owns this pod (for shutdown detection)",
			},
			&cli.StringFlag{
				Name:  "log-level",
				Value: "debug",
				Usage: "Log level for NVCA Operator",
			},
			&cli.StringFlag{
				Name:    "k8s-version-override",
				Value:   "",
				Usage:   "Backend K8s Version Override to report to ICMS",
				EnvVars: []string{"K8S_VERSION_OVERRIDE"},
			},
			&cli.StringFlag{
				Name:    "priority-class-name",
				Usage:   "Priority Class Name for NVCA pods",
				EnvVars: []string{"PRIORITY_CLASS_NAME"},
				Value:   "",
			},
			&cli.StringFlag{
				Name:     "cluster-name",
				Usage:    "Name of this edge K8s cluster (max 32 chars)",
				EnvVars:  []string{"CLUSTER_NAME"},
				Required: true,
			},
			&cli.StringFlag{
				Name:    "cluster-source",
				Usage:   "Source of cluster configuration (one of: ngc-managed, helm-managed)",
				EnvVars: []string{"NVCA_CLUSTER_SOURCE"},
				Value:   nvcaoptypes.ClusterSourceNGCManaged.String(),
			},
			&cli.StringFlag{
				Name:    "ngc-service-key-file",
				Value:   "/var/run/secrets/ngc-service-key/ngcServiceKey",
				Usage:   "NGC Service Key File for NGC API and registry usage",
				EnvVars: []string{"NGC_SERVICE_KEY_FILE"},
			},
			&cli.BoolFlag{
				Name:    "otel-enabled",
				EnvVars: []string{"OTEL_ENABLED"},
				Value:   false,
				Usage:   "Enable OpenTelemetry tracing.",
			},
			&cli.StringFlag{
				Name:    "otel-endpoint",
				EnvVars: []string{"OTEL_ENDPOINT"},
				Usage:   "OpenTelemetry OTLP exporter endpoint. Required when otel is enabled.",
			},
			&cli.StringFlag{
				Name:    "otel-service-name",
				EnvVars: []string{"LS_SERVICE_NAME"},
				Usage:   "OpenTelemetry service name.",
			},
			&cli.StringFlag{
				Name:    "otel-access-token",
				EnvVars: []string{"LS_ACCESS_TOKEN"},
				Usage:   "OpenTelemetry access token.",
			},
			&cli.StringFlag{
				Name:    "agent-resources-b64",
				EnvVars: []string{"AGENT_RESOURCES_B64"},
				Usage:   "base64 encoded JSON for agent container resource requirements",
			},
			&cli.StringFlag{
				Name:    "webhook-resources-b64",
				EnvVars: []string{"WEBHOOK_RESOURCES_B64"},
				Usage:   "base64 encoded JSON for webhook container resource requirements",
			},
			&cli.StringFlag{
				Name:  "node-selector-key",
				Value: NodeSelectorKey,
				Usage: "Node Selector key for Operator & NVCA Pod placement",
			},
			&cli.StringFlag{
				Name:  "node-selector-value",
				Value: "",
				Usage: "Node Selector Values for NVCA Operator & NVCA Pod placement",
			},
			&cli.StringFlag{
				Name:    "ngc-api-url",
				EnvVars: []string{"NGC_API_URL"},
				Usage:   "Root NGC API URL",
				Value:   clustermgmt.DefaultRootNGCAPIURL,
			},
			&cli.Int64Flag{
				Name:    "nvca-run-as-userid",
				EnvVars: []string{"NVCA_RUN_AS_USERID"},
				Usage:   "NVCA Run As User ID",
				Value:   DefaultNVCARunAsUserID,
			},
			&cli.Int64Flag{
				Name:    "nvca-run-as-groupid",
				EnvVars: []string{"NVCA_RUN_AS_GROUPID"},
				Usage:   "NVCA Run As Group ID",
				Value:   DefaultNVCARunAsGroupID,
			},
			&cli.DurationFlag{
				Name:    "nvca-cluster-api-refresh-interval",
				EnvVars: []string{"NVCA_CLUSTER_API_REFRESH_INTERVAL"},
				Usage:   "Interval to refresh the cluster from the NGC API",
				Value:   5 * time.Minute,
			},
			&cli.StringFlag{
				Name:    "nvca-gxcache-namespace",
				EnvVars: []string{"NVCA_GXCACHE_NAMESPACE"},
				Usage:   "Namespace for GXCache",
				Value:   clustermgmt.DefaultGXCacheNamespace,
			},
			&cli.StringFlag{
				Name:    "nvca-helm-repository-prefix",
				EnvVars: []string{"NVCA_HELM_REPO_PREFIX"},
				Usage:   "Helm charts prefix for gating in NVCA",
			},
			&cli.StringFlag{
				Name:    "nvca-crowdstrike-namespace",
				EnvVars: []string{"NVCA_CROWDSTRIKE_NAMESPACE"},
				Usage:   "Namespace for Crowdstrike Injector",
				Value:   clustermgmt.DefaultCrowdstrikeInjectorNamespace,
			},
			&cli.BoolFlag{
				Name:  "enable-gxcache",
				Usage: "Enable GXCache support in NVCA",
				Value: false,
			},
			&cli.StringSliceFlag{
				Name:    "ddcs-ip-allowlist",
				Usage:   "Comma Separated CIDR Allowlist for DDCS",
				EnvVars: []string{"DDCS_IP_ALLOWLIST"},
				Value:   cli.NewStringSlice(),
			},
			&cli.StringSliceFlag{
				Name:    "k8s-cluster-network-cidrs",
				Value:   cli.NewStringSlice(DefaultClusterNetworkCIDRs...),
				Usage:   "Comma-separated list of CIDR ranges for the cluster network",
				EnvVars: []string{"K8S_CLUSTER_NETWORK_CIDRS"},
			},
			&cli.BoolFlag{
				Name:    "nvca-cache-mount-options-enabled",
				Usage:   "Enable CSI volume mount options for NVCA caches",
				EnvVars: []string{"NVCA_CACHE_MOUNT_OPTIONS_ENABLED"},
				Value:   false,
			},
			&cli.StringFlag{
				Name:    "nvca-cache-mount-options",
				Usage:   "Comma-separated string of CSI volume mount options",
				EnvVars: []string{"NVCA_CACHE_MOUNT_OPTIONS"},
				Value:   "",
			},
			&cli.DurationFlag{
				Name:    "nvca-worker-degradation-period",
				Usage:   "Duration for determining if a worker is degraded (e.g., 90m, 1h30m)",
				EnvVars: []string{"NVCA_WORKER_DEGRADATION_PERIOD"},
				Value:   0,
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
				Value:   "",
			},
			&cli.BoolFlag{
				Name:    "generate-image-pull-secret",
				Usage:   "Generate an image pull secret for nvca Pods using the NGC service key",
				EnvVars: []string{"GENERATE_IMAGE_PULL_SECRET"},
				Value:   true,
			},
			&cli.StringFlag{
				Name:    "additional-image-pull-secrets-b64",
				Usage:   "base64 encoded JSON array of imagePullSecret objects with name field",
				EnvVars: []string{"ADDITIONAL_IMAGE_PULL_SECRETS_B64"},
			},
			&cli.StringFlag{
				Name:    "otel-collector-image-repo",
				EnvVars: []string{"OTEL_COLLECTOR_IMAGE_REPO"},
				Usage:   "OTel collector container image repository",
				Value:   defaultOTelCollectorImageRepo,
			},
			&cli.StringFlag{
				Name:    "otel-collector-image-tag",
				EnvVars: []string{"OTEL_COLLECTOR_IMAGE_TAG"},
				Usage:   "OTel collector container image tag",
				Value:   defaultOTelCollectorImageTag,
			},
			&cli.StringFlag{
				Name:    "otel-collector-resources-b64",
				EnvVars: []string{"OTEL_COLLECTOR_RESOURCES_B64"},
				Usage:   "Base64 encoded JSON of OTel collector container resource requirements",
			},
			&cli.BoolFlag{
				Name:    "otel-collector-enabled",
				EnvVars: []string{"OTEL_COLLECTOR_ENABLED"},
				Usage:   "Enable OTel collector sidecar for K8s event collection",
			},
			&cli.BoolFlag{
				Name:  "create-agent-config",
				Usage: "Generate an NVCA config file instead of using CLI args and env variables",
				Value: false,
			},
			&cli.StringFlag{
				Name:    "function-env-overrides-b64",
				EnvVars: []string{"FUNCTION_ENV_OVERRIDES_B64"},
				Usage:   "Base64 encoded JSON of environment variable overrides for function workloads",
			},
			&cli.StringFlag{
				Name:    "task-env-overrides-b64",
				EnvVars: []string{"TASK_ENV_OVERRIDES_B64"},
				Usage:   "Base64 encoded JSON of environment variable overrides for task workloads",
			},
			&cli.StringFlag{
				Name:    "workload-tolerations-b64",
				EnvVars: []string{"WORKLOAD_TOLERATIONS_B64"},
				Usage:   "Base64 encoded JSON array of workload pod tolerations",
			},
			&cli.StringFlag{
				Name:    "agent-tolerations-b64",
				EnvVars: []string{"AGENT_TOLERATIONS_B64"},
				Usage: "Base64 encoded JSON array of NVCA agent pod tolerations. " +
					"Fallback when the NVCFBackend CR's agentConfig.tolerations is empty (e.g. ngc-managed).",
			},
			&cli.StringFlag{
				Name:    "agent-override-env-vars-json-b64",
				Usage:   "base64 encoded JSON map of environment variables to override on the NVCA agent container",
				EnvVars: []string{"AGENT_OVERRIDE_ENV_VARS_JSON_B64"},
			},
			&cli.StringFlag{
				Name:    "vault-oauth-client-mount-path-template",
				Usage:   "Template for Vault OAuth client mount path with %s placeholder for clientID (e.g., nvidia/services/oauth/clients/%s/kv/secret)",
				EnvVars: []string{"VAULT_OAUTH_CLIENT_MOUNT_PATH_TEMPLATE"},
			},
			&cli.StringFlag{
				Name: "identity-source",
				Usage: "Identity source for self-hosted NVCA agents: 'psat' (projected SA token; default and currently the only supported value)." +
					" Empty/unset is treated as 'psat' for self-hosted clusters. Ignored for managed clusters." +
					" SPIRE is scaffolded in-tree but not yet supported end-to-end.",
				EnvVars: []string{"NVCA_IDENTITY_SOURCE"},
			},
		},
		Action: func(c *cli.Context) error {
			err := doAction(c)
			if err != nil {
				nvcaoperatorerrors.ExitReason(c.Context, err)
			}
			return err
		},
	}
}

func doAction(c *cli.Context) error {
	ctx := c.Context
	log := core.GetLogger(ctx)
	err := core.SetLevel(log, c.String("log-level"))
	if err != nil {
		return err
	}

	// Set logger to avoid logs going to stderr without
	// going through logrus
	klog.SetLogger(logrusr.New(log))

	clusterSource, err := nvcaoptypes.ValidateClusterSource(c.String("cluster-source"))
	if err != nil {
		return err
	}

	tokenFetcher, err := getDefaultNewTokenFetcher(ctx, c.String("ngc-service-key-file"), clusterSource)
	if err != nil {
		return err
	}

	// prepare resource requirements
	defAgentRR := getDefaultAgentRR()
	defWebhookRR := getDefaultWebhookRR()
	defOTelCollectorRR := getDefaultOTelCollectorRR()
	agentRR := decodeRR(c.String("agent-resources-b64"), defAgentRR)
	webhookRR := decodeRR(c.String("webhook-resources-b64"), defWebhookRR)
	otelCollectorRR := decodeRR(c.String("otel-collector-resources-b64"), defOTelCollectorRR)
	additionalSecrets, err := mirror.DecodeImagePullSecrets(c.String("additional-image-pull-secrets-b64"))
	if err != nil {
		return fmt.Errorf("failed to decode additional image pull secrets: %w", err)
	}
	workloadTolerations, err := decodeTolerations(c.String("workload-tolerations-b64"))
	if err != nil {
		return err
	}

	agentTolerations, err := decodeTolerations(c.String("agent-tolerations-b64"))
	if err != nil {
		return err
	}

	agentOverrideEnvVars, err := decodeEnvVarsMap(c.String("agent-override-env-vars-json-b64"))
	if err != nil {
		return err
	}

	vaultOAuthClientMountPathTemplate := c.String("vault-oauth-client-mount-path-template")
	if vaultOAuthClientMountPathTemplate == "" {
		log.Warn("VAULT_OAUTH_CLIENT_MOUNT_PATH_TEMPLATE not set, using default. " +
			"Set vaultConfig.oAuthClientMountPathTemplate in helm values.")
		//nolint:staticcheck
		vaultOAuthClientMountPathTemplate = clustermgmt.DefaultVaultOAuthClientMountPathTemplate
	}

	otelServiceName := c.String("otel-service-name")
	otelAccessToken := c.String("otel-access-token")
	otelEnabled := c.Bool("otel-enabled") || (otelServiceName != "" && otelAccessToken != "")

	if _, err := tracing.SetupOTELTracer(&tracing.OTELConfig{
		Enabled:     otelEnabled,
		Endpoint:    c.String("otel-endpoint"),
		AccessToken: otelAccessToken,
		Attributes: tracing.Attributes{
			ServiceName:    otelServiceName,
			ServiceVersion: nvversion.ReleaseString(),
		},
	}); err != nil {
		return fmt.Errorf("tracing setup: %w", err)
	}
	defer tracing.Shutdown()

	var nvcaOTELConfig *nvidiaiov1.OTELConfig
	if otelServiceName != "" && otelAccessToken != "" {
		nvcaOTELConfig = &nvidiaiov1.OTELConfig{
			Exporter:    string(DefaultOTELExporter),
			Endpoint:    c.String("otel-endpoint"),
			ServiceName: otelServiceName,
			AccessToken: otelAccessToken,
		}
	}

	// Resolve identity source for self-hosted clusters only.
	identitySource := c.String("identity-source")
	if clusterSource == nvcaoptypes.ClusterSourceSelfHosted {
		if identitySource, err = detectIdentitySource(identitySource); err != nil {
			return err
		}
	} else if identitySource != "" {
		return fmt.Errorf("identity-source is not supported for cluster source %s", clusterSource)
	}

	opts := &AgentOptions{
		NCAID:                             c.String("nca-id"),
		KubeConfigPath:                    c.String("kubeconfig"),
		K8sVersionOverride:                c.String("k8s-version-override"),
		SvcAddress:                        c.String("listen"),
		AdminAddr:                         c.String("listen-admin"),
		ShutdownAddr:                      c.String("listen-shutdown"),
		PodName:                           c.String("pod-name"),
		PodNamespace:                      c.String("pod-namespace"),
		DeploymentName:                    c.String("deployment-name"),
		SystemNamespace:                   c.String("system-namespace"),
		PriorityClassName:                 c.String("priority-class-name"),
		ClusterName:                       c.String("cluster-name"),
		ClusterSource:                     clusterSource,
		NodeSelectorKey:                   c.String("node-selector-key"),
		NodeSelectorValue:                 c.String("node-selector-value"),
		NVCAClusterManagementAPIURL:       c.String("ngc-api-url"),
		NVCFClusterID:                     c.String("cluster-id"),
		NVCAClusterAPIRefreshInterval:     c.Duration("nvca-cluster-api-refresh-interval"),
		NVCAImageRepo:                     c.String("nvca-image-repo"),
		NVCARunAsUserID:                   c.Int64("nvca-run-as-userid"),
		NVCARunAsGroupID:                  c.Int64("nvca-run-as-groupid"),
		GXCacheNamespace:                  c.String("nvca-gxcache-namespace"),
		HelmRepositoryPrefix:              c.String("nvca-helm-repository-prefix"),
		EnableGXCache:                     c.Bool("enable-gxcache"),
		DDCSIPAllowList:                   c.StringSlice("ddcs-ip-allowlist"),
		K8sClusterNetworkCIDRs:            c.StringSlice("k8s-cluster-network-cidrs"),
		AgentResources:                    agentRR,
		WebhookResources:                  webhookRR,
		NVCACacheMountOptionsEnabled:      c.Bool("nvca-cache-mount-options-enabled"),
		NVCACacheMountOptions:             c.String("nvca-cache-mount-options"),
		NVCAWorkerDegradationPeriod:       c.Duration("nvca-worker-degradation-period"),
		NVCAWorkloadTolerations:           workloadTolerations,
		NVCAAgentTolerations:              agentTolerations,
		NVCASecretMirrorSourceNamespace:   c.String("nvca-secret-mirror-source-namespace"),
		NVCASecretMirrorLabelSelector:     c.String("nvca-secret-mirror-label-selector"),
		GenerateImagePullSecret:           c.Bool("generate-image-pull-secret"),
		AdditionalImagePullSecrets:        additionalSecrets,
		TokenFetcher:                      tokenFetcher,
		OTelCollectorImageRepo:            c.String("otel-collector-image-repo"),
		OTelCollectorImageTag:             c.String("otel-collector-image-tag"),
		OTelCollectorEnabled:              c.Bool("otel-collector-enabled"),
		OTelCollectorResources:            otelCollectorRR,
		NVCAOTELConfig:                    nvcaOTELConfig,
		CreateAgentConfig:                 c.Bool("create-agent-config"),
		FunctionEnvOverridesB64:           c.String("function-env-overrides-b64"),
		TaskEnvOverridesB64:               c.String("task-env-overrides-b64"),
		AgentOverrideEnvVars:              agentOverrideEnvVars,
		VaultOAuthClientMountPathTemplate: vaultOAuthClientMountPathTemplate,
		IdentitySource:                    identitySource,
	}

	a, err := NewAgent(ctx, opts)
	if err != nil {
		return err
	}

	err = a.Start(ctx)
	if err != nil {
		return err
	}
	<-c.Context.Done() //nolint:staticcheck
	return nil
}

func getDefaultAgentRR() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1000m"),
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("200Mi"),
		},
	}
}

func getDefaultWebhookRR() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("200m"),
			corev1.ResourceMemory: resource.MustParse("200Mi"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("50m"),
			corev1.ResourceMemory: resource.MustParse("50Mi"),
		},
	}
}

func getDefaultOTelCollectorRR() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1000m"),
			corev1.ResourceMemory: resource.MustParse("1Gi"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("200m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
	}
}

// decodeRR decodes base64 JSON into ResourceRequirements; fallback to defRR on error.
func decodeRR(b64 string, defRR corev1.ResourceRequirements) corev1.ResourceRequirements {
	if b64 == "" {
		return defRR
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return defRR
	}
	var rr corev1.ResourceRequirements
	if err = json.Unmarshal(data, &rr); err != nil {
		return defRR
	}
	// ensure maps not nil
	if rr.Limits == nil && len(defRR.Limits) > 0 {
		rr.Limits = defRR.Limits
	}
	if rr.Requests == nil && len(defRR.Requests) > 0 {
		rr.Requests = defRR.Requests
	}
	return rr
}

// decodeEnvVarsMap decodes base64 JSON into a map of environment variables.
// Returns nil on empty input, or an error if decoding/parsing fails.
func decodeEnvVarsMap(b64 string) (map[string]string, error) {
	if b64 == "" {
		return nil, nil
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("failed to decode agent override env vars base64: %w", err)
	}
	var envVars map[string]string
	if err = json.Unmarshal(data, &envVars); err != nil {
		return nil, fmt.Errorf("failed to parse agent override env vars JSON: %w", err)
	}
	return envVars, nil
}

func decodeTolerations(b64 string) ([]corev1.Toleration, error) {
	if b64 == "" {
		return nil, nil
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("failed to decode tolerations base64: %w", err)
	}
	var tolerations []corev1.Toleration
	if err = json.Unmarshal(data, &tolerations); err != nil {
		return nil, fmt.Errorf("failed to parse tolerations JSON: %w", err)
	}
	return tolerations, nil
}

func getDefaultNewTokenFetcher(
	ctx context.Context, ngcServiceKeyFile string, clusterSource nvcaoptypes.ClusterSource,
) (cmnsecret.TokenFetcher, error) {
	if clusterSource == nvcaoptypes.ClusterSourceSelfHosted {
		// hand back empty token fetcher for self-hosted clusters
		return tokenFetcherAdapter(func(_ context.Context) (string, error) {
			return "", nil
		}), nil
	}

	if ngcServiceKeyFile != "" {
		return cmnsecret.NewKeyFileFetcher(ctx,
			cmnsecret.WithSecretKeyFile(ngcServiceKeyFile))
	}

	return nil, errors.New("NGC Service API Key File must be provided")
}

// tokenFetcher adapter for
type tokenFetcherAdapter func(ctx context.Context) (string, error)

func (t tokenFetcherAdapter) FetchToken(ctx context.Context) (string, error) {
	return t(ctx)
}
