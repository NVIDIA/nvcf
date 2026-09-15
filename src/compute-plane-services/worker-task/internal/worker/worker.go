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

package worker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/servers"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/tracing"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/configs"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/health"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/progress"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/secrets"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/types"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-task/internal/upload"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metering"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/nvct"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvct"
	sharedTypes "github.com/NVIDIA/nvcf/src/libraries/go/worker/types"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
)

type NVCTWorker struct {
	config             configs.Config
	meteringConfig     *metering.Config
	infraMetering      *metering.InfraMetering
	nvctClient         *nvct.Client
	streamingClient    *nvct.StreamingClient
	progressMonitor    *progress.Monitor
	workerHealth       *health.HealthHandler
	server             servers.Server
	maxRunTimeDuration time.Duration
	cancelHeartbeat    context.CancelFunc
}

func NewNVCTWorker(ctx context.Context, zapLogger *logs.ZapLogger, config configs.Config) (*NVCTWorker, error) {
	_, err := uuid.Parse(config.TaskId)
	if err != nil {
		return nil, sharedTypes.NewInternalError(fmt.Errorf("invalid task id %s", config.TaskId))
	}

	if config.HealthPort <= 0 {
		config.HealthPort = configs.DefaultHealthPort
	}

	otelUrl, err := url.Parse(config.OTELExporterOTLPEndpoint)
	if err != nil {
		return nil, sharedTypes.NewInternalError(err)
	}

	workerHealth := health.New()

	// We don't hardcode values, instead expect them to be passed by newt or nvca
	// to both task and utils container env.
	if config.ResultsDir == "" && config.ResultHandlingStrategy == types.UPLOAD_STRATEGY {
		return nil, sharedTypes.NewInternalError(
			errors.New("failed to find results directory in utils environment"),
		)
	}

	if config.ProgressFilePath == "" {
		return nil, sharedTypes.NewInternalError(
			errors.New("failed to find progress file path in utils environment"),
		)
	}

	// To resolve instance type confusion between GFN and non GFN backends.
	// Will eventually use InstanceTypeName and deprecate InstanceType.
	if config.InstanceTypeName != "" {
		config.InstanceType = config.InstanceTypeName
	}

	if config.ICMSEnvironment == "" {
		config.ICMSEnvironment = config.SpotEnvironment
	}

	if config.SharedConfigDir == "" {
		config.SharedConfigDir = configs.DefaultSharedConfigDir
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
		Backend:                config.CloudProvider,
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
	}

	var maxRunTimeDuration time.Duration
	if config.MaxRunTime != "" {
		maxRunTimeDuration, err = utils.ConvertISO8601Duration(config.MaxRunTime)
		if err != nil {
			return nil, sharedTypes.NewUserActionableError(
				fmt.Errorf("invalid value of max time duration %s", config.MaxRunTime),
			)
		}
	}

	if config.TaskReadyTimeout == 0 {
		config.TaskReadyTimeout = configs.DefaultTaskReadyTimeout
	}

	if config.ResultHandlingStrategy == types.UNKNOWN_STRATEGY {
		return nil, sharedTypes.NewUserActionableError(
			errors.New("unsupported result handling strategy specified in config"),
		)
	}

	if config.PollProgressInterval == 0 {
		config.PollProgressInterval = configs.DefaultPollProgressInterval
	}

	if config.ProgressUpdateTimeout == 0 {
		config.ProgressUpdateTimeout = configs.DefaultProgressUpdateTimeout
	}

	w := &NVCTWorker{
		config:             config,
		meteringConfig:     &meteringConfig,
		workerHealth:       workerHealth,
		maxRunTimeDuration: maxRunTimeDuration,
		server: servers.NewGRPCServer(&servers.GRPCConfig{
			HTTPAddr:  "0.0.0.0:9190",
			GRPCAddr:  "0.0.0.0:9191",
			AdminAddr: "0.0.0.0:" + strconv.Itoa(config.HealthPort),
			BaseServerConfig: &servers.BaseServerConfig{
				ServiceName: "gdn-nvct-worker-service",
				Version:     utils.Version,
				Tracing: tracing.OTELConfig{
					Enabled:     true,
					Endpoint:    otelUrl.Host,
					Insecure:    otelUrl.Scheme == "http",
					AccessToken: config.TracingAccessToken,
					Attributes: tracing.Attributes{
						Extra: map[string]string{
							"task_id":       config.TaskId,
							"task_name":     config.TaskName,
							"host.provider": config.CloudProvider,
							"host.platform": config.CloudPlatform,
							"instance.type": config.InstanceType,
							"nca_id":        config.NcaId,
							"host.id":       config.InstanceId,
							"host.dc":       config.ZoneName,
							"gpu.type":      config.GpuType,
						},
					},
				},
			},
		},
			servers.WithLogger(zapLogger),
			// only running a healthcheck server
			servers.WithRegisterServer(func(*grpc.Server) {}),
			servers.WithHttpHealthEndpoints("/v1/health/live"),
			servers.WithAdditionalHTTPHandlers(map[string]http.Handler{"/v1/health/ready": workerHealth}),
		),
	}

	return w, nil
}

