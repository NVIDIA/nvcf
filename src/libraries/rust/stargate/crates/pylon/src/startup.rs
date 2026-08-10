// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

use std::collections::BTreeSet;
use std::future::Future;
use std::net::SocketAddr;
use std::str::FromStr;
use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, Result, ensure};
use pylon_lib::{
    AuthTokenProvider, BringupConfig, CalibrationConfig, EngineStatsStreamConfig,
    EngineStatsStreamHandle, EngineStatsStreamMode, InferenceServerRegistrationClient,
    InferenceServerRegistrationConfig, MetricsServerHandle, ModelDiscoveryConfig,
    ModelInitialization, ModelLifecycleConfig, ModelLifecycleHandle, ModelSource, PylonMetrics,
    PylonQueueMismatchRetryConfig, PylonRetryConfig, PylonRuntimeState, QuicHttpTunnelConfig,
    QuicHttpTunnelHandle, RequestQualityMonitorConfig, StatsCollectorConfig, StatsCollectorHandle,
    TunnelForwardingConfig, start_engine_stats_stream, start_metrics_server, start_model_lifecycle,
    start_quic_http_tunnel, start_stats_collector_with_engine_stats,
    stats_aggregator_update_channel,
};
use reqwest::header::HeaderName;
use stargate_proto::pb::InferenceServerStatus;
use stargate_protocol::BackendConnectivity;
use stargate_runtime::wait_for_termination_signal;
use tokio::task::JoinError;
use tracing::{error, info, warn};

use super::Args;

type TaskExit = std::result::Result<(), JoinError>;

pub(super) async fn run(args: Args) -> Result<()> {
    let plan = PylonStartupPlan::from_args(&args)?;
    let _telemetry_guard = stargate_telemetry::init_telemetry(
        args.otel_endpoint.as_deref(),
        &args.otel_service_name,
        "pylon_upstream_http_request",
        None,
    )?;
    let runtime = start_pylon_runtime(&args, &plan).await?;

    log_startup_complete(
        &args.stargate_address,
        &args.inference_server_id,
        &plan,
        &runtime.registration_inference_server_url,
        &runtime.initial_model_ids,
    );
    info!("pylon running");
    runtime
        .run_until_shutdown(wait_for_termination_signal())
        .await
}

fn log_startup_complete(
    stargate_address: &str,
    inference_server_id: &str,
    plan: &PylonStartupPlan,
    registration_inference_server_url: &str,
    model_ids: &[String],
) {
    if plan.backend_tunnel.is_reverse() {
        info!(
            stargate = stargate_address,
            inference_server_id,
            cluster_id = %plan.cluster_id,
            upstream = %plan.upstream,
            model_ids = ?model_ids,
            "pylon startup complete; stargate registration started (reverse tunnel mode)"
        );
    } else {
        info!(
            stargate = stargate_address,
            inference_server_id,
            cluster_id = %plan.cluster_id,
            inference_server_url = registration_inference_server_url,
            upstream = %plan.upstream,
            model_ids = ?model_ids,
            "pylon startup complete; stargate registration started (direct tunnel mode)"
        );
    }
}

pub(crate) struct PylonStartupPlan {
    upstream: String,
    cluster_id: String,
    model_source: ModelSource,
    pylon_retry: PylonRetryConfig,
    queue_mismatch_retry: PylonQueueMismatchRetryConfig,
    model_initialization: ModelInitialization,
    bringup: BringupConfig,
    request_quality_monitor: RequestQualityMonitorConfig,
    metrics_addr: SocketAddr,
    auth_token_provider: Option<Arc<AuthTokenProvider>>,
    backend_tunnel: BackendTunnelStartup,
}

enum BackendTunnelStartup {
    Direct { listen_addr: SocketAddr },
    Reverse,
}

impl BackendTunnelStartup {
    fn from_args(args: &Args) -> Result<Self> {
        match args.backend_connectivity {
            BackendConnectivity::Direct => Ok(Self::Direct {
                listen_addr: args.quic_listen_addr.parse()?,
            }),
            BackendConnectivity::Reverse => Ok(Self::Reverse),
        }
    }

    fn direct_listen_addr(&self) -> Option<SocketAddr> {
        match self {
            Self::Direct { listen_addr } => Some(*listen_addr),
            Self::Reverse => None,
        }
    }

    fn is_reverse(&self) -> bool {
        matches!(self, Self::Reverse)
    }
}

impl PylonStartupPlan {
    pub(crate) fn from_args(args: &Args) -> Result<Self> {
        let model_initialization = model_initialization_from_args(args)?;
        let model_source = model_source_from_args(args)?;
        Ok(Self {
            upstream: normalize_base_url(&args.upstream_http_base_url),
            cluster_id: effective_cluster_id(args),
            model_source,
            pylon_retry: pylon_retry_config_from_args(args)?,
            queue_mismatch_retry: pylon_queue_mismatch_retry_config_from_args(args)?,
            model_initialization,
            bringup: BringupConfig {
                enabled: !args.disable_bringup,
                active_canary_interval: Duration::from_millis(args.active_canary_interval_ms),
                canary_timeout: Duration::from_millis(args.bringup_canary_timeout_ms),
                canary_max_generation_threshold: args.canary_max_generation_threshold,
            },
            request_quality_monitor: request_quality_monitor_config_from_args(args),
            metrics_addr: format!("{}:{}", args.metrics_host, args.metrics_port).parse()?,
            auth_token_provider: auth_token_provider_from_args(args),
            backend_tunnel: BackendTunnelStartup::from_args(args)?,
        })
    }

    pub(crate) fn direct_tunnel_listen_addr(&self) -> Option<SocketAddr> {
        self.backend_tunnel.direct_listen_addr()
    }

    #[cfg(test)]
    pub(crate) fn static_model_ids(&self) -> Option<&BTreeSet<String>> {
        match &self.model_source {
            ModelSource::Static(model_ids) => Some(model_ids),
            ModelSource::Discovered(_) => None,
        }
    }
}

fn auth_token_provider_from_args(args: &Args) -> Option<Arc<AuthTokenProvider>> {
    match (&args.auth_token, &args.auth_token_file) {
        (Some(token), _) => Some(Arc::new(AuthTokenProvider::Static(token.clone()))),
        (None, Some(path)) => Some(Arc::new(AuthTokenProvider::File(path.clone().into()))),
        (None, None) => None,
    }
}

fn model_source_from_args(args: &Args) -> Result<ModelSource> {
    ensure!(
        args.model_name
            .iter()
            .all(|model_id| !model_id.trim().is_empty()),
        "model names must be non-empty"
    );
    ensure!(
        args.model_discovery_poll_interval_ms > 0,
        "model discovery poll interval must be greater than zero"
    );
    ensure!(
        args.model_discovery_request_timeout_ms > 0,
        "model discovery request timeout must be greater than zero"
    );
    let model_ids = args.model_name.iter().cloned().collect::<BTreeSet<_>>();
    Ok(if model_ids.is_empty() {
        ModelSource::Discovered(ModelDiscoveryConfig {
            provider: args.model_discovery_provider,
            poll_interval: Duration::from_millis(args.model_discovery_poll_interval_ms),
            request_timeout: Duration::from_millis(args.model_discovery_request_timeout_ms),
        })
    } else {
        ModelSource::Static(model_ids)
    })
}

