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
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/core"
	nvcaconfig "github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/types/nvca/config"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/version"
	"github.com/bombsimon/logrusr/v4"
	"github.com/go-logr/logr"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	cli "github.com/urfave/cli/v2"
	"k8s.io/klog/v2"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	nvcaauth "github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/auth"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/cmdutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/internal/util/k8sutil"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/featureflag"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/storage"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvca/pkg/types"
)

func NewCobraCommand() *cobra.Command {
	return newCobraCommand(
		func(ctx context.Context, opts *AgentOptions) (cliAgent, error) {
			return NewAgent(ctx, opts)
		},
		func(gf cli.Generic, value string) error {
			return gf.Set(value)
		},
		func(log *logrus.Entry) logr.Logger {
			k8sLogger := logrusr.New(log, logrusr.WithReportCaller())
			ctrllog.SetLogger(k8sLogger)
			klog.SetLogger(k8sLogger)
			return k8sLogger
		},
	)
}

type cliAgent interface {
	Start(ctx context.Context) error
}

// newCobraCommand creates a new command with initializer funcs to mock in tests.
func newCobraCommand(
	newAgent func(ctx context.Context, opts *AgentOptions) (cliAgent, error),
	setFlag func(cli.Generic, string) error,
	initLogger func(log *logrus.Entry) logr.Logger,
) *cobra.Command {
	var (
		configFile     string
		tokenFilePath  string
		identitySource string
	)
	cmd := &cobra.Command{
		Use:     types.AppName,
		Short:   "NVIDIA Cluster Agent for NVIDIA Cloud Functions",
		Version: version.ReleaseString(),
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			ctx := cmd.Context()

			if configFile == "" {
				return fmt.Errorf("config file is required")
			}

			cfg, err := nvcaconfig.Init(configFile)
			if err != nil {
				return err
			}

			cfg = cfg.Complete()
			if err := setDefaults(&cfg); err != nil {
				return err
			}

			if cfg.Agent.ICMSURL == "" {
				return fmt.Errorf("agent.icmsURL is required")
			}

			log := core.GetLogger(ctx)
			if log.Logger.Level, err = logrus.ParseLevel(cfg.Agent.LogLevel); err != nil {
				return err
			}

			// Move logs from all client-go logs into
			// the default logrus logger
			k8sLogger := initLogger(log)
			ctx = ctrllog.IntoContext(ctx, k8sLogger)

			// Feature flag shim
			if err := setFlag(&featureflag.CLIFlag{}, strings.Join(cfg.Agent.FeatureFlags, ",")); err != nil {
				return fmt.Errorf("set featureflag CLI flag for config: %v", err)
			}
			// Cluster attributes shim
			if err := setFlag(&featureflag.AttrCLIFlag{}, strings.Join(cfg.Cluster.Attributes, ",")); err != nil {
				return fmt.Errorf("set attribute CLI flag for config: %v", err)
			}

			opts := &AgentOptions{
				Config: cfg,
				TokenFetcherOptions: nvcaauth.TokenFetcherOptions{
					TokenURL:                        cfg.Authz.TokenURL,
					OAuthClientID:                   cfg.Authz.ClientID,
					OAuthClientSecretKey:            cfg.Authz.ClientSecretKey,
					OAuthClientSecretsEnvFile:       cfg.Authz.ClientSecretsEnvFile,
					OAuthTokenScope:                 cfg.Authz.TokenScope,
					OAuthPublicKeysetEndpoint:       cfg.Authz.PublicKeysetEndpoint,
					OAuthTokenFetchFailureThreshold: cfg.Authz.TokenFetchFailureThreshold,
					NGCServiceAPIKey:                cfg.Authz.NGCServiceAPIKey,
					NGCServiceAPIKeyFile:            cfg.Authz.NGCServiceAPIKeyFile,
					SelfHostedEnabled:               featureflag.SelfHosted.Enabled(),
					SelfHostedVaultSecretsJSONPath:  cfg.Authz.SelfManagedVaultSecretsJSONPath,
					PSATTokenFilePath:               tokenFilePath,
				},
				IdentitySource:                          identitySource,
				NATSURL:                                 cfg.Agent.NATSURL,
				NCAId:                                   cfg.Cluster.NCAID,
				ClusterName:                             cfg.Cluster.Name,
				ClusterID:                               cfg.Cluster.ID,
				ClusterRegion:                           cfg.Cluster.Region,
				ClusterGroupID:                          cfg.Cluster.GroupID,
				ClusterGroupName:                        cfg.Cluster.GroupName,
				ClusterAttributes:                       featureflag.GetEnabledAttributes(),
				CloudProvider:                           cfg.Cluster.CloudProvider,
				ICMSURL:                                 cfg.Agent.ICMSURL,
				SystemNamespace:                         cfg.Agent.SystemNamespace,
				RequestsNamespace:                       cfg.Agent.RequestsNamespace,
				NamespaceLabels:                         cfg.Agent.NamespaceLabels,
				ComputeBackend:                          cfg.Agent.ComputeBackend,
				KubeConfigPath:                          cfg.Agent.KubeconfigPath,
				NVCASvcAddress:                          cfg.Agent.SvcAddress,
				NVCAAdminAddr:                           cfg.Agent.AdminAddr,
				NVCADebugAddr:                           cfg.Agent.DebugAddr,
				HeartbeatInterval:                       cfg.Agent.HeartbeatInterval,
				CredRenewInterval:                       cfg.Agent.CredRenewInterval,
				SyncQueueInterval:                       cfg.Agent.SyncQueueInterval,
				SyncRequestStatusInterval:               cfg.Agent.SyncRequestStatusInterval,
				PeriodicInstanceStatusInterval:          cfg.Agent.PeriodicInstanceStatusInterval,
				RolloverServiceUpdateInterval:           cfg.Agent.RolloverServiceUpdateInterval,
				ICMSRequestACKInterval:                  cfg.Agent.ICMSRequestAckInterval,
				GPUCapacity:                             cfg.Agent.StaticGPUCapacity,
				K8sVersion:                              cfg.Agent.KubernetesVersionOverride,
				SharedStorageServerImage:                cfg.Agent.SharedStorage.Server.Image,
				HelmRepositoryPrefix:                    cfg.Agent.HelmRepositoryPrefix,
				SyncAcknowledgeRequestInterval:          cfg.Agent.SyncAcknowledgeRequestInterval,
				HelmReValServiceURL:                     cfg.Agent.HelmReValServiceURL,
				HelmReValStageOAuthTokenURL:             cfg.Agent.HelmReValStageOAuthTokenURL,
				HelmReValStageOAuthPublicKeysetEndpoint: cfg.Agent.HelmReValStageOAuthPublicKeysetEndpoint,
				HelmReValProdOAuthTokenURL:              cfg.Agent.HelmReValProdOAuthTokenURL,
				HelmReValProdOAuthPublicKeysetEndpoint:  cfg.Agent.HelmReValProdOAuthPublicKeysetEndpoint,
				CSIVolumeMountOptions:                   cfg.Agent.CSIVolumeMountOptions,
				FunctionDeploymentStagesServiceURL:      cfg.Agent.FunctionDeploymentStagesServiceURL,
				FunctionDeploymentStagesStageOAuthTokenURL:             cfg.Agent.FunctionDeploymentStagesStageOAuthTokenURL,
				FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint: cfg.Agent.FunctionDeploymentStagesStageOAuthPublicKeysetEndpoint,
				FunctionDeploymentStagesProdOAuthTokenURL:              cfg.Agent.FunctionDeploymentStagesProdOAuthTokenURL,
				FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint:  cfg.Agent.FunctionDeploymentStagesProdOAuthPublicKeysetEndpoint,
				LogPostingEnabled:                             featureflag.LogPosting.Enabled(),
				CachingSupportEnabled:                         featureflag.CachingSupport.Enabled(),
				NVMeshEncryptionEnabled:                       featureflag.NVMeshEncryption.Enabled(),
				HelmRBACEnforcementEnabled:                    featureflag.HelmRBACEnforcement.Enabled(),
				HelmResourceConstraintsEnabled:                featureflag.HelmResourceConstraints.Enabled(),
				DynamicGPUDiscoveryEnabled:                    featureflag.DynamicGPUDiscovery.Enabled(),
				MultipleGPUTypesAllowed:                       featureflag.MultipleGPUTypesAllowed.Enabled(),
				PeriodicInstanceStatusUpdateEnabled:           featureflag.PeriodicInstanceStatusUpdate.Enabled(),
				UniformInstanceLabelsEnabled:                  featureflag.UniformInstanceLabels.Enabled(),
				AutoPurgeDegradedWorkers:                      featureflag.AutoPurgeDegradedWorkers.Enabled(),
				ClusterTargetingEnabled:                       featureflag.ClusterTargeting.Enabled(),
				HelmSharedStorageEnabled:                      featureflag.HelmSharedStorage.Enabled(),
				GXCacheEnabled:                                featureflag.GXCache.Enabled(),
				LowLatencyStreamingEnabled:                    featureflag.LowLatencyStreaming.Enabled(),
				PVCRebindEnabled:                              featureflag.PVCRebind.Enabled(),
				MultiNodeWorkloadsEnabled:                     featureflag.MultiNodeWorkloads.Enabled(),
				FeatureFlagFetcher:                            featureflag.DefaultFetcher,
				RolloverServiceURL:                            cfg.Agent.RolloverServiceURL,
				RolloverServiceStageOAuthTokenURL:             cfg.Agent.RolloverServiceStageOAuthTokenURL,
				RolloverServiceStageOAuthPublicKeysetEndpoint: cfg.Agent.RolloverServiceStageOAuthPublicKeysetEndpoint,
				RolloverServiceProdOAuthTokenURL:              cfg.Agent.RolloverServiceProdOAuthTokenURL,
				RolloverServiceProdOAuthPublicKeysetEndpoint:  cfg.Agent.RolloverServiceProdOAuthPublicKeysetEndpoint,
				ICMSRequestAckRetryTimeout:                    cfg.Agent.ICMSRequestAckRetryTimeout,
				NVCAOperatorVersion:                           cfg.Agent.OperatorVersion,
				NVCAAgentVersion:                              version.ReleaseString(),
				MaintenanceMode:                               types.MaintenanceMode(cfg.Agent.MaintenanceMode),
				SkipSelfDestruct:                              cfg.Agent.SkipSelfDestruct,
				ForceSelfDestruct:                             cfg.Agent.ForceSelfDestruct,
				ImageCredentialHelperImage:                    cfg.Agent.ImageCredentialHelperImage,
				SecretMirrorSourceNamespace:                   cfg.Agent.SecretMirrorSourceNamespace,
				SecretMirrorLabelSelector:                     cfg.Agent.SecretMirrorLabelSelector,
				K8sTimeConfig: &k8sutil.TimeConfig{
					MaxRunningTimeout:                         cfg.Workload.MaxRunningTimeout,
					ModelCacheIdlePeriod:                      cfg.Workload.ModelCacheIdlePeriod,
					ModelCacheIdleCleanupPeriod:               cfg.Workload.ModelCacheIdleCleanupPeriod,
					ModelCacheROPVCBindTimeGracePeriod:        cfg.Workload.ModelCacheROPVCBindTimeGracePeriod,
					ModelCacheVolumeDetachmentTimeout:         cfg.Workload.ModelCacheVolumeDetachmentTimeout,
					WorkerDegradationTimeout:                  cfg.Workload.WorkerDegradationTimeout,
					WorkerStartupTimeout:                      cfg.Workload.WorkerStartupTimeout,
					PodLaunchThresholdSecondsOnFailedRestarts: cfg.Workload.PodLaunchThresholdSecondsOnFailedRestarts,
					PodLaunchThresholdMinutesOnInitFailure:    cfg.Workload.PodLaunchThresholdMinutesOnInitFailure,
					PodScheduledThreshold:                     cfg.Workload.PodScheduledThreshold,
					InitCacheJobFailureThreshold:              cfg.Workload.InitCacheJobFailureThreshold,
					MaxImagePullErrorThreshold:                cfg.Workload.MaxImagePullErrorThreshold,
					NamespaceStuckTimeout:                     cfg.Workload.NamespaceStuckTimeout,
					FailingObjectsBackoffTimeout:              cfg.Workload.FailingObjectsBackoffTimeout,
					FailingObjectsBackoffRequeueInterval:      cfg.Workload.FailingObjectsBackoffRequeueInterval,
				},
				FunctionEnvOverrides: cfg.Workload.FunctionEnvOverrides,
				TaskEnvOverrides:     cfg.Workload.TaskEnvOverrides,
			}

			// The operator injects OTEL_EXPORTER via the otel-nvca-config secret, but Viper
			// auto-binding with prefix "nvca" doesn't map it to tracing.exporter.
			if opts.Config.Tracing.Exporter == nvcaconfig.NoExporter {
				if exp := os.Getenv("OTEL_EXPORTER"); exp != "" {
					opts.Config.Tracing.Exporter = nvcaconfig.OTELExporter(exp)
				}
			}

			if opts.Config.Tracing.Exporter == nvcaconfig.LightstepExporter {
				opts.Config.Tracing.LightstepAccessToken = os.Getenv("LS_ACCESS_TOKEN")
				if opts.Config.Tracing.LightstepAccessToken == "" {
					opts.Config.Tracing.LightstepAccessToken = os.Getenv("NVCA_AUTHZ_LIGHTSTEP_ACCESS_TOKEN")
				}
				if opts.Config.Tracing.LightstepServiceName == "" {
					opts.Config.Tracing.LightstepServiceName = os.Getenv("LS_SERVICE_NAME")
				}
				if opts.Config.Tracing.LightstepAccessToken == "" {
					log.Warn("Tracing is set to lightstep but LS_ACCESS_TOKEN is not set; spans will not be exported")
				}
			}

			opts.K8sTimeConfig.Complete()

			if err := configureAndCheckCLI(log, opts); err != nil {
				return err
			}

			a, err := newAgent(ctx, opts)
			if err != nil {
				return err
			}

			if err := a.Start(ctx); err != nil {
				log.WithError(err).Error("Failed to start agent")
				return err
			}
			<-ctx.Done()
			return nil
		},
	}

	cmd.PersistentFlags().StringVar(&configFile, "config", "", "Config file path")
	if err := cmd.MarkPersistentFlagRequired("config"); err != nil {
		panic(err)
	}

	// Identity-related plumbing. Bound here as proper Cobra flags (rather than
	// looked up via os.Getenv at runtime) so they appear in --help, follow the
	// standard CLI > env > default precedence, and are introspectable for
	// tests. The vendored nvcaconfig.AuthzConfig doesn't carry these yet; once
	// it does they should move under cfg.Authz / cfg.Cluster like their peers.
	cmd.PersistentFlags().StringVar(&tokenFilePath, "token-file-path", os.Getenv("NVCF_TOKEN_FILE_PATH"),
		"Path to the projected ServiceAccount token (PSAT) used by the agent for ICMS auth, NATS auth-callout, and ReVal token fetching")
	cmd.PersistentFlags().StringVar(&identitySource, "identity-source", defaultIdentitySource(),
		"Identity source the agent runs under: 'psat' (default; projected SA token). Empty/unset is treated as 'psat'")

	return cmd
}