// Setup NVCT worker.
// Http server is disabled in unit tests.
func (w *NVCTWorker) Setup(withHttpServer bool) error {
	resultDirectory := w.config.ResultsDir
	if resultDirectory == "" {
		resultDirectory = filepath.Dir(w.config.ProgressFilePath)
	}

	// Using 0777 since the user will write to this directory
	_, err := os.Stat(resultDirectory)
	if errors.Is(err, os.ErrNotExist) {
		err = utils.CreateDirectory(resultDirectory, os.FileMode(0777))
		if err != nil {
			return sharedTypes.NewInternalError(err)
		}
	} else if err != nil {
		return sharedTypes.NewInternalError(err)
	}

	if err := os.Chmod(resultDirectory, 0777); err != nil {
		return sharedTypes.NewInternalError(err)
	}

	if withHttpServer {
		if err := w.server.Setup(); err != nil {
			return sharedTypes.NewInternalError(err)
		}
	}

	return nil
}

// ------------------------------------------------------------------------

// Run NVCT worker.
// Http server is disabled in unit tests.
func (w *NVCTWorker) Run(ctx context.Context, withHttpServer bool) error {
	zap.L().Info("Starting nvct worker",
		utils.PublicLogMarker,
		zap.String("task id", w.config.TaskId),
		zap.String("instance id", w.config.InstanceId),
		zap.String("max runtime", w.config.MaxRunTime),
		zap.String("version", utils.Version),
		zap.String("termination grace period", w.config.TerminationGracePeriod),
	)

	if withHttpServer {
		serverCloseErrPattern := regexp.MustCompile(`^received signal (interrupt|terminated)`)
		go func() {
			err := w.server.Run()
			if err != nil && !serverCloseErrPattern.MatchString(strings.ToLower(err.Error())) {
				err = sharedTypes.NewInternalError(err)
				utils.ExitReason(err)
				zap.S().Panic(err)
			}
		}()
	}

	infraMetering := metering.NewInfraMetering(ctx, metering.Task, w.meteringConfig, metering.InfraInitializing)
	defer utils.Close(infraMetering.Close)
	w.infraMetering = infraMetering

	nvctClient, err := nvct.CreateClient(
		w.config.NVCTFqdnGrpc,
		w.config.NVCTWorkerToken,
		w.config.InstanceId,
		w.config.TaskId,
		w.config.InstanceType,
		nvct.DefaultNvctClientTimeout,
		w.config.SharedConfigDir,
	)
	if err != nil {
		return sharedTypes.NewInternalError(err)
	}
	w.nvctClient = nvctClient

	// Start metrics exporter
	if err = metrics.Initialize(metrics.NvctRootNamespace, nil); err != nil {
		return sharedTypes.NewInternalError(err)
	}

	// Start worker token refreshment
	w.nvctClient.StartWorkerTokenRefresher(ctx, true)

	// Start ess agent assertion token refreshment
	if w.config.SecretsAssertionToken != "" {
		tokenDir := w.config.EssAgentConfigDir
		if tokenDir == "" {
			tokenDir = configs.DefaultEssAgentConfigDir
		}
		tokenPath := filepath.Join(tokenDir, configs.EssTokenFileName)
		w.nvctClient.StartAssertionTokenRefresher(ctx, tokenPath, true)
	} else {
		zap.L().Info("Skip refreshing ESS assertion token")
	}

	streamingClient := nvct.NewStreamingClient(
		ctx,
		w.nvctClient.Client,
		w.nvctClient.NvctTokenProvider,
	)
	w.streamingClient = streamingClient
	defer utils.ClosePreservingError(&err, streamingClient)

	monitor := progress.NewProgressMonitor(
		w.config.TaskId,
		w.config.InstanceId,
		w.config.ResultsDir,
		w.config.InstanceType,
		w.config.ProgressFilePath,
		w.config.PollProgress,
		w.config.PollProgressInterval,
		w.config.ProgressUpdateTimeout,
		w.config.ResultHandlingStrategy,
		streamingClient.SendResult,
	)
	w.progressMonitor = monitor

	return w.workSession(ctx)
}