struct RunningPylon {
    registration_client: InferenceServerRegistrationClient,
    engine_stats_stream: Option<RunningEngineStatsStream>,
    stats_collector: StatsCollectorHandle,
    model_lifecycle: ModelLifecycleHandle,
    metrics_server: MetricsServerHandle,
    tunnel: Option<QuicHttpTunnelHandle>,
    registration_inference_server_url: String,
    initial_model_ids: Vec<String>,
}

struct RunningEngineStatsStream {
    mode: EngineStatsStreamMode,
    handle: EngineStatsStreamHandle,
}

impl RunningPylon {
    async fn run_until_shutdown<S>(mut self, signal: S) -> Result<()>
    where
        S: Future<Output = std::io::Result<&'static str>>,
    {
        tokio::pin!(signal);
        loop {
            let error = tokio::select! {
                result = signal.as_mut() => {
                    let result = result.context("failed to receive pylon termination signal");
                    if let Ok(signal) = &result {
                        info!(signal, "received shutdown signal");
                    }
                    self.shutdown().await;
                    return result.map(|_| ());
                }
                result = self.registration_client.wait_for_exit() => critical_task_exit_error("registration session", result),
                result = async {
                    match self.engine_stats_stream.as_mut() {
                        Some(stream) => stream.handle.wait_for_exit().await,
                        None => std::future::pending().await,
                    }
                } => {
                    if engine_stats_exit_is_expected(
                        self.engine_stats_stream.as_ref().map(|stream| stream.mode),
                        &result,
                    ) {
                        info!("auto engine stats stream completed after enabling fallback");
                        self.engine_stats_stream = None;
                        continue;
                    }
                    critical_task_exit_error("engine stats stream", result)
                }
                result = self.stats_collector.wait_for_exit() => critical_task_exit_error("stats collector", result),
                result = async {
                    self.model_lifecycle.wait_for_exit().await
                } => critical_task_exit_error("model lifecycle supervisor", result),
                result = self.metrics_server.wait_for_exit() => critical_task_exit_error("metrics server", result),
                result = async {
                    match self.tunnel.as_mut() {
                        Some(tunnel) => tunnel.wait_for_exit().await,
                        None => std::future::pending().await,
                    }
                } => {
                    critical_task_exit_error("direct tunnel accept loop", result)
                }
            };
            error!(error = %error, "critical pylon task exited");
            self.shutdown().await;
            return Err(error);
        }
    }

    async fn shutdown(self) {
        let Self {
            mut registration_client,
            engine_stats_stream,
            stats_collector,
            model_lifecycle,
            metrics_server,
            tunnel,
            ..
        } = self;
        tokio::join!(
            registration_client.shutdown(),
            async move {
                if let Some(stream) = engine_stats_stream {
                    stream.handle.shutdown().await;
                }
            },
            stats_collector.shutdown(),
            model_lifecycle.shutdown(),
            metrics_server.shutdown(),
            async move {
                if let Some(tunnel) = tunnel {
                    tunnel.shutdown().await;
                }
            },
        );
    }
}

fn critical_task_exit_error(name: &'static str, result: TaskExit) -> anyhow::Error {
    match result {
        Ok(()) => anyhow::anyhow!("{name} exited unexpectedly"),
        Err(error) => anyhow::anyhow!("{name} failed: {error}"),
    }
}

fn engine_stats_exit_is_expected(mode: Option<EngineStatsStreamMode>, result: &TaskExit) -> bool {
    mode == Some(EngineStatsStreamMode::Auto) && result.is_ok()
}

async fn start_pylon_runtime(args: &Args, plan: &PylonStartupPlan) -> Result<RunningPylon> {
    let metrics = PylonMetrics::new()?;
    metrics.observe_target_info(
        env!("CARGO_PKG_VERSION"),
        env!("CARGO_PKG_NAME"),
        option_env!("GIT_COMMIT_HASH")
            .or(option_env!("GIT_COMMIT_SHA"))
            .unwrap_or(""),
    );
    let metrics_server = start_metrics_server(plan.metrics_addr, metrics.registry()).await?;
    let stats_config = stats_collector_config_from_args(args, &plan.upstream);
    let (runtime_state, request_observation_rx) = PylonRuntimeState::observed(
        InferenceServerStatus::Active,
        &[],
        stats_config.observation_channel_capacity,
        Some(metrics.clone()),
    );
    let (engine_stats_stream, stats_update_rx) = start_engine_stats_runtime(
        args,
        plan,
        metrics.clone(),
        &stats_config,
        runtime_state.clone(),
    )
    .unzip();
    let stats_collector = start_stats_collector_with_engine_stats(
        stats_config,
        request_observation_rx,
        stats_update_rx,
        runtime_state.clone(),
    );
    let tls_cert_pem = args.tls_cert_path.as_ref().map(std::fs::read).transpose()?;
    let tls_key_pem = args.tls_key_path.as_ref().map(std::fs::read).transpose()?;
    let forwarding =
        tunnel_forwarding_config_from_plan(plan, runtime_state.clone(), metrics.clone());
    let tunnel = start_direct_tunnel_from_plan(
        args,
        plan,
        &forwarding,
        tls_cert_pem.as_deref(),
        tls_key_pem,
    )
    .await?;
    if matches!(
        &plan.model_initialization,
        ModelInitialization::Calibration(_)
    ) {
        warn!(
            cluster_id = %plan.cluster_id,
            "running local calibration; --do-calibration is valid only for a cluster with one pylon"
        );
    }
    let model_lifecycle = start_model_lifecycle(
        ModelLifecycleConfig {
            upstream_http_base_url: plan.upstream.clone(),
            source: plan.model_source.clone(),
            initialization: plan.model_initialization.clone(),
            bringup: plan.bringup.clone(),
        },
        runtime_state.clone(),
        &stats_collector,
        Some(metrics),
    )
    .await
    .context("pylon initial model initialization failed")?;
    let initial_model_ids = runtime_state.advertised_model_ids();
    let registration_inference_server_url = registration_url(plan, tunnel.as_ref());
    let registration_config = registration_config_from_plan(
        args,
        plan,
        forwarding,
        registration_inference_server_url.clone(),
        tls_cert_pem,
    );
    let mut registration_client = InferenceServerRegistrationClient::default();
    registration_client.start(registration_config)?;

    Ok(RunningPylon {
        registration_client,
        engine_stats_stream,
        stats_collector,
        model_lifecycle,
        metrics_server,
        tunnel,
        registration_inference_server_url,
        initial_model_ids,
    })
}