// defaultIdentitySource resolves the env var fallback for --identity-source so
// the operator's existing env-based injection (NVCF_IDENTITY_SOURCE) continues
// to work for charts that haven't been updated to pass the flag explicitly.
func defaultIdentitySource() string {
	if v := os.Getenv("NVCF_IDENTITY_SOURCE"); v != "" {
		return v
	}
	return "psat"
}

func setDefaults(cfg *nvcaconfig.Config) error {
	cmdutil.SetEmptyValue(&cfg.Agent.RequestsNamespace, RequestsNamespace)
	cmdutil.SetEmptyValue(&cfg.Agent.SystemNamespace, types.DefaultICMSRequestNamespace)
	cmdutil.SetEmptyValue(&cfg.Agent.SvcAddress, ":8000")
	cmdutil.SetEmptyValue(&cfg.Agent.AdminAddr, "127.0.0.1:8001")
	cmdutil.SetEmptyValue(&cfg.Agent.DebugAddr, "127.0.0.1:8181")
	cmdutil.SetEmptyValue(&cfg.Agent.ComputeBackend, "k8s")
	cmdutil.SetEmptyValue(&cfg.Agent.SecretMirrorSourceNamespace, "nvca-operator")
	cmdutil.SelectEmptyOnEnv(cfg.Environment, &cfg.Agent.RolloverServiceURL, "https://stg.api.ros.nvidia.com", "https://api.ros.nvidia.com")
	cmdutil.SelectEmptyOnEnv(cfg.Environment, &cfg.Agent.HelmReValServiceURL, "https://reval.stg.nvcf.nvidia.com", "https://reval.nvcf.nvidia.com")
	cmdutil.SelectEmptyOnEnv(cfg.Environment, &cfg.Agent.FunctionDeploymentStagesServiceURL, "https://deployment-stages.stg.nvcf.nvidia.com", "https://deployment-stages.nvcf.nvidia.com")
	cmdutil.SetEmptyValue(&cfg.Agent.SharedStorage.Server.Image, storage.GetSharedStorageServerImage("", cfg.Environment))
	if err := k8sutil.SetConfigDefaultResources(cfg); err != nil {
		return err
	}
	cmdutil.SetEmptyValue(&cfg.Workload.DefaultStargateAddress, "llm-request-router.nvcf.svc.cluster.local:50071")
	return nil
}