func (w *NVCTWorker) workSession(ctx context.Context) error {
	// Connect to NVCT server
	zap.L().Info("NVCT FQDN", zap.String("address", w.config.NVCTFqdnGrpc))
	if err := w.nvctClient.Connect(ctx); err != nil {
		return sharedTypes.NewInternalError(fmt.Errorf("failed to connect to nvct server: %w", err))
	}

	// Start sending heartbeat to NVCT.
	// The initial status is TASK_CONTAINER_INITIALIZING and
	// will be changed to RUNNING after the task container creates the progress file.
	heartbeatCtx, cancelHeartbeat := context.WithCancel(ctx)
	w.cancelHeartbeat = cancelHeartbeat
	go w.workerHealth.StartHeartbeat(heartbeatCtx, w.nvctClient)

	// Create an upload client for uploading results
	var uploadClient *upload.NgcUploader
	if w.config.ResultHandlingStrategy == types.UPLOAD_STRATEGY {
		// Results are uploaded to NGC under the UPLOAD strategy in both managed
		// and self-hosted deployments, using the caller-supplied API key. The
		// stage environment routes to the NGC staging endpoint.
		ngcBaseUrl := configs.NgcBaseUrl
		if strings.ToLower(w.config.ICMSEnvironment) == "stage" {
			ngcBaseUrl = configs.NgcBaseUrlStg
		}

		workerSecrets, err := secrets.New(ctx, configs.DefaultSecretsFilePath)
		if err != nil {
			w.handleTaskError(ctx, types.ErrSecretSetup)
			return sharedTypes.NewInternalError(err)
		}

		if workerSecrets.NgcApiKey() == "" {
			w.handleTaskError(ctx, types.ErrMissingSecret)
			return sharedTypes.NewInternalError(types.ErrMissingSecret)
		}

		uploadClient, err = upload.NewUploader(ctx, w.config.ResultsLocation, ngcBaseUrl, workerSecrets)
		if err != nil {
			w.handleTaskError(ctx, types.ErrInternal)
			return sharedTypes.NewInternalError(err)
		}
		w.progressMonitor.UploadClient = uploadClient
	}

	// Wait for task container to be ready
	if err := w.waitForTaskReady(ctx, w.config.TaskReadyTimeout); err != nil {
		zap.L().Error(types.ErrTaskNotReady.Error(), zap.Error(err))
		w.handleTaskError(ctx, types.ErrTaskNotReady)
		return sharedTypes.NewUserActionableError(types.ErrTaskNotReady)
	}
	zap.L().Info("Task is running")

	// Mark utils container as ready
	w.workerHealth.SetExecutionStatus(pb.ExecutionStatus_RUNNING)
	w.infraMetering.SetStatus(metering.InfraReady)

	//Start timer when task actually starts running
	var runCtx context.Context
	var cancel context.CancelFunc
	if w.maxRunTimeDuration > 0 {
		runCtx, cancel = context.WithTimeout(ctx, w.maxRunTimeDuration)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	// Start progress monitor
	if err := w.progressMonitor.Start(runCtx); err != nil {
		return sharedTypes.NewInternalError(fmt.Errorf("failed to start progress monitor: %w", err))
	}
	defer utils.Close(w.progressMonitor.Stop)

	for {
		select {
		case <-runCtx.Done():
			if errors.Is(runCtx.Err(), context.Canceled) {
				zap.L().Warn(types.ErrWorkerTerminatedUngracefully.Error())
				if w.config.ResultHandlingStrategy == types.UPLOAD_STRATEGY {
					if uploadErr := w.progressMonitor.UploadClient.Wait(); uploadErr != nil {
						zap.L().Error("failed to finish uploads before terminating worker", zap.Error(uploadErr))
					}
				}
				return sharedTypes.NewInternalError(types.ErrWorkerTerminatedUngracefully)
			}

			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				taskErr := types.ErrMaxRunTimeDurationExceeded
				zap.L().Error(taskErr.Error(), zap.Error(runCtx.Err()))
				w.cancelHeartbeat()
				if sendErr := w.nvctClient.SendMaxRuntimeExceededHeartbeat(ctx, taskErr.Error()); sendErr != nil {
					zap.L().Error("failed to send heartbeat", zap.Error(sendErr))
				}
				return sharedTypes.NewUserActionableError(taskErr)
			}

			return sharedTypes.NewInternalError(runCtx.Err())

		case err := <-w.progressMonitor.TaskCompleted:
			defer close(w.progressMonitor.TaskCompleted)
			if err == nil {
				zap.L().Info("task completed, shutting down", zap.String("task id", w.config.TaskId))
				return nil
			}

			w.handleTaskError(ctx, err)
			return err
		}
	}
}