fn start_engine_stats_runtime(
    args: &Args,
    plan: &PylonStartupPlan,
    metrics: Arc<PylonMetrics>,
    stats_config: &StatsCollectorConfig,
    runtime_state: PylonRuntimeState,
) -> Option<(
    RunningEngineStatsStream,
    flume::Receiver<pylon_lib::StatsAggregatorUpdate>,
)> {
    let (stats_update_tx, stats_update_rx) = stats_aggregator_update_channel(stats_config);
    let mut config = EngineStatsStreamConfig::new(
        &plan.upstream,
        &args.engine_stats_stream_path,
        args.engine_stats_stream,
    );
    config.metrics = Some(metrics);
    config.runtime_state = Some(runtime_state);
    let mode = config.mode;
    start_engine_stats_stream(config, stats_update_tx)
        .map(|handle| (RunningEngineStatsStream { mode, handle }, stats_update_rx))
}

async fn start_direct_tunnel_from_plan(
    args: &Args,
    plan: &PylonStartupPlan,
    forwarding: &TunnelForwardingConfig,
    tls_cert_pem: Option<&[u8]>,
    tls_key_pem: Option<Vec<u8>>,
) -> Result<Option<QuicHttpTunnelHandle>> {
    let Some(tunnel_config) =
        direct_tunnel_config(args, plan, forwarding, tls_cert_pem, tls_key_pem)
    else {
        return Ok(None);
    };
    let tunnel = start_quic_http_tunnel(tunnel_config).await?;
    info!(addr = %tunnel.listen_addr(), url = %format!("quic://{}", tunnel.listen_addr()), "QUIC tunnel listening");
    Ok(Some(tunnel))
}

fn registration_url(plan: &PylonStartupPlan, tunnel: Option<&QuicHttpTunnelHandle>) -> String {
    tunnel
        .map(|tunnel| format!("quic://{}", tunnel.listen_addr()))
        .unwrap_or_else(|| plan.upstream.clone())
}

fn direct_tunnel_config(
    args: &Args,
    plan: &PylonStartupPlan,
    forwarding: &TunnelForwardingConfig,
    tls_cert_pem: Option<&[u8]>,
    tls_key_pem: Option<Vec<u8>>,
) -> Option<QuicHttpTunnelConfig> {
    let listen_addr = plan.direct_tunnel_listen_addr()?;
    Some(QuicHttpTunnelConfig {
        listen_addr,
        upstream_http_base_url: plan.upstream.clone(),
        inference_server_id: Some(args.inference_server_id.clone()),
        forwarding: forwarding.clone(),
        tls_cert_pem: tls_cert_pem.map(Vec::from),
        tls_key_pem,
        tunnel_protocol: args.tunnel_protocol,
    })
}

fn registration_config_from_plan(
    args: &Args,
    plan: &PylonStartupPlan,
    forwarding: TunnelForwardingConfig,
    inference_server_url: String,
    tls_cert_pem: Option<Vec<u8>>,
) -> InferenceServerRegistrationConfig {
    InferenceServerRegistrationConfig {
        seeds: vec![args.stargate_address.clone()],
        inference_server_id: args.inference_server_id.clone(),
        cluster_id: plan.cluster_id.clone(),
        inference_server_url,
        min_update_interval: Duration::from_millis(args.min_update_interval_ms),
        reverse_tunnel: plan.backend_tunnel.is_reverse(),
        tls_cert_pem,
        quic_insecure: args.quic_insecure,
        tunnel_protocol: args.tunnel_protocol,
        forwarding,
        auth_token_provider: plan.auth_token_provider.clone(),
    }
}

fn tunnel_forwarding_config_from_plan(
    plan: &PylonStartupPlan,
    runtime_state: PylonRuntimeState,
    metrics: Arc<PylonMetrics>,
) -> TunnelForwardingConfig {
    TunnelForwardingConfig {
        runtime_state,
        request_quality_monitor: plan.request_quality_monitor.clone(),
        metrics: Some(metrics),
        retry: plan.pylon_retry.clone(),
        queue_mismatch_retry: plan.queue_mismatch_retry.clone(),
        ..Default::default()
    }
}

pub(crate) fn stats_collector_config_from_args(
    args: &Args,
    upstream: &str,
) -> StatsCollectorConfig {
    StatsCollectorConfig {
        openai_fallback_stats_enabled: args.engine_stats_stream == EngineStatsStreamMode::Off,
        // Mock benchmark backends can expose live KV-cache occupancy over HTTP;
        // real upstreams usually do not, so polling is explicit.
        kv_cache_stats_url: args.kv_cache_stats_path.as_deref().map(|path| {
            format!(
                "{}/{}",
                upstream.trim_end_matches('/'),
                path.trim_start_matches('/')
            )
        }),
        ..Default::default()
    }
}

pub(crate) fn normalize_base_url(url: &str) -> String {
    url.trim_end_matches('/').to_string()
}

pub(crate) fn effective_cluster_id(args: &Args) -> String {
    args.cluster_id
        .clone()
        .unwrap_or_else(|| args.inference_server_id.clone())
}

pub(crate) fn pylon_retry_config_from_args(args: &Args) -> Result<PylonRetryConfig> {
    Ok(PylonRetryConfig {
        retryable_upstream_status_codes: parse_retryable_status_codes(
            &args.pylon_retryable_upstream_status_codes,
        )?,
        require_upstream_retry_header: args.pylon_require_upstream_retry_header,
        upstream_retry_header: HeaderName::from_str(args.pylon_upstream_retry_header.trim())
            .with_context(|| {
                format!(
                    "invalid pylon upstream retry header: {}",
                    args.pylon_upstream_retry_header
                )
            })?,
        propagate_retry_after: args.pylon_propagate_retry_after,
        local_connect_failures_retryable: args.pylon_local_connect_failures_retryable,
    })
}

pub(crate) fn pylon_queue_mismatch_retry_config_from_args(
    args: &Args,
) -> Result<PylonQueueMismatchRetryConfig> {
    ensure!(
        args.pylon_queue_mismatch_tolerance_factor.is_finite()
            && args.pylon_queue_mismatch_tolerance_factor > 0.0,
        "pylon queue mismatch tolerance factor must be finite and positive"
    );
    Ok(PylonQueueMismatchRetryConfig {
        enabled: args.pylon_queue_mismatch_retry_enabled,
        min_delta_ms: args.pylon_queue_mismatch_min_delta_ms,
        tolerance_factor: args.pylon_queue_mismatch_tolerance_factor,
        retry_after_ms: args.pylon_queue_mismatch_retry_after_ms,
    })
}

fn model_initialization_from_args(args: &Args) -> Result<ModelInitialization> {
    ensure!(
        args.do_calibration ^ args.initial_input_tps.is_some(),
        "exactly one of --do-calibration or --initial-input-tps is required"
    );
    ensure!(
        args.initial_input_tps
            .is_none_or(|input_tps| input_tps.is_finite() && input_tps > 0.0),
        "initial input TPS must be finite and positive"
    );
    if args.do_calibration {
        ensure!(
            args.calibration_requests > 0,
            "--do-calibration requires --calibration-requests greater than zero"
        );
        ensure!(
            !args.benchmark_pin_input_tps,
            "--benchmark-pin-input-tps requires --initial-input-tps"
        );
        return Ok(ModelInitialization::Calibration(CalibrationConfig {
            health_timeout: Duration::from_millis(args.bringup_canary_timeout_ms),
            calibration_requests: args.calibration_requests,
            calibration_prompt_units: args.calibration_prompt_units,
            calibration_max_concurrency: args.calibration_max_concurrency,
            calibration_timeout: Duration::from_millis(args.bringup_calibration_timeout_ms),
        }));
    }

    Ok(ModelInitialization::ConfiguredInputTps {
        input_tps: args
            .initial_input_tps
            .expect("exactly one bootstrap source was validated"),
        pin: args.benchmark_pin_input_tps,
    })
}

