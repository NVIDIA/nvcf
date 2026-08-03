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
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/clients"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/logs"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/servers"
	"github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/nvkit/tracing"
	"github.com/cenkalti/backoff/v4"
	"github.com/google/uuid"
	"github.com/hellofresh/health-go/v5"
	"github.com/nats-io/nats.go"
	"github.com/panjf2000/ants/v2"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-utils/worker/httpstream"
	utilsMetrics "github.com/NVIDIA/nvcf/src/compute-plane-services/worker-utils/worker/metrics"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/worker-utils/worker/progress"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/auth"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/consumer"
	infHealth "github.com/NVIDIA/nvcf/src/libraries/go/worker/health"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metering"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics"
	nvcfMetrics "github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proxy"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/types"
	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
)

const defaultBaseInferenceDomain = "127.0.0.1"
const defaultInferenceReadyTimeout = time.Duration(30) * time.Minute
const defaultEssAgentConfigDir = "/config/ess-agent"
const defaultSharedConfigDir = "/config/shared"

// EssTokenFileName is the filename the ESS agent writes the JWT assertion
// token to. Kept local to avoid a cross-service module dependency on
// worker-init (which owns the ESS agent that produces this file).
const EssTokenFileName = "jwt.token"

type Config struct {
	NVCFWorkerToken                     string        `mapstructure:"NVCF_WORKER_TOKEN"`
	NVCFFqdn                            string        `mapstructure:"NVCF_FQDN_GRPC"`
	NVCFFqdnNATS                        string        `mapstructure:"NVCF_FQDN_NATS"`
	NKeySeed                            *string       `mapstructure:"NKEY_SEED"` // will use the worker token if not set
	InferencePort                       int           `mapstructure:"INFERENCE_PORT"`
	InferenceHealthEndpoint             string        `mapstructure:"INFERENCE_HEALTH_ENDPOINT"`
	InferenceHealthProtocol             string        `mapstructure:"INFERENCE_HEALTH_PROTOCOL"`
	InferenceHealthTimeout              time.Duration `mapstructure:"INFERENCE_HEALTH_TIMEOUT"`
	InferenceHealthPort                 int           `mapstructure:"INFERENCE_HEALTH_PORT"`
	InferenceHealthExpectedResponseCode int           `mapstructure:"INFERENCE_HEALTH_EXPECTED_RESPONSE_CODE"`
	HelmChartInferenceServiceName       string        `mapstructure:"HELM_CHART_INFERENCE_SERVICE_NAME"`
	HelmChartNamespace                  string        `mapstructure:"HELM_CHART_NAMESPACE"`
	// The time.Duration unmarshal converts from a string, so suffixes are expected (ms, s, etc).
	// Because it can vary, we won't set a unit on the variable name.
	InferenceReadyTimeout          time.Duration `mapstructure:"INFERENCE_READY_TIMEOUT"`
	InfraMeteringHeartbeatInterval time.Duration `mapstructure:"INFRA_METERING_HEARTBEAT_INTERVAL_SECS"`
	OTELExporterOTLPEndpoint       string        `mapstructure:"OTEL_EXPORTER_OTLP_ENDPOINT"`
	TracingAccessToken             string        `mapstructure:"TRACING_ACCESS_TOKEN"`
	NcaId                          string        `mapstructure:"NCA_ID"`
	BillingNcaId                   string        `mapstructure:"BILLING_NCA_ID"`
	FunctionId                     string        `mapstructure:"FUNCTION_ID"`
	FunctionVersionId              string        `mapstructure:"FUNCTION_VERSION_ID"`
	FunctionName                   string        `mapstructure:"FUNCTION_NAME"`
	BaseAssetDir                   string        `mapstructure:"BASE_ASSET_DIR"`
	BaseResponseDir                string        `mapstructure:"BASE_RESPONSE_DIR"`
	HealthPort                     int           `mapstructure:"HEALTH_PORT"`
	CloudProvider                  string        `mapstructure:"CLOUD_PROVIDER"`
	CloudPlatform                  string        `mapstructure:"CLOUD_PLATFORM"`
	InstanceType                   string        `mapstructure:"INSTANCE_TYPE"`
	// SpotEnvironment is deprecated: use ICMSEnvironment (or set ICMS_ENVIRONMENT). If unset, ICMSEnvironment is set from this in setup.
	SpotEnvironment       string   `mapstructure:"SPOT_ENVIRONMENT"`
	ICMSEnvironment       string   `mapstructure:"ICMS_ENVIRONMENT"`
	ZoneName              string   `mapstructure:"ZONE_NAME"`
	GpuType               string   `mapstructure:"GPU_NAME"`
	GpuCount              int      `mapstructure:"ATTACHED_GPU_COUNT"`
	InstanceId            string   `mapstructure:"INSTANCE_ID"`
	MaxRequestConcurrency int      `mapstructure:"MAX_REQUEST_CONCURRENCY"`
	FunctionTags          []string `mapstructure:"FUNCTION_TAGS"` // csv
	EssAgentConfigDir     string   `mapstructure:"ESS_AGENT_CONFIG_DIR"`
	SecretsAssertionToken string   `mapstructure:"SECRETS_ASSERTION_TOKEN"`
	SharedConfigDir       string   `mapstructure:"SHARED_CONFIG_DIR"`
	// by default, compatibility mode is enabled
	V3BackwardsCompatibilityDisabled bool   `mapstructure:"V3_BACKWARDS_COMPATIBILITY_DISABLED"`
	InferenceServiceName             string `mapstructure:"INFERENCE_SERVICE_NAME"`
	InferenceNamespace               string `mapstructure:"INFERENCE_NAMESPACE"`
}

