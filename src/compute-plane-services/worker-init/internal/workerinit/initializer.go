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

package workerinit

import (
	"context"
	"net/url"
	"path/filepath"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/tracing"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-init/configs"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-init/internal/downloader"
	initMetrics "github.com/NVIDIA/nvcf/src/compute-plane-services/worker-init/internal/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-init/pkg/ess"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metering"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/types"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
)

type baseInitializer struct {
	config             configs.InitConfig
	meteringConfg      *metering.Config
	modelDownloader    *downloader.ArtifactsDownloader
	resourceDownloader *downloader.ArtifactsDownloader
}

type Initializer interface {
	Setup() error
	Run(context.Context) error
}

func NewInitializer(config configs.InitConfig) (Initializer, error) {
	isNvctInit := isNVCTInit(config)
	if config.ModelRepo == "" {
		config.ModelRepo = configs.DefaultModelsRepo
	}

	if config.ResourceRepo == "" {
		config.ResourceRepo = configs.DefaultResourcesRepo
	}

	if config.EssAgentConfigDir == "" {
		config.EssAgentConfigDir = configs.DefaultEssAgentConfigDir
	}

	if config.ConcurrentDownloads == 0 {
		config.ConcurrentDownloads = configs.DefaultConcurrentDownloads
	}

	if config.ConcurrentChunks == 0 {
		config.ConcurrentChunks = configs.DefaultConcurrentChunks
	}

	if config.ChunkSize == 0 {
		config.ChunkSize = configs.DefaultChunkSize
	}

	if config.SharedConfigDir == "" {
		config.SharedConfigDir = configs.DefaultSharedConfigDir
	}

	if config.ICMSEnvironment == "" {
		config.ICMSEnvironment = config.SpotEnvironment
	}

	otelUrl, err := url.Parse(config.OTELExporterOTLPEndpoint)
	if err != nil {
		return nil, err
	}

	serviceName := "gdn-nvcf-worker-service"
	if isNvctInit {
		serviceName = "gdn-nvct-worker-service"
		// To resolve instance type confusion between GFN and non GFN backends.
		// Will eventually use InstanceTypeName and deprecate InstanceType.
		if config.InstanceTypeName != "" {
			config.InstanceType = config.InstanceTypeName
		}
	}
	tracingConfig := tracing.OTELConfig{
		Enabled:     true,
		Endpoint:    otelUrl.Host,
		Insecure:    otelUrl.Scheme == "http",
		AccessToken: config.TracingAccessToken,
		Attributes: tracing.Attributes{
			ServiceName:    serviceName,
			ServiceVersion: utils.Version,
			Extra: map[string]string{
				"host.provider": config.CloudProvider,
				"host.platform": config.CloudPlatform,
				"instance.type": config.InstanceType,
				"nca_id":        config.NcaId,
				"host.id":       config.InstanceId,
				"host.dc":       config.ZoneName,
				"gpu.type":      config.GpuType,
			},
		},
	}
	if isNvctInit {
		tracingConfig.Attributes.Extra["task_id"] = config.TaskId
		tracingConfig.Attributes.Extra["task_name"] = config.TaskName
	} else {
		tracingConfig.Attributes.Extra["function_id"] = config.FunctionId
		tracingConfig.Attributes.Extra["function_version_id"] = config.FunctionVersionId
		tracingConfig.Attributes.Extra["function_name"] = config.FunctionName
	}
	_, err = tracing.SetupOTELTracer(&tracingConfig)
	if err != nil {
		return nil, err
	}

	infraMeteringHeartbeatInterval := config.InfraMeteringHeartbeatInterval
	if infraMeteringHeartbeatInterval == 0 {
		infraMeteringHeartbeatInterval = metering.DefaultInfraMeteringHeartbeatInterval
	}
	billingNcaId := config.BillingNcaId
	if billingNcaId == "" {
		billingNcaId = config.NcaId
	}
	backend := config.CloudProvider
	if backend == "NGN" {
		backend = "GFN"
	}
	meteringConfig := metering.Config{
		Backend:                backend,
		NcaId:                  config.NcaId,
		BillingNcaId:           billingNcaId,
		NspectId:               metering.NspectIdFromEnv(),
		InstanceId:             config.InstanceId,
		InstanceType:           config.InstanceType,
		ICMSEnvironment:        config.ICMSEnvironment,
		ZoneName:               config.ZoneName,
		StartupTime:            time.Now(),
		InfraHeartbeatInterval: infraMeteringHeartbeatInterval,
		GpuCount:               config.GpuCount,
		GpuType:                config.GpuType,
		TaskId:                 config.TaskId,
		TaskTags:               config.TaskTags,
		FunctionId:             config.FunctionId,
		FunctionVersionId:      config.FunctionVersionId,
		FunctionTags:           config.FunctionTags,
	}
	base := baseInitializer{
		config:        config,
		meteringConfg: &meteringConfig,
		modelDownloader: downloader.NewArtifactDownloader(
			config.ModelRepo,
			config.ConcurrentDownloads,
			config.ConcurrentChunks,
			config.ChunkSize,
		),
		resourceDownloader: downloader.NewArtifactDownloader(
			config.ResourceRepo,
			config.ConcurrentDownloads,
			config.ConcurrentChunks,
			config.ChunkSize,
		),
	}
	if isNvctInit {
		return &NvctInitializer{baseInitializer: base}, nil
	}
	return &NvcfInitializer{baseInitializer: base}, nil
}