pub(crate) fn parse_retryable_status_codes(value: &str) -> Result<Vec<reqwest::StatusCode>> {
    value
        .split(',')
        .map(str::trim)
        .filter(|part| !part.is_empty())
        .map(|part| {
            let code = part
                .parse::<u16>()
                .with_context(|| format!("invalid pylon retryable status code: {part}"))?;
            reqwest::StatusCode::from_u16(code)
                .with_context(|| format!("invalid pylon retryable status code: {part}"))
        })
        .collect()
}

pub(crate) fn request_quality_monitor_config_from_args(args: &Args) -> RequestQualityMonitorConfig {
    RequestQualityMonitorConfig {
        collect_quality_metrics: args.collect_quality_metrics,
        collect_quality_metrics_min_tokens: args.collect_quality_metrics_min_tokens,
        output_tokens_threshold_min: args.quality_output_tokens_threshold_min,
        output_compression_threshold_max: args.quality_output_compression_threshold_max,
        output_degeneracy_threshold_min: args.quality_output_degeneracy_threshold_min,
        output_repetition_1gram_threshold_min: args.quality_output_repetition_1gram_threshold_min,
        output_repetition_2gram_threshold_min: args.quality_output_repetition_2gram_threshold_min,
        output_repetition_3gram_threshold_min: args.quality_output_repetition_3gram_threshold_min,
        median_logprob_threshold_max: args.quality_median_logprob_threshold_max,
    }
}

#[cfg(test)]
mod tests {
    use std::collections::HashSet;
    use std::pin::Pin;
    use std::sync::Arc;
    use std::sync::Mutex as StdMutex;
    use std::sync::atomic::{AtomicUsize, Ordering};
    use std::time::Duration;

    use axum::extract::State;
    use axum::http::{HeaderMap, StatusCode};
    use axum::response::{IntoResponse, Response as AxumResponse};
    use axum::routing::{get, post};
    use axum::{Json, Router};
    use clap::Parser;
    use pylon_lib::{
        EngineStatsStreamMode, PylonMetrics, RequestObservation, RequestObservationEndpoint,
        RequestObservationState, TunnelTransportProtocol,
    };
    use stargate_proto::pb::stargate_control_plane_server::{
        StargateControlPlane, StargateControlPlaneServer,
    };
    use stargate_proto::pb::{
        InferenceServerAck, InferenceServerRegistration, InferenceServerStatus, StargateInfo,
        WatchStargatesRequest, WatchStargatesResponse,
    };
    use stargate_protocol::tunnel_contract::HEADER_REQUEST_ID;
    use tokio::net::TcpListener;
    use tokio::sync::{Semaphore, mpsc};
    use tokio_stream::wrappers::TcpListenerStream;
    use tokio_stream::{Stream, StreamExt};
    use tonic::{Request, Response, Status};

    use super::*;

    fn startup(extra: &[&str]) -> (Args, PylonStartupPlan) {
        let mut args = vec![
            "pylon",
            "--upstream-http-base-url",
            "http://127.0.0.1:8090/",
            "--model-name",
            "model-a",
            "--model-name",
            "model-b",
            "--initial-input-tps",
            "100",
        ];
        args.extend_from_slice(extra);
        let args = <Args as Parser>::try_parse_from(args).expect("args should parse");
        let plan = PylonStartupPlan::from_args(&args).expect("startup plan should build");
        (args, plan)
    }

    fn runtime_args(
        upstream: &str,
        stargate: SocketAddr,
        model_ids: &[&str],
        bootstrap_args: &[&str],
    ) -> Args {
        let mut args = vec![
            "pylon".to_string(),
            "--upstream-http-base-url".to_string(),
            upstream.to_string(),
            "--stargate-address".to_string(),
            format!("http://{stargate}"),
            "--inference-server-id".to_string(),
            "pylon-a".to_string(),
            "--cluster-id".to_string(),
            "cluster-a".to_string(),
            "--quic-listen-addr".to_string(),
            "127.0.0.1:0".to_string(),
            "--metrics-host".to_string(),
            "127.0.0.1".to_string(),
            "--metrics-port".to_string(),
            "0".to_string(),
            "--engine-stats-stream".to_string(),
            "off".to_string(),
            "--disable-bringup".to_string(),
            "--min-update-interval-ms".to_string(),
            "10".to_string(),
        ];
        for model_id in model_ids {
            args.push("--model-name".to_string());
            args.push((*model_id).to_string());
        }
        args.extend(bootstrap_args.iter().map(|arg| (*arg).to_string()));
        Args::try_parse_from(args).expect("runtime args should parse")
    }

    #[derive(Clone)]
    struct TestUpstreamState {
        calibration_requests: Arc<AtomicUsize>,
        calibration_generations: Arc<StdMutex<HashSet<String>>>,
        calibration_plans: Arc<AtomicUsize>,
        calibration_started: mpsc::UnboundedSender<String>,
        calibration_release: Arc<Semaphore>,
        calibration_request_errors: bool,
    }

    struct TestUpstream {
        base_url: String,
        calibration_requests: Arc<AtomicUsize>,
        calibration_plans: Arc<AtomicUsize>,
        calibration_started: mpsc::UnboundedReceiver<String>,
        calibration_release: Arc<Semaphore>,
        task: tokio::task::JoinHandle<()>,
    }

    impl TestUpstream {
        async fn spawn(calibration_request_errors: bool) -> Self {
            let listener = TcpListener::bind("127.0.0.1:0")
                .await
                .expect("test upstream should bind");
            let addr = listener.local_addr().expect("test upstream address");
            let calibration_requests = Arc::new(AtomicUsize::new(0));
            let calibration_plans = Arc::new(AtomicUsize::new(0));
            let (calibration_started, calibration_started_rx) = mpsc::unbounded_channel();
            let calibration_release = Arc::new(Semaphore::new(0));
            let state = TestUpstreamState {
                calibration_requests: calibration_requests.clone(),
                calibration_generations: Arc::new(StdMutex::new(HashSet::new())),
                calibration_plans: calibration_plans.clone(),
                calibration_started,
                calibration_release: calibration_release.clone(),
                calibration_request_errors,
            };
            let app = Router::new()
                .route("/health", get(|| async { "ok" }))
                .route(
                    "/v1/models",
                    get(|| async { Json(serde_json::json!({"data": []})) }),
                )
                .route("/v1/chat/completions", post(test_calibration_completion))
                .with_state(state);
            let task = tokio::spawn(async move {
                axum::serve(listener, app)
                    .await
                    .expect("test upstream should serve");
            });
            Self {
                base_url: format!("http://{addr}"),
                calibration_requests,
                calibration_plans,
                calibration_started: calibration_started_rx,
                calibration_release,
                task,
            }
        }