type NVCFWorker struct {
	globalCtx               context.Context
	config                  Config
	meteringConfig          *metering.Config
	baseAssetDir            string
	baseResponseDir         string
	nvcfClient              *nvcf.Client
	workerRestClient        *clients.HTTPClient
	baseInferenceDomain     string
	inferenceUrlWithoutPath string
	server                  servers.Server
	progressMonitor         *progress.Monitor
	health                  *health.Health
	httpProxy               *proxy.HttpProxy
	sharedProxyClient       httpstream.ProxyClient
	shutdownCtx             context.Context
	shutdownCancel          context.CancelFunc

	// in-flight requests keyed by request id; looked up on a NATS
	// cancel broadcast to fire the matching per-request cancel.
	cancelSubMu     sync.Mutex
	inFlightCancels map[string]context.CancelCauseFunc
	cancelSub       *nats.Subscription
}

func NewNVCFWorker(ctx context.Context, zapLogger *logs.ZapLogger, config Config) (*NVCFWorker, error) {
	_, err := uuid.Parse(config.FunctionId)
	if err != nil {
		return nil, types.NewInternalError(fmt.Errorf("invalid function id %s", config.FunctionId))
	}
	_, err = uuid.Parse(config.FunctionVersionId)
	if err != nil {
		return nil, types.NewInternalError(fmt.Errorf("invalid function version id %s", config.FunctionVersionId))
	}
	otelUrl, err := url.Parse(config.OTELExporterOTLPEndpoint)
	if err != nil {
		return nil, types.NewInternalError(err)
	}
	config.MaxRequestConcurrency = max(1, config.MaxRequestConcurrency) // at least 1

	if config.ICMSEnvironment == "" {
		config.ICMSEnvironment = config.SpotEnvironment
	}

	baseInferenceDomain := defaultBaseInferenceDomain

	if config.InferenceServiceName != "" && config.InferenceNamespace != "" {
		zap.L().Info("Inference configuration:  ",
			zap.String("namespace", config.InferenceNamespace),
			zap.String("service name", config.InferenceServiceName))
		baseInferenceDomain = fmt.Sprintf("%s.%s.svc.cluster.local", config.InferenceServiceName, config.InferenceNamespace)
	}

	if config.HelmChartInferenceServiceName != "" && config.HelmChartNamespace != "" {
		zap.L().Info("helm chart configuration",
			utils.PublicLogMarker,
			zap.String("namespace", config.HelmChartNamespace),
			zap.String("service name", config.HelmChartInferenceServiceName))
		baseInferenceDomain = fmt.Sprintf("%s.%s.svc.cluster.local", config.HelmChartInferenceServiceName, config.HelmChartNamespace)
	}

	baseInferenceUrl := fmt.Sprintf("http://%s:%d", baseInferenceDomain, config.InferencePort)
	clientConfig := clients.HTTPClientConfig{
		BaseClientConfig: &clients.BaseClientConfig{
			Type: "http",
		},
		NumRetries: 0,
	}
	client, err := clients.DefaultHTTPClient(&clientConfig, func(_ string, r *http.Request) string {
		return r.URL.Path
	})
	if err != nil {
		return nil, types.NewInternalError(err)
	}

	zap.L().Info("inference configuration",
		utils.PublicLogMarker,
		zap.String("domain", baseInferenceDomain),
		zap.Int("port", config.InferencePort))

	if config.SharedConfigDir == "" {
		config.SharedConfigDir = defaultSharedConfigDir
	}

	baseAssetDir := defaultBaseAssetDir
	if config.BaseAssetDir != "" {
		baseAssetDir = config.BaseAssetDir
	}

	baseResponseDir := defaultBaseResponseDir
	if config.BaseResponseDir != "" {
		baseResponseDir = config.BaseResponseDir
	}

	if config.HealthPort <= 0 {
		config.HealthPort = 8080
	}
	if config.NVCFFqdnNATS == "" {
		zap.L().Warn("using default NATS url")
		config.NVCFFqdnNATS = nats.DefaultURL
	}

	infraMeteringHeartbeatInterval := config.InfraMeteringHeartbeatInterval
	if infraMeteringHeartbeatInterval == 0 {
		infraMeteringHeartbeatInterval = metering.DefaultInfraMeteringHeartbeatInterval
	}
	billingNcaId := config.BillingNcaId
	if billingNcaId == "" {
		billingNcaId = config.NcaId
	}
	meteringConfig := metering.Config{
		Backend:                config.CloudProvider,
		NcaId:                  config.NcaId,
		BillingNcaId:           billingNcaId,
		NspectId:               metering.NspectIdFromEnv(),
		FunctionId:             config.FunctionId,
		FunctionVersionId:      config.FunctionVersionId,
		InstanceId:             config.InstanceId,
		InstanceType:           config.InstanceType,
		ICMSEnvironment:        config.ICMSEnvironment,
		ZoneName:               config.ZoneName,
		StartupTime:            time.Now(),
		InfraHeartbeatInterval: infraMeteringHeartbeatInterval,
		GpuCount:               config.GpuCount,
		GpuType:                config.GpuType,
		FunctionTags:           config.FunctionTags,
	}
	if meteringConfig.Backend == "NGN" {
		meteringConfig.Backend = "GFN"
	}

	progressMonitor := progress.New(baseResponseDir)
	utilsMetrics.InitProgressMonitorMetrics(progressMonitor)

	if config.InferenceReadyTimeout == 0 {
		config.InferenceReadyTimeout = defaultInferenceReadyTimeout
	}

	h, err := health.New(health.WithComponent(health.Component{
		Name: "gdn-nvcf-worker-service",
	}))
	if err != nil {
		return nil, types.NewInternalError(err)
	}

	healthCheckConfig, err := infHealth.HealthCheckConfig(baseInferenceDomain, config.InferenceHealthProtocol, config.InferenceHealthEndpoint, config.InferenceHealthTimeout, config.InferenceHealthExpectedResponseCode, config.InferenceHealthPort, config.InferencePort)
	if err != nil {
		return nil, types.NewUserActionableError(err)
	}

	err = h.Register(healthCheckConfig)
	if err != nil {
		return nil, types.NewInternalError(err)
	}

	shutdownCtx, shutdownCancel := context.WithCancel(ctx)

	w := &NVCFWorker{
		globalCtx:               ctx,
		shutdownCtx:             shutdownCtx,
		shutdownCancel:          shutdownCancel,
		config:                  config,
		meteringConfig:          &meteringConfig,
		workerRestClient:        client,
		baseInferenceDomain:     baseInferenceDomain,
		inferenceUrlWithoutPath: baseInferenceUrl,
		baseAssetDir:            baseAssetDir,
		baseResponseDir:         baseResponseDir,
		progressMonitor:         progressMonitor,
		health:                  h,
		sharedProxyClient:       httpstream.NewProxiedClient(),
		inFlightCancels:         make(map[string]context.CancelCauseFunc),
		server: servers.NewGRPCServer(&servers.GRPCConfig{
			HTTPAddr:  "0.0.0.0:9190",
			GRPCAddr:  "0.0.0.0:9191",
			AdminAddr: "0.0.0.0:" + strconv.Itoa(config.HealthPort),
			AdditionalServers: []servers.ServerFuncPair{
				{
					Execute: func() error {
						<-shutdownCtx.Done()
						return nil
					},
					Interrupt: func(err error) {
						if err != nil {
							zap.L().Info("received shutdown signal, initiating graceful shutdown...")
							shutdownCancel()
						}
					},
				},
			},

			BaseServerConfig: &servers.BaseServerConfig{
				ServiceName: "gdn-nvcf-worker-service",
				Version:     utils.Version,
				Tracing: tracing.OTELConfig{
					Enabled:     true,
					Endpoint:    otelUrl.Host,
					Insecure:    otelUrl.Scheme == "http",
					AccessToken: config.TracingAccessToken,
					Attributes: tracing.Attributes{
						Extra: map[string]string{
							"function_id":             config.FunctionId,
							"function_version_id":     config.FunctionVersionId,
							"function_name":           config.FunctionName,
							"host.provider":           config.CloudProvider,
							"host.platform":           config.CloudPlatform,
							"instance.type":           config.InstanceType,
							"nca_id":                  config.NcaId,
							"host.id":                 config.InstanceId,
							"host.dc":                 config.ZoneName,
							"gpu.type":                config.GpuType,
							"max_request_concurrency": strconv.Itoa(config.MaxRequestConcurrency),
						},
					},
				},
			},
		},
			servers.WithLogger(zapLogger),
			// only running a healthcheck server
			servers.WithRegisterServer(func(*grpc.Server) {}),
			servers.WithHttpHealthEndpoints("/v1/health/live"),
			servers.WithAdditionalHTTPHandlers(map[string]http.Handler{"/v1/health/ready": h.Handler()}),
		),
	}
	return w, nil
}