func (in *baseInitializer) startAssertionTokenRefresher(ctx context.Context, f func(context.Context, string, bool)) {
	if in.config.SecretsAssertionToken != "" {
		tokenPath := filepath.Join(in.config.EssAgentConfigDir, ess.EssTokenFileName)
		f(ctx, tokenPath, true)
	} else {
		zap.L().Info("Skip refreshing ESS assertion token")
	}
}

func (in *baseInitializer) downloadArtifacts(ctx context.Context, getArtifactsList func(ctx context.Context) (*types.ArtifactsList, error)) error {
	initMetrics.WorkerInitThreadCounter.Add(float64(in.config.ConcurrentDownloads))
	logger := zap.L().With(utils.PublicLogMarker)
	logger.Info("Fetch artifacts list")
	artifacts, err := getArtifactsList(ctx)
	if err != nil {
		return err
	}
	logArtifactsForDebug("models", in.config.ModelRepo, artifacts.Models)
	logArtifactsForDebug("resources", in.config.ResourceRepo, artifacts.Resources)
	if len(artifacts.Models) > 0 {
		logger.Info("Starting to download and install models")
		if err = in.modelDownloader.DownloadArtifacts(ctx, artifacts.Models); err != nil {
			return err
		}
		logger.Info("Finished downloading and installing models")
	}
	if len(artifacts.Resources) > 0 {
		logger.Info("Starting to download and install resources")
		if err = in.resourceDownloader.DownloadArtifacts(ctx, artifacts.Resources); err != nil {
			return err
		}
		logger.Info("Finished downloading and installing resources")
	}
	return nil
}

func isNVCTInit(config configs.InitConfig) bool {
	return config.NvcfConfig.NvcfWorkerToken == ""
}

func logArtifactsForDebug(kind, baseRepo string, artifacts []types.Artifact) {
	if len(artifacts) == 0 {
		zap.L().Debug("No artifacts to process", zap.String("kind", kind))
		return
	}

	zap.L().Debug(
		"Artifacts list details",
		zap.String("kind", kind),
		zap.String("base_repo", baseRepo),
		zap.Int("count", len(artifacts)),
	)
	for _, artifact := range artifacts {
		zap.L().Debug(
			"Artifact entry",
			zap.String("kind", kind),
			zap.String("name", artifact.Name),
			zap.String("version", artifact.Version),
			zap.String("path", artifact.Path),
		)
		// Pre-signed URLs (S3/GCS/NGC) carry credentials in the query
		// (X-Amz-Signature, token, ...) and occasionally in userinfo.
		// Strip both, and never fall back to the raw URL on parse error.
		redactedURL := "REDACTED"
		if u, err := url.Parse(artifact.Url); err == nil {
			u.User = nil
			if u.RawQuery != "" {
				u.RawQuery = "REDACTED"
			}
			redactedURL = u.String()
		}
		zap.L().Debug(
			"Artifact entry URL",
			zap.String("kind", kind),
			zap.String("name", artifact.Name),
			zap.String("path", artifact.Path),
			zap.String("url", redactedURL),
		)
	}
}