        async fn shutdown(self) {
            self.task.abort();
            let _ = self.task.await;
        }
    }

    async fn test_calibration_completion(
        State(state): State<TestUpstreamState>,
        headers: HeaderMap,
        Json(request): Json<serde_json::Value>,
    ) -> AxumResponse {
        state.calibration_requests.fetch_add(1, Ordering::SeqCst);
        let model_id = request["model"].as_str().unwrap_or_default().to_string();
        let generation = headers
            .get(HEADER_REQUEST_ID)
            .and_then(|value| value.to_str().ok())
            .and_then(|request_id| request_id.rsplit_once('-').map(|(prefix, _)| prefix))
            .expect("calibration request should carry an exact-generation request ID");
        let first_request = state
            .calibration_generations
            .lock()
            .expect("calibration generation set should not be poisoned")
            .insert(generation.to_string());
        if first_request {
            state.calibration_plans.fetch_add(1, Ordering::SeqCst);
            state
                .calibration_started
                .send(model_id)
                .expect("test should still observe calibration");
        }
        let permit = state
            .calibration_release
            .acquire()
            .await
            .expect("test calibration gate should remain open");
        permit.forget();
        if state.calibration_request_errors {
            return StatusCode::INTERNAL_SERVER_ERROR.into_response();
        }
        Json(serde_json::json!({"usage": {"completion_tokens": 1}})).into_response()
    }

    type TestWatchStream =
        Pin<Box<dyn Stream<Item = Result<WatchStargatesResponse, Status>> + Send + 'static>>;
    type TestRegistrationStream =
        Pin<Box<dyn Stream<Item = Result<InferenceServerAck, Status>> + Send + 'static>>;

    #[derive(Clone)]
    struct TestControlPlaneService {
        address: String,
        watch_calls: Arc<AtomicUsize>,
        register_calls: Arc<AtomicUsize>,
        registrations: mpsc::UnboundedSender<InferenceServerRegistration>,
    }

    #[tonic::async_trait]
    impl StargateControlPlane for TestControlPlaneService {
        type WatchStargatesStream = TestWatchStream;
        type RegisterInferenceServerStream = TestRegistrationStream;

        async fn watch_stargates(
            &self,
            _request: Request<WatchStargatesRequest>,
        ) -> Result<Response<Self::WatchStargatesStream>, Status> {
            self.watch_calls.fetch_add(1, Ordering::SeqCst);
            let snapshot = WatchStargatesResponse {
                stargates: vec![StargateInfo {
                    stargate_id: "stargate-a".to_string(),
                    advertise_addr: self.address.clone(),
                    http_advertise_addr: String::new(),
                    grpc_pylon_dial_addr: String::new(),
                }],
                watch_stargate_urls: Vec::new(),
            };
            Ok(Response::new(Box::pin(
                tokio_stream::once(Ok(snapshot)).chain(tokio_stream::pending()),
            )))
        }

        async fn register_inference_server(
            &self,
            request: Request<tonic::Streaming<InferenceServerRegistration>>,
        ) -> Result<Response<Self::RegisterInferenceServerStream>, Status> {
            self.register_calls.fetch_add(1, Ordering::SeqCst);
            let mut registrations = request.into_inner();
            let observed_registrations = self.registrations.clone();
            tokio::spawn(async move {
                if let Ok(Some(registration)) = registrations.message().await {
                    let _ = observed_registrations.send(registration);
                }
            });
            Ok(Response::new(Box::pin(
                tokio_stream::once(Ok(InferenceServerAck::default()))
                    .chain(tokio_stream::pending()),
            )))
        }
    }

    struct TestControlPlane {
        addr: SocketAddr,
        watch_calls: Arc<AtomicUsize>,
        register_calls: Arc<AtomicUsize>,
        registrations: mpsc::UnboundedReceiver<InferenceServerRegistration>,
        task: tokio::task::JoinHandle<()>,
    }

    impl TestControlPlane {
        async fn spawn() -> Self {
            let listener = TcpListener::bind("127.0.0.1:0")
                .await
                .expect("test control plane should bind");
            let addr = listener.local_addr().expect("test control plane address");
            let watch_calls = Arc::new(AtomicUsize::new(0));
            let register_calls = Arc::new(AtomicUsize::new(0));
            let (registrations, registrations_rx) = mpsc::unbounded_channel();
            let service = TestControlPlaneService {
                address: format!("http://{addr}"),
                watch_calls: watch_calls.clone(),
                register_calls: register_calls.clone(),
                registrations,
            };
            let task = tokio::spawn(async move {
                tonic::transport::Server::builder()
                    .add_service(StargateControlPlaneServer::new(service))
                    .serve_with_incoming(TcpListenerStream::new(listener))
                    .await
                    .expect("test control plane should serve");
            });
            Self {
                addr,
                watch_calls,
                register_calls,
                registrations: registrations_rx,
                task,
            }
        }

        fn assert_no_calls(&self) {
            assert_eq!(self.watch_calls.load(Ordering::SeqCst), 0);
            assert_eq!(self.register_calls.load(Ordering::SeqCst), 0);
        }

        async fn first_registration(&mut self) -> InferenceServerRegistration {
            match tokio::time::timeout(Duration::from_secs(2), self.registrations.recv()).await {
                Ok(Some(registration)) => registration,
                result => panic!(
                    "registration should arrive: result={result:?}, watch_calls={}, register_calls={}",
                    self.watch_calls.load(Ordering::SeqCst),
                    self.register_calls.load(Ordering::SeqCst),
                ),
            }
        }

        async fn shutdown(self) {
            self.task.abort();
            let _ = self.task.await;
        }
    }

    fn test_forwarding(plan: &PylonStartupPlan) -> TunnelForwardingConfig {
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        tunnel_forwarding_config_from_plan(plan, PylonRuntimeState::default(), metrics)
    }

    fn test_observation() -> RequestObservation {
        RequestObservation {
            endpoint: RequestObservationEndpoint::ChatCompletions,
            request_id: "request-a".to_string(),
            routing_key: None,
            model_id: "model-a".to_string(),
            priority: 0,
            input_tokens: 8,
            embedding_items: 0,
            embedding_items_observed: false,
            upstream_status: None,
            output_messages: 0,
            output_tokens: 0,
            output_tokens_explicit: false,
            output_tokens_from_chunk_usage: false,
            state: RequestObservationState::Queued,
            time_to_response_headers: None,
            time_to_first_output: None,
            time_to_first_token: None,
            total_duration: Duration::ZERO,
        }
    }

    async fn receive_queued_model_stats(
        runtime_state: &PylonRuntimeState,
        model_id: &str,
    ) -> pylon_lib::CurrentModelStats {
        tokio::time::timeout(Duration::from_secs(1), async {
            let mut poll = tokio::time::interval(Duration::from_millis(1));
            loop {
                poll.tick().await;
                if let Some(stats) = runtime_state.model_stats(model_id)
                    && stats.queue_size > 0
                {
                    break stats;
                }
            }
        })
        .await
        .expect("model stats should arrive")
    }