func (w *NVCFWorker) Setup() error {
	err := w.SetupWorkDirs()
	if err != nil {
		return types.NewInternalError(err)
	}

	if err := w.server.Setup(); err != nil {
		return types.NewInternalError(err)
	}

	return nil
}

// SetupWorkDirs is visible for testing
func (w *NVCFWorker) SetupWorkDirs() error {
	_, err := os.Stat(w.baseResponseDir)
	if errors.Is(err, os.ErrNotExist) {
		err = utils.CreateDirectory(w.baseResponseDir, os.FileMode(0777))
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	_, err = os.Stat(w.baseAssetDir)
	if errors.Is(err, os.ErrNotExist) {
		err = utils.CreateDirectory(w.baseAssetDir, os.FileMode(0777))
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return nil
}

// Run withHttpServer argument is for testing
func (w *NVCFWorker) Run(withHttpServer bool) error {
	zap.L().Info("starting worker",
		utils.PublicLogMarker,
		zap.String("instance id", w.config.InstanceId),
		zap.String("function id", w.config.FunctionId),
		zap.String("function version id", w.config.FunctionVersionId),
		zap.String("version", utils.Version))
	zap.L().Info("starting progress monitor")
	err := w.progressMonitor.Start()
	if err != nil {
		zap.L().Error("failed to start progress monitor", zap.Error(err))
		return types.NewInternalError(err)
	}
	defer utils.Close(func() error {
		zap.L().Info("stopping progress monitor")
		return w.progressMonitor.Stop()
	})
	if withHttpServer {
		go func() {
			// health server + framework config
			err := w.server.Run()
			if err != nil && w.shutdownCtx.Err() == nil {
				err = types.NewInternalError(err)
				utils.ExitReason(err)
				zap.S().Panic(err)
			}
		}()
	}

	infraMetering := metering.NewInfraMetering(w.globalCtx, metering.Function, w.meteringConfig, metering.InfraInitializing)
	defer utils.Close(infraMetering.Close)

	nvcfClient, err := nvcf.CreateClient(w.config.NVCFFqdn,
		&w.config.NVCFFqdnNATS,
		w.config.NVCFWorkerToken,
		w.config.NKeySeed,
		w.config.NcaId,
		w.config.InstanceId,
		w.config.FunctionId,
		w.config.FunctionVersionId,
		w.config.SharedConfigDir,
		nvcf.DefaultNvcfClientTimeout)
	if err != nil {
		return types.NewInternalError(err)
	}
	w.nvcfClient = nvcfClient
	defer w.nvcfClient.Close()

	defer ((*http.Client)(w.sharedProxyClient)).CloseIdleConnections()
	w.nvcfClient.StartMetadataCredentialsRefresher(w.globalCtx)

	// Start ess agent assertion token refreshment for container deployment
	if w.config.SecretsAssertionToken != "" {
		tokenDir := w.config.EssAgentConfigDir
		if tokenDir == "" {
			tokenDir = defaultEssAgentConfigDir
		}
		tokenPath := filepath.Join(tokenDir, EssTokenFileName)
		w.nvcfClient.StartAssertionTokenRefresher(w.globalCtx, tokenPath, true)
	} else {
		zap.L().Info("Skip refreshing ESS assertion token")
	}

	inferenceAddress := w.baseInferenceDomain + ":" + strconv.Itoa(w.config.InferencePort)
	httpProxy, err := proxy.NewHttpProxy(w.nvcfClient.NatsConn, w.nvcfClient.JetStream, w.config.FunctionId, w.config.FunctionVersionId, func(request *httputil.ProxyRequest) {
		request.Out.URL.Scheme = "http"
		request.Out.URL.Host = inferenceAddress
	}, nil, nil, nil)
	if err != nil {
		return types.NewInternalError(err)
	}
	w.httpProxy = httpProxy
	defer utils.Close(w.httpProxy.Close)

	// Wait for inference container to be ready.
	zap.L().Info("waiting for inference container to be ready",
		utils.PublicLogMarker,
		zap.Duration("timeout", w.config.InferenceReadyTimeout))
	checkInterval := min(w.config.InferenceReadyTimeout, 500*time.Millisecond)
	maxRetries := int(w.config.InferenceReadyTimeout / checkInterval)
	_, _, err = lo.AttemptWithDelay(maxRetries, checkInterval, func(i int, duration time.Duration) error {
		result := w.health.Measure(w.globalCtx)
		if result.Status != health.StatusOK {
			nvcfMetrics.HealthcheckCounter.WithLabelValues("failure").Inc()
			return errors.New("inference container not ready")
		}
		nvcfMetrics.HealthcheckCounter.WithLabelValues("success").Inc()
		return nil
	})
	if err != nil {
		zap.L().Info("inference container failed to become ready",
			utils.PublicLogMarker,
			zap.Duration("timeout", w.config.InferenceReadyTimeout))
		return types.NewUserActionableError(err)
	}
	zap.L().Info("inference container is ready")
	infraMetering.SetStatus(metering.InfraReady)

	connectedCtx, err := w.nvcfClient.ConnectIndefinitely(w.globalCtx)
	if err != nil {
		return types.NewInternalError(err)
	}

	// subscribe to cancel broadcasts so a client 504 / disconnect on the IS
	// can tear down our in-flight inference.
	cancelSubject := fmt.Sprintf("nvcf.cancel.%s", w.config.FunctionVersionId)
	cancelSub, err := w.nvcfClient.NatsConn.Subscribe(cancelSubject, w.handleCancelMessage)
	if err != nil {
		return types.NewInternalError(fmt.Errorf("subscribe %s: %w", cancelSubject, err))
	}
	w.cancelSub = cancelSub
	defer func() {
		if err := w.cancelSub.Unsubscribe(); err != nil {
			zap.L().Warn("failed to unsubscribe cancel subject", zap.Error(err))
		}
	}()

	// add nats health check now that we're connected
	err = w.health.Register(health.Config{
		Name:    "nats",
		Timeout: 1 * time.Second,
		Check: func(ctx context.Context) error {
			if !w.nvcfClient.NatsConn.IsConnected() {
				return fmt.Errorf("nats not connected")
			}
			return nil
		},
	})
	if err != nil {
		return types.NewInternalError(err)
	}

	err = metrics.Initialize(metrics.NvcfRootNamespace, w.nvcfClient.NatsConn)
	if err != nil {
		return types.NewInternalError(err)
	}

	nvcfMetrics.WorkerNatsServerGauge.WithLabelValues(w.config.NVCFFqdnNATS).Set(1)

	zap.L().Info("starting work sessions", zap.Int("concurrency", w.config.MaxRequestConcurrency))
	return w.workSession(connectedCtx)
}

func (w *NVCFWorker) workSession(ctx context.Context) error {
	nvcfMetrics.WorkerThreadCountGauge.Add(float64(w.config.MaxRequestConcurrency))
	defer nvcfMetrics.WorkerThreadCountGauge.Sub(float64(w.config.MaxRequestConcurrency))

	pool, err := ants.NewPool(w.config.MaxRequestConcurrency, ants.WithPreAlloc(true))
	if err != nil {
		return types.NewInternalError(err)
	}
	defer pool.Release()

	// Start health check in background
	inferenceHealth := infHealth.NewInferenceHealthStatus()
	go inferenceHealth.RunHealthCheckRoutine(ctx, w.health)

	for {
		// checking if the current ctx ended or the shutdownCtx ended, so we should not requeue another work request
		if ctx.Err() != nil || w.shutdownCtx.Err() != nil {
			return nil
		}

		connectedRegion := *w.nvcfClient.ConnectedRegions.Load()
		globalNatsConsumer, err := consumer.NewNatsConsumer(ctx, w.nvcfClient.JetStream, func(ctx context.Context, region string) error {
			_, err := w.nvcfClient.Client.ProvisionRegionalWorker(ctx, &pb.ProvisionWorkerRequest{
				InstanceId:        w.config.InstanceId,
				FunctionId:        w.config.FunctionId,
				FunctionVersionId: w.config.FunctionVersionId,
				RegionToProvision: region,
			}, auth.GrpcTokenFromSource(w.nvcfClient.NvcfTokenProvider))
			return err
		}, w.config.FunctionVersionId, connectedRegion.Primary, connectedRegion.Secondaries, w.config.MaxRequestConcurrency)
		if err != nil {
			return types.NewInternalError(err)
		}
		w.nvcfClient.CurrentNatsConsumer.Store(globalNatsConsumer)

		// close FetchWorkStream's ctx when region list changes to gracefully end the work stream and loop to fetch another
		fetchCtx, cancelFetch := context.WithCancel(ctx)
		stopAfterFunc := context.AfterFunc(w.shutdownCtx, cancelFetch)

		// Wrap cancelFetch with a function that also stops the afterFunc to prevent memory leaks
		wrappedCancelFetch := func() {
			stopAfterFunc()
			cancelFetch()
		}

		w.nvcfClient.TriggerNewNatsConsumer.Store((*func())(&wrappedCancelFetch))
		inferenceHealth.CallBackFn.Store((*func())(&wrappedCancelFetch))

		zap.L().Info("waiting until inference container is healthy")
		err = inferenceHealth.WaitForHealthyState(fetchCtx)
		if err != nil {
			zap.L().Error("unable to perform health check", zap.Error(err))
			continue
		}
		zap.L().Info("inference container is healthy, will fetch work stream")

		var workRequests <-chan *consumer.WorkRequest
		// retry forever. cancellation is not gated by time.
		exponentialBackOff := backoff.NewExponentialBackOff(backoff.WithMaxElapsedTime(0))
		err = backoff.Retry(func() error {
			zap.L().Debug("requesting new work stream")
			var err error
			// cancelling fetchCtx will cause the workRequests channel to gracefully close
			workRequests, err = globalNatsConsumer.FetchWorkStream(fetchCtx)
			return err
		}, backoff.WithContext(exponentialBackOff, fetchCtx))
		if err != nil {
			wrappedCancelFetch()
			continue
		}

		for work := range workRequests {
			zap.L().Info("got work request",
				utils.PublicLogMarker,
				zap.String("req id", work.RequestData.RequestId))

			err := pool.Submit(func() {
				defer utils.Close(work.Close)
				err := w.handleWorkRequest(ctx, work)
				if err != nil {
					// failure for a single request, keep trying to consume other requests
					zap.L().Error("failed to handle request", zap.Error(err), zap.String("req id", work.RequestData.RequestId))
				}
			})
			// failure to submit to the pool is a catastrophic error and should never happen
			// unless the pool is misconfigured. this should be dead code.
			if err != nil {
				zap.L().Debug("nacking message due to failed request submission", zap.String("subject", work.Subject()))
				_ = work.Nak()
				zap.L().Warn("failed to submit work request", zap.Error(err))
				_ = work.Close()
			}
		}
		wrappedCancelFetch()

		// A very long timeout to avoid the pool from being released without completing the already accepted requests
		if w.shutdownCtx.Err() != nil {
			pool.ReleaseTimeout(time.Second * 1000000)
		}

		zap.L().Debug("finished processing work stream")
	}
}

// Shutdown triggers a graceful shutdown of the worker -- This is only used by tests
func (w *NVCFWorker) Shutdown() {
	w.shutdownCancel()
}