func (w *NVCTWorker) PreStopCheck(ctx context.Context) error {
	zap.L().Info("Received termination signal, checking progress file")
	progressData, err := progress.ParseProgressFile(w.config.ProgressFilePath)
	if err != nil {
		w.handleWorkerTermination(ctx)
		return err
	}

	zap.L().Info("Checking task execution status")
	executionStatus, err := w.nvctClient.SendInProgressHeartbeat(ctx, pb.ExecutionStatus_RUNNING)
	if err != nil {
		zap.L().Warn("failed to get latest task execution status")
		executionStatus = pb.ExecutionStatus_RUNNING.String()
	}

	// Trigger graceful termination when:
	// 1. Task container exits with progress reaches 100.
	// 2. Task is cancelled by users.
	// In other cases, utils container will exit with error immediately.
	if progressData.PercentComplete != 100 && executionStatus != pb.ExecutionStatus_CANCELED.String() {
		w.handleWorkerTermination(ctx)
		return types.ErrTaskNotComplete
	}

	return nil
}

// Catch task execution errors and report back to NVCT
func (w *NVCTWorker) handleTaskError(ctx context.Context, taskErr error) {
	zap.L().Info("task failed with errors", zap.String("task id", w.config.TaskId), zap.Error(taskErr))
	if w.cancelHeartbeat != nil {
		w.cancelHeartbeat()
	}

	if sendErr := w.nvctClient.SendErrorHeartbeat(ctx, taskErr.Error()); sendErr != nil {
		zap.L().Error("failed to send heartbeat", zap.Error(sendErr))
	}

	if w.progressMonitor != nil {
		w.progressMonitor.SendErrorProgressMessage(taskErr.Error())
	}
}

func (w *NVCTWorker) handleWorkerTermination(ctx context.Context) {
	if w.cancelHeartbeat != nil {
		w.cancelHeartbeat()
	}

	if sendErr := w.nvctClient.SendWorkerTerminatedHeartbeat(ctx); sendErr != nil {
		zap.L().Error("failed to send heartbeat", zap.Error(sendErr))
	}
}

// Check if task container is started from progress file.
func (w *NVCTWorker) waitForTaskReady(ctx context.Context, timeout time.Duration) error {
	if _, err := os.Stat(w.config.ProgressFilePath); err == nil {
		return nil
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := os.Stat(w.config.ProgressFilePath); err == nil {
				return nil
			} else if os.IsNotExist(err) {
				continue
			} else {
				return err
			}
		}
	}
}