    async fn test_running_pylon(stats_collector: StatsCollectorHandle) -> RunningPylon {
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let model_lifecycle = start_model_lifecycle(
            ModelLifecycleConfig {
                upstream_http_base_url: "http://127.0.0.1:1".to_string(),
                source: ModelSource::Static(BTreeSet::new()),
                initialization: ModelInitialization::ConfiguredInputTps {
                    input_tps: 1.0,
                    pin: false,
                },
                bringup: BringupConfig {
                    enabled: false,
                    ..BringupConfig::default()
                },
            },
            PylonRuntimeState::default(),
            &stats_collector,
            None,
        )
        .await
        .expect("test model lifecycle should start");
        RunningPylon {
            registration_client: InferenceServerRegistrationClient::default(),
            engine_stats_stream: None,
            stats_collector,
            model_lifecycle,
            metrics_server: start_metrics_server(
                "127.0.0.1:0".parse().expect("metrics address should parse"),
                metrics.registry(),
            )
            .await
            .expect("metrics server should start"),
            tunnel: None,
            registration_inference_server_url: "quic://127.0.0.1:4567".to_string(),
            initial_model_ids: Vec::new(),
        }
    }

    fn active_test_stats_collector() -> StatsCollectorHandle {
        let config = StatsCollectorConfig::default();
        let (runtime_state, request_observation_rx) = PylonRuntimeState::observed(
            InferenceServerStatus::Unknown,
            &[],
            config.observation_channel_capacity,
            None,
        );
        start_stats_collector_with_engine_stats(config, request_observation_rx, None, runtime_state)
    }

    #[test]
    fn engine_stats_runtime_off_mode_leaves_stats_updates_unclaimed() {
        let (args, plan) = startup(&["--engine-stats-stream", "off"]);
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let config = StatsCollectorConfig::default();
        assert!(
            start_engine_stats_runtime(
                &args,
                &plan,
                metrics,
                &config,
                PylonRuntimeState::default(),
            )
            .is_none()
        );
    }

    #[tokio::test]
    async fn engine_stats_runtime_required_mode_claims_stats_updates() {
        let (args, plan) = startup(&["--engine-stats-stream", "required"]);
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let config = StatsCollectorConfig::default();
        let (engine_stats_stream, _stats_update_rx) = start_engine_stats_runtime(
            &args,
            &plan,
            metrics,
            &config,
            PylonRuntimeState::default(),
        )
        .expect("required engine stats should start a stream task");
        engine_stats_stream.handle.shutdown().await;
    }

    #[test]
    fn only_successful_auto_engine_stats_completion_is_nonfatal() {
        let completed = Ok(());

        assert!(engine_stats_exit_is_expected(
            Some(EngineStatsStreamMode::Auto),
            &completed,
        ));
        assert!(!engine_stats_exit_is_expected(
            Some(EngineStatsStreamMode::Required),
            &completed,
        ));
        assert!(!engine_stats_exit_is_expected(None, &completed));
    }

    #[tokio::test]
    async fn reverse_mode_direct_tunnel_startup_returns_no_tunnel_without_binding() {
        let (args, plan) = startup(&["--backend-connectivity", "reverse"]);
        let forwarding = test_forwarding(&plan);
        let tunnel = start_direct_tunnel_from_plan(&args, &plan, &forwarding, None, None)
            .await
            .expect("reverse mode should not start a direct tunnel");

        assert!(tunnel.is_none());
        assert_eq!(registration_url(&plan, tunnel.as_ref()), plan.upstream);
    }

    #[tokio::test]
    async fn direct_mode_direct_tunnel_startup_binds_and_reports_quic_url() {
        let (args, plan) = startup(&["--quic-listen-addr", "127.0.0.1:0"]);
        let forwarding = test_forwarding(&plan);
        let tunnel = start_direct_tunnel_from_plan(&args, &plan, &forwarding, None, None)
            .await
            .expect("direct mode should bind a direct tunnel")
            .expect("direct mode should return the tunnel handle");

        assert!(registration_url(&plan, Some(&tunnel)).starts_with("quic://127.0.0.1:"));
        tunnel.shutdown().await;
    }

    #[test]
    fn direct_tunnel_config_from_plan_preserves_runtime_inputs() {
        let (args, plan) = startup(&[
            "--inference-server-id",
            "pylon-a",
            "--quic-listen-addr",
            "127.0.0.1:4567",
            "--tunnel-protocol",
            "webtransport",
            "--pylon-local-connect-failures-retryable=true",
            "--pylon-queue-mismatch-retry-enabled=false",
            "--collect-quality-metrics",
        ]);
        let forwarding = test_forwarding(&plan);
        let metrics = forwarding.metrics.clone().unwrap();

        let config = direct_tunnel_config(
            &args,
            &plan,
            &forwarding,
            Some(b"cert"),
            Some(b"key".to_vec()),
        )
        .expect("direct mode should build tunnel config");

        assert_eq!(config.listen_addr, "127.0.0.1:4567".parse().unwrap());
        assert_eq!(config.upstream_http_base_url, "http://127.0.0.1:8090");
        assert_eq!(config.inference_server_id.as_deref(), Some("pylon-a"));
        assert_eq!(config.tls_cert_pem.as_deref(), Some(&b"cert"[..]));
        assert_eq!(config.tls_key_pem.as_deref(), Some(&b"key"[..]));
        assert_eq!(
            config.tunnel_protocol,
            TunnelTransportProtocol::WebTransport
        );
        assert!(config.forwarding.retry.local_connect_failures_retryable);
        assert!(!config.forwarding.queue_mismatch_retry.enabled);
        assert!(
            config
                .forwarding
                .request_quality_monitor
                .collect_quality_metrics
        );
        assert!(Arc::ptr_eq(
            config.forwarding.metrics.as_ref().unwrap(),
            &metrics
        ));
    }

    #[test]
    fn direct_tunnel_config_is_absent_for_reverse_mode() {
        let (args, plan) = startup(&["--backend-connectivity", "reverse"]);
        let forwarding = test_forwarding(&plan);

        assert!(direct_tunnel_config(&args, &plan, &forwarding, None, None).is_none());
    }

    #[test]
    fn registration_config_from_plan_preserves_direct_registration_contract() {
        let (args, plan) = startup(&[
            "--stargate-address",
            "http://stargate:50071",
            "--inference-server-id",
            "pylon-a",
            "--min-update-interval-ms",
            "250",
        ]);
        let forwarding = test_forwarding(&plan);

        let config = registration_config_from_plan(
            &args,
            &plan,
            forwarding,
            "quic://127.0.0.1:4567".to_string(),
            None,
        );

        assert_eq!(config.seeds, ["http://stargate:50071"]);
        assert_eq!(config.inference_server_id, "pylon-a");
        assert_eq!(config.inference_server_url, "quic://127.0.0.1:4567");
        assert_eq!(config.min_update_interval, Duration::from_millis(250));
        assert!(!config.reverse_tunnel);
    }

    #[test]
    fn registration_config_from_plan_preserves_reverse_registration_contract() {
        let (args, plan) = startup(&[
            "--backend-connectivity",
            "reverse",
            "--stargate-address",
            "http://stargate:50071",
            "--inference-server-id",
            "pylon-a",
            "--cluster-id",
            "shared-cluster",
            "--min-update-interval-ms",
            "250",
            "--quic-insecure",
            "--tunnel-protocol",
            "http3",
            "--disable-bringup",
            "--active-canary-interval-ms",
            "123",
            "--canary-max-generation-threshold",
            "77",
            "--calibration-requests",
            "3",
            "--calibration-prompt-units",
            "512",
            "--calibration-max-concurrency",
            "2",
            "--bringup-canary-timeout-ms",
            "456",
            "--bringup-calibration-timeout-ms",
            "789",
            "--auth-token",
            "token-from-cli",
        ]);
        let forwarding = test_forwarding(&plan);
        let metrics = forwarding.metrics.clone().unwrap();

        let config = registration_config_from_plan(
            &args,
            &plan,
            forwarding,
            "http://127.0.0.1:8090".to_string(),
            Some(b"trusted reverse cert".to_vec()),
        );

        assert_eq!(config.seeds, ["http://stargate:50071"]);
        assert_eq!(config.inference_server_id, "pylon-a");
        assert_eq!(config.cluster_id, "shared-cluster");
        assert_eq!(config.inference_server_url, "http://127.0.0.1:8090");
        assert_eq!(config.min_update_interval, Duration::from_millis(250));
        assert!(config.reverse_tunnel);
        assert_eq!(
            config.tls_cert_pem.as_deref(),
            Some(&b"trusted reverse cert"[..])
        );
        assert!(config.quic_insecure);
        assert_eq!(config.tunnel_protocol, TunnelTransportProtocol::Http3);
        assert!(Arc::ptr_eq(
            config.forwarding.metrics.as_ref().unwrap(),
            &metrics
        ));
        assert!(matches!(
            config.auth_token_provider.as_deref(),
            Some(AuthTokenProvider::Static(token)) if token == "token-from-cli"
        ));
    }

    #[test]
    fn stats_config_uses_normalized_upstream() {
        let (args, plan) = startup(&[
            "--engine-stats-stream",
            "required",
            "--kv-cache-stats-path",
            "kv/live",
        ]);
        let stats = stats_collector_config_from_args(&args, &plan.upstream);

        assert_eq!(
            stats.kv_cache_stats_url.as_deref(),
            Some("http://127.0.0.1:8090/kv/live")
        );
        assert!(!stats.openai_fallback_stats_enabled);
    }

    #[tokio::test]
    async fn configured_input_tps_seeds_queue_estimates_before_engine_stats() {
        let (args, plan) = startup(&[]);
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let mut config = stats_collector_config_from_args(&args, &plan.upstream);
        config.openai_fallback_stats_enabled = true;
        let (runtime_state, request_observation_rx) = PylonRuntimeState::observed(
            InferenceServerStatus::Unknown,
            &[],
            config.observation_channel_capacity,
            Some(metrics),
        );
        let stats_collector = start_stats_collector_with_engine_stats(
            config,
            request_observation_rx,
            None,
            runtime_state.clone(),
        );
        let model_lifecycle = start_model_lifecycle(
            ModelLifecycleConfig {
                upstream_http_base_url: plan.upstream.clone(),
                source: ModelSource::Static(BTreeSet::from(["model-a".to_string()])),
                initialization: ModelInitialization::ConfiguredInputTps {
                    input_tps: 1_000.0,
                    pin: false,
                },
                bringup: BringupConfig {
                    enabled: false,
                    ..BringupConfig::default()
                },
            },
            runtime_state.clone(),
            &stats_collector,
            None,
        )
        .await
        .expect("configured generation should initialize");
        let mut observation = test_observation();
        observation.input_tokens = 1000;

        runtime_state.observe_request(observation);
        let stats = receive_queued_model_stats(&runtime_state, "model-a").await;

        assert_eq!(stats.queue_size, 1);
        assert_eq!(
            stats
                .queue_time_estimate_ms_by_priority
                .as_ref()
                .and_then(|estimates| estimates.get(&0)),
            Some(&1000)
        );
        model_lifecycle.shutdown().await;
        stats_collector.shutdown().await;
    }

    #[tokio::test]
    async fn running_pylon_shutdown_stops_owned_metrics_server() {
        let runtime = test_running_pylon(active_test_stats_collector()).await;

        tokio::time::timeout(Duration::from_secs(1), runtime.shutdown())
            .await
            .expect("owned metrics server should stop during shutdown");
    }

    #[tokio::test]
    async fn running_pylon_returns_error_when_stats_collector_exits() {
        let observation_rx = {
            let (_observation_tx, observation_rx) =
                flume::bounded::<pylon_lib::RequestObservationEvent>(1);
            observation_rx
        };
        let runtime = test_running_pylon(start_stats_collector_with_engine_stats(
            StatsCollectorConfig::default(),
            observation_rx,
            None,
            PylonRuntimeState::default(),
        ))
        .await;

        let error = tokio::time::timeout(
            Duration::from_secs(1),
            runtime.run_until_shutdown(std::future::pending::<std::io::Result<&'static str>>()),
        )
        .await
        .expect("critical stats exit should wake the runtime")
        .expect_err("critical stats exit should fail the runtime");

        assert!(error.to_string().contains("stats collector exited"));
    }

    #[tokio::test]
    async fn running_pylon_treats_sigterm_as_clean_shutdown() {
        let runtime = test_running_pylon(active_test_stats_collector()).await;

        tokio::time::timeout(
            Duration::from_secs(1),
            runtime.run_until_shutdown(async { Ok("SIGTERM") }),
        )
        .await
        .expect("SIGTERM shutdown should finish")
        .expect("SIGTERM should be a clean runtime exit");
    }

    #[tokio::test]
    async fn every_model_finishes_local_calibration_before_the_first_stargate_rpc() {
        let mut upstream = TestUpstream::spawn(false).await;
        let mut control_plane = TestControlPlane::spawn().await;
        let args = runtime_args(
            &upstream.base_url,
            control_plane.addr,
            &["model-a", "model-b"],
            &[
                "--do-calibration",
                "--calibration-requests",
                "1",
                "--calibration-prompt-units",
                "256",
                "--calibration-max-concurrency",
                "1",
                "--bringup-calibration-timeout-ms",
                "20",
            ],
        );
        let plan = PylonStartupPlan::from_args(&args).expect("startup plan should build");
        let startup = tokio::spawn(async move { start_pylon_runtime(&args, &plan).await });

        assert_eq!(
            upstream.calibration_started.recv().await.as_deref(),
            Some("model-a")
        );
        control_plane.assert_no_calls();
        upstream.calibration_release.add_permits(1);
        assert_eq!(
            upstream.calibration_started.recv().await.as_deref(),
            Some("model-b")
        );
        control_plane.assert_no_calls();
        upstream.calibration_release.add_permits(1);

        let registration = control_plane.first_registration().await;
        assert_eq!(upstream.calibration_plans.load(Ordering::SeqCst), 2);
        assert!(upstream.calibration_requests.load(Ordering::SeqCst) >= 4);
        assert_eq!(control_plane.watch_calls.load(Ordering::SeqCst), 1);
        assert_eq!(control_plane.register_calls.load(Ordering::SeqCst), 1);
        assert_eq!(registration.models.len(), 2);
        for model_id in ["model-a", "model-b"] {
            let input_tps = registration.models[model_id]
                .stats
                .as_ref()
                .expect("first heartbeat should contain stats")
                .last_mean_input_tps;
            assert!(input_tps.is_finite());
        }

        startup
            .await
            .expect("pylon startup task should not panic")
            .expect("pylon startup should succeed")
            .shutdown()
            .await;
        upstream.shutdown().await;
        control_plane.shutdown().await;
    }

    #[tokio::test]
    async fn duplicate_model_flags_run_one_calibration_plan() {
        let mut upstream = TestUpstream::spawn(false).await;
        let mut control_plane = TestControlPlane::spawn().await;
        let args = runtime_args(
            &upstream.base_url,
            control_plane.addr,
            &["model-a", "model-a"],
            &[
                "--do-calibration",
                "--calibration-requests",
                "1",
                "--calibration-prompt-units",
                "256",
                "--bringup-calibration-timeout-ms",
                "20",
            ],
        );
        let plan = PylonStartupPlan::from_args(&args).expect("startup plan should build");
        let startup = tokio::spawn(async move { start_pylon_runtime(&args, &plan).await });

        assert_eq!(
            upstream.calibration_started.recv().await.as_deref(),
            Some("model-a")
        );
        upstream.calibration_release.add_permits(1);
        let runtime = startup
            .await
            .expect("pylon startup task should not panic")
            .expect("pylon startup should succeed");
        let registration = control_plane.first_registration().await;

        assert_eq!(upstream.calibration_plans.load(Ordering::SeqCst), 1);
        assert!(upstream.calibration_requests.load(Ordering::SeqCst) >= 2);
        assert_eq!(registration.models.len(), 1);
        assert!(registration.models.contains_key("model-a"));

        runtime.shutdown().await;
        upstream.shutdown().await;
        control_plane.shutdown().await;
    }

    #[tokio::test]
    async fn invalid_bootstrap_source_selection_makes_zero_stargate_calls() {
        let upstream = TestUpstream::spawn(false).await;
        let control_plane = TestControlPlane::spawn().await;

        for bootstrap_args in [
            Vec::<&str>::new(),
            vec!["--do-calibration", "--initial-input-tps", "100"],
        ] {
            let args = runtime_args(
                &upstream.base_url,
                control_plane.addr,
                &["model-a"],
                &bootstrap_args,
            );
            assert!(PylonStartupPlan::from_args(&args).is_err());
            control_plane.assert_no_calls();
        }

        upstream.shutdown().await;
        control_plane.shutdown().await;
    }

    #[tokio::test]
    async fn initial_calibration_request_error_returns_before_any_stargate_rpc() {
        let mut upstream = TestUpstream::spawn(true).await;
        let control_plane = TestControlPlane::spawn().await;
        let args = runtime_args(
            &upstream.base_url,
            control_plane.addr,
            &["model-a"],
            &[
                "--do-calibration",
                "--calibration-requests",
                "1",
                "--calibration-prompt-units",
                "256",
            ],
        );
        let plan = PylonStartupPlan::from_args(&args).expect("startup plan should build");
        let startup = tokio::spawn(async move { start_pylon_runtime(&args, &plan).await });

        assert_eq!(
            upstream.calibration_started.recv().await.as_deref(),
            Some("model-a")
        );
        control_plane.assert_no_calls();
        upstream.calibration_release.add_permits(1);
        let error = match startup.await.expect("pylon startup task should not panic") {
            Ok(runtime) => {
                runtime.shutdown().await;
                panic!("calibration request error must fail startup");
            }
            Err(error) => error,
        };

        assert!(
            error
                .to_string()
                .contains("pylon initial model initialization failed")
        );
        control_plane.assert_no_calls();
        upstream.shutdown().await;
        control_plane.shutdown().await;
    }

    #[tokio::test]
    async fn initial_discovery_failure_returns_before_any_stargate_rpc() {
        let control_plane = TestControlPlane::spawn().await;
        let args = runtime_args(
            "http://127.0.0.1:1",
            control_plane.addr,
            &[],
            &["--initial-input-tps", "100"],
        );
        let plan = PylonStartupPlan::from_args(&args).expect("startup plan should build");

        let error = match start_pylon_runtime(&args, &plan).await {
            Ok(runtime) => {
                runtime.shutdown().await;
                panic!("initial discovery failure must fail startup");
            }
            Err(error) => error,
        };

        assert!(
            error
                .to_string()
                .contains("initial model initialization failed")
        );
        control_plane.assert_no_calls();
        control_plane.shutdown().await;
    }

    #[tokio::test]
    async fn valid_empty_initial_discovery_registers_an_empty_snapshot() {
        let upstream = TestUpstream::spawn(false).await;
        let mut control_plane = TestControlPlane::spawn().await;
        let args = runtime_args(
            &upstream.base_url,
            control_plane.addr,
            &[],
            &["--initial-input-tps", "100"],
        );
        let plan = PylonStartupPlan::from_args(&args).expect("startup plan should build");

        let runtime = start_pylon_runtime(&args, &plan)
            .await
            .expect("valid empty discovery should start pylon");
        let registration = control_plane.first_registration().await;

        assert!(registration.models.is_empty());
        runtime.shutdown().await;
        upstream.shutdown().await;
        control_plane.shutdown().await;
    }

    #[tokio::test]
    async fn initial_input_tps_seeds_the_first_heartbeat_without_calibration_requests() {
        let upstream = TestUpstream::spawn(false).await;
        let mut control_plane = TestControlPlane::spawn().await;
        let args = runtime_args(
            &upstream.base_url,
            control_plane.addr,
            &["model-a", "model-b"],
            &["--initial-input-tps", "123.5"],
        );
        let plan = PylonStartupPlan::from_args(&args).expect("startup plan should build");
        let runtime = start_pylon_runtime(&args, &plan)
            .await
            .expect("pylon startup should succeed");

        let registration = control_plane.first_registration().await;
        assert_eq!(upstream.calibration_requests.load(Ordering::SeqCst), 0);
        assert_eq!(registration.models.len(), 2);
        for model_id in ["model-a", "model-b"] {
            assert_eq!(
                registration.models[model_id]
                    .stats
                    .as_ref()
                    .expect("first heartbeat should contain stats")
                    .last_mean_input_tps,
                123.5
            );
        }

        runtime.shutdown().await;
        upstream.shutdown().await;
        control_plane.shutdown().await;
    }

    #[test]
    fn auth_token_file_is_used_when_static_token_is_absent() {
        let (_, plan) = startup(&["--auth-token-file", "/tmp/pylon-token"]);

        assert!(matches!(
            plan.auth_token_provider.as_deref(),
            Some(AuthTokenProvider::File(path)) if path == std::path::Path::new("/tmp/pylon-token")
        ));
    }
}
