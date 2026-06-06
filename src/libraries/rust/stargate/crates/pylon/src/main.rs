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

use std::net::SocketAddr;
use std::str::FromStr;
use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, Result, ensure};
use pylon_lib::{
    AuthTokenProvider, BringupConfig, EngineStatsStreamConfig, EngineStatsStreamMode,
    InferenceServerRegistrationClient, InferenceServerRegistrationConfig, OutputTokenParserFactory,
    PylonMetrics, PylonQueueMismatchRetryConfig, PylonRetryConfig, QueueAdmissionTracker,
    QuicHttpTunnelConfig, RequestQualityMonitorConfig, StatsCollectorConfig,
    TunnelTransportProtocol, request_observation_channel, start_engine_stats_stream,
    start_metrics_server, start_quic_http_tunnel, start_stats_collector_with_engine_stats,
    stats_aggregator_update_channel,
};
use reqwest::header::HeaderName;
use stargate_proto::pb::InferenceServerStatus;
use stargate_protocol::tunnel_contract::HEADER_STARGATE_UPSTREAM_RETRYABLE;
use tracing::info;

const DEFAULT_PYLON_RETRYABLE_UPSTREAM_STATUS_CODES: &str = "429,503";
const DEFAULT_PYLON_UPSTREAM_RETRY_HEADER: &str = HEADER_STARGATE_UPSTREAM_RETRYABLE;

mod telemetry;

struct DirectTunnelConfigParams {
    listen_addr: SocketAddr,
    upstream_http_base_url: String,
    inference_server_id: String,
    tls_cert_pem: Option<Vec<u8>>,
    tls_key_pem: Option<Vec<u8>>,
    quic_insecure: bool,
    tunnel_protocol: TunnelTransportProtocol,
    retry: PylonRetryConfig,
    queue_mismatch_retry: PylonQueueMismatchRetryConfig,
    queue_tracker: QueueAdmissionTracker,
    request_quality_monitor: RequestQualityMonitorConfig,
    metrics: Arc<PylonMetrics>,
}

fn build_direct_tunnel_config(params: DirectTunnelConfigParams) -> QuicHttpTunnelConfig {
    let mut tunnel_config =
        QuicHttpTunnelConfig::new(params.listen_addr, params.upstream_http_base_url);
    tunnel_config.inference_server_id = Some(params.inference_server_id);
    tunnel_config.tls_cert_pem = params.tls_cert_pem;
    tunnel_config.tls_key_pem = params.tls_key_pem;
    tunnel_config.quic_insecure = params.quic_insecure;
    tunnel_config.tunnel_protocol = params.tunnel_protocol;
    tunnel_config.retry = params.retry;
    tunnel_config.queue_mismatch_retry = params.queue_mismatch_retry;
    tunnel_config.queue_tracker = params.queue_tracker;
    tunnel_config.request_quality_monitor = params.request_quality_monitor;
    tunnel_config.metrics = Some(params.metrics);
    tunnel_config
}

fn stats_collector_config_from_args(
    args: &Args,
    upstream: &str,
    metrics: Arc<PylonMetrics>,
    queue_tracker: QueueAdmissionTracker,
    fixed_last_mean_input_tps: Option<f64>,
) -> StatsCollectorConfig {
    let mut metrics_config = StatsCollectorConfig {
        configured_model_ids: args.model_name.clone(),
        fixed_last_mean_input_tps,
        openai_fallback_stats_enabled: args.engine_stats_stream == EngineStatsStreamMode::Off,
        metrics: Some(metrics),
        queue_tracker,
        ..Default::default()
    };
    if let Some(path) = args.kv_cache_stats_path.as_deref() {
        // Mock benchmark backends can expose live KV-cache occupancy over HTTP;
        // real upstreams usually do not, so polling is explicit.
        metrics_config.kv_cache_stats_url = Some(join_base_url_path(upstream, path));
    }
    metrics_config
}

fn join_base_url_path(base_url: &str, path: &str) -> String {
    format!(
        "{}/{}",
        base_url.trim_end_matches('/'),
        path.trim_start_matches('/')
    )
}

#[derive(clap::Parser, Debug)]
#[command(name = "pylon")]
struct Args {
    /// Base URL of the upstream HTTP inference server (for example http://127.0.0.1:8090)
    #[arg(long, value_name = "URL")]
    upstream_http_base_url: String,

    /// QUIC tunnel listen address (advertised to stargate in forward mode)
    #[arg(long, default_value = "127.0.0.1:0", value_name = "ADDR")]
    quic_listen_addr: String,

    /// Model IDs to register (repeatable, e.g. --model-name a --model-name b)
    #[arg(long, default_value = "dummy-model", value_name = "MODEL")]
    model_name: Vec<String>,

    /// Stargate gRPC address for registration
    #[arg(long, default_value = "127.0.0.1:50071", value_name = "ADDR")]
    stargate_address: String,

    /// Inference server id for registration
    #[arg(long, default_value = "pylon", value_name = "ID")]
    inference_server_id: String,

    /// Logical cluster id for registration. Defaults to inference-server-id.
    #[arg(long, value_name = "ID")]
    cluster_id: Option<String>,

    /// Path to TLS certificate PEM for the QUIC tunnel (generates self-signed if omitted)
    #[arg(long, env = "STARGATE_TLS_CERT_PATH", value_name = "PATH")]
    tls_cert_path: Option<String>,

    /// Path to TLS private key PEM for the QUIC tunnel (generates self-signed if omitted)
    #[arg(long, env = "STARGATE_TLS_KEY_PATH", value_name = "PATH")]
    tls_key_path: Option<String>,

    /// Skip QUIC TLS certificate verification for reverse tunnel connections
    #[arg(long, default_value_t = false, env = "STARGATE_QUIC_INSECURE")]
    quic_insecure: bool,

    /// Discover reverse tunnel targets from InferenceServerAck and connect to stargate
    #[arg(long, default_value_t = false)]
    reverse_tunnel: bool,

    /// Tunnel protocol used for proxied request streams
    #[arg(long, default_value_t = TunnelTransportProtocol::Custom, value_name = "PROTOCOL")]
    tunnel_protocol: TunnelTransportProtocol,

    /// Disable client-side bringup calibration and active canaries
    #[arg(long, default_value_t = false)]
    disable_bringup: bool,

    /// Interval between active canary requests in milliseconds. `0` disables active canaries
    #[arg(long, default_value_t = 5000, value_name = "MS")]
    active_canary_interval_ms: u64,

    /// Treat canary responses that generate this many tokens as runaway generation
    #[arg(long, default_value_t = 237, value_name = "TOKENS")]
    canary_max_generation_threshold: u32,

    /// Number of calibration requests to send before advertising active
    #[arg(long, default_value_t = 5, value_name = "N")]
    calibration_requests: usize,

    /// Approximate prompt units used for calibration requests
    #[arg(long, default_value_t = 4096, value_name = "N")]
    calibration_prompt_units: usize,

    /// Maximum concurrent requests used during calibration
    #[arg(long, default_value_t = 4, value_name = "N")]
    calibration_max_concurrency: usize,

    /// Timeout for canary requests in milliseconds
    #[arg(long, default_value_t = 5000, value_name = "MS")]
    bringup_canary_timeout_ms: u64,

    /// Timeout for calibration requests in milliseconds
    #[arg(long, default_value_t = 30000, value_name = "MS")]
    bringup_calibration_timeout_ms: u64,

    /// Upstream HTTP path to poll for KV-cache stats. Omit to disable KV metric polling
    #[arg(long, value_name = "PATH")]
    kv_cache_stats_path: Option<String>,

    /// Engine stats stream source selection mode
    #[arg(long, default_value_t = EngineStatsStreamMode::Auto, value_name = "MODE")]
    engine_stats_stream: EngineStatsStreamMode,

    /// Upstream HTTP path for the engine stats stream
    #[arg(long, default_value = "/pylon/v1/stats/stream", value_name = "PATH")]
    engine_stats_stream_path: String,

    /// Pin input TPS for deterministic benchmark/test queue-estimation experiments
    #[arg(long, value_name = "TPS", hide = true)]
    benchmark_fixed_last_mean_input_tps: Option<f64>,

    /// Minimum interval between registration/stat updates to stargate
    #[arg(long, default_value_t = 1000, value_name = "MS")]
    min_update_interval_ms: u64,

    /// Static auth token for registration and reverse tunnel handshake
    #[arg(long, env = "STARGATE_AUTH_TOKEN", value_name = "TOKEN")]
    auth_token: Option<String>,

    /// Path to file containing the auth token (re-read on each use for rotation)
    #[arg(long, env = "STARGATE_AUTH_TOKEN_FILE", value_name = "PATH")]
    auth_token_file: Option<String>,

    /// Address for Prometheus metrics HTTP server
    #[arg(long, default_value = "0.0.0.0", value_name = "HOST")]
    metrics_host: String,

    /// Port for Prometheus metrics HTTP server
    #[arg(long, default_value_t = 9089, value_name = "PORT")]
    metrics_port: u16,

    /// OTLP/gRPC endpoint for trace export.
    #[arg(long, env = "OTEL_EXPORTER_OTLP_ENDPOINT", value_name = "URL")]
    otel_endpoint: Option<String>,

    /// OpenTelemetry service.name resource attribute
    #[arg(
        long,
        default_value = telemetry::DEFAULT_SERVICE_NAME,
        env = "OTEL_SERVICE_NAME",
        value_name = "NAME"
    )]
    otel_service_name: String,

    /// Comma-separated upstream HTTP statuses that can be marked retryable
    #[arg(
        long,
        default_value = DEFAULT_PYLON_RETRYABLE_UPSTREAM_STATUS_CODES,
        env = "PYLON_RETRYABLE_UPSTREAM_STATUS_CODES",
        value_name = "CODES"
    )]
    pylon_retryable_upstream_status_codes: String,

    /// Require the upstream retry header before marking retryable statuses retryable
    #[arg(
        long,
        action = clap::ArgAction::Set,
        default_value_t = true,
        env = "PYLON_REQUIRE_UPSTREAM_RETRY_HEADER"
    )]
    pylon_require_upstream_retry_header: bool,

    /// Upstream response header that authorizes retrying retryable status codes
    #[arg(
        long,
        default_value = DEFAULT_PYLON_UPSTREAM_RETRY_HEADER,
        env = "PYLON_UPSTREAM_RETRY_HEADER",
        value_name = "HEADER"
    )]
    pylon_upstream_retry_header: String,

    /// Convert upstream Retry-After responses into x-stargate-retry-after-ms
    #[arg(
        long,
        action = clap::ArgAction::Set,
        default_value_t = true,
        env = "PYLON_PROPAGATE_RETRY_AFTER"
    )]
    pylon_propagate_retry_after: bool,

    /// Mark local upstream connection failures as retryable
    #[arg(
        long,
        action = clap::ArgAction::Set,
        default_value_t = false,
        env = "PYLON_LOCAL_CONNECT_FAILURES_RETRYABLE"
    )]
    pylon_local_connect_failures_retryable: bool,

    /// Retry locally when Pylon's queue estimate exceeds Stargate's routing-time estimate
    #[arg(
        long,
        action = clap::ArgAction::Set,
        default_value_t = true,
        env = "PYLON_QUEUE_MISMATCH_RETRY_ENABLED"
    )]
    pylon_queue_mismatch_retry_enabled: bool,

    /// Minimum additive delta above Stargate's queue estimate before local retry
    #[arg(
        long,
        default_value_t = 25,
        env = "PYLON_QUEUE_MISMATCH_MIN_DELTA_MS",
        value_name = "MS"
    )]
    pylon_queue_mismatch_min_delta_ms: u64,

    /// Multiplicative tolerance above Stargate's queue estimate before local retry
    #[arg(
        long,
        default_value_t = 1.25,
        env = "PYLON_QUEUE_MISMATCH_TOLERANCE_FACTOR",
        value_name = "FACTOR"
    )]
    pylon_queue_mismatch_tolerance_factor: f64,

    /// Optional retry-after hint in milliseconds for local queue-mismatch retries
    #[arg(long, env = "PYLON_QUEUE_MISMATCH_RETRY_AFTER_MS", value_name = "MS")]
    pylon_queue_mismatch_retry_after_ms: Option<u64>,

    /// Collect post-stream output quality metrics (gibberish checks)
    #[arg(long, default_value_t = false)]
    collect_quality_metrics: bool,

    /// Minimum output tokens required before quality metrics and threshold checks run
    #[arg(long, default_value_t = 20, value_name = "TOKENS")]
    collect_quality_metrics_min_tokens: u32,

    /// Trigger quality event when observed output tokens exceed this threshold
    #[arg(long, value_name = "TOKENS")]
    quality_output_tokens_threshold_min: Option<u32>,

    /// Trigger quality event when compression ratio is below this threshold
    #[arg(long, value_name = "RATIO")]
    quality_output_compression_threshold_max: Option<f64>,

    /// Trigger quality event when degeneracy score exceeds this threshold
    #[arg(long, value_name = "SCORE")]
    quality_output_degeneracy_threshold_min: Option<f64>,

    /// Trigger quality event when repetition 1-gram score exceeds this threshold
    #[arg(long, value_name = "SCORE")]
    quality_output_repetition_1gram_threshold_min: Option<f64>,

    /// Trigger quality event when repetition 2-gram score exceeds this threshold
    #[arg(long, value_name = "SCORE")]
    quality_output_repetition_2gram_threshold_min: Option<f64>,

    /// Trigger quality event when repetition 3-gram score exceeds this threshold
    #[arg(long, value_name = "SCORE")]
    quality_output_repetition_3gram_threshold_min: Option<f64>,

    /// Trigger quality event when median logprob is below this threshold
    #[arg(long, value_name = "LOGPROB")]
    quality_median_logprob_threshold_max: Option<f32>,
}

#[tokio::main]
async fn main() -> Result<()> {
    let args = <Args as clap::Parser>::parse();
    run(args).await
}

async fn run(args: Args) -> Result<()> {
    let _telemetry_guard =
        telemetry::init_telemetry(args.otel_endpoint.as_deref(), &args.otel_service_name)?;
    let upstream = normalize_base_url(&args.upstream_http_base_url);
    let cluster_id = effective_cluster_id(&args);
    let pylon_retry = pylon_retry_config_from_args(&args)?;
    let queue_mismatch_retry = pylon_queue_mismatch_retry_config_from_args(&args)?;
    let fixed_last_mean_input_tps = benchmark_fixed_last_mean_input_tps_from_args(&args)?;
    let queue_tracker = QueueAdmissionTracker::default();
    if let Some(last_mean_input_tps) = fixed_last_mean_input_tps {
        for model_id in &args.model_name {
            queue_tracker.update_model_throughput(model_id, last_mean_input_tps);
        }
    }
    let request_quality_monitor = request_quality_monitor_config_from_args(&args);

    let metrics = PylonMetrics::new()?;
    metrics.observe_target_info(
        env!("CARGO_PKG_VERSION"),
        env!("CARGO_PKG_NAME"),
        option_env!("GIT_COMMIT_HASH")
            .or(option_env!("GIT_COMMIT_SHA"))
            .unwrap_or(""),
    );
    let metrics_addr: SocketAddr =
        format!("{}:{}", args.metrics_host, args.metrics_port).parse()?;
    let metrics_task = tokio::spawn({
        let registry = metrics.registry();
        async move {
            if let Err(error) = start_metrics_server(metrics_addr, registry).await {
                tracing::error!(error = %error, "pylon metrics server failed");
            }
        }
    });
    let metrics_config = stats_collector_config_from_args(
        &args,
        &upstream,
        metrics.clone(),
        queue_tracker.clone(),
        fixed_last_mean_input_tps,
    );
    let (request_observation_tx, request_observation_rx) =
        request_observation_channel(&metrics_config);
    let (stats_update_tx, stats_update_rx) = stats_aggregator_update_channel(&metrics_config);
    let (_metrics_stop_tx, metrics_stop_rx) = tokio::sync::watch::channel(false);
    let mut engine_stats_stream_config = EngineStatsStreamConfig::new(
        &upstream,
        &args.engine_stats_stream_path,
        args.engine_stats_stream,
    );
    engine_stats_stream_config.metrics = Some(metrics.clone());
    let engine_stats_stream = start_engine_stats_stream(
        engine_stats_stream_config,
        stats_update_tx,
        metrics_stop_rx.clone(),
    );
    let stats_update_rx = engine_stats_stream.as_ref().map(|_| stats_update_rx);

    let tls_cert_pem = args.tls_cert_path.as_ref().map(std::fs::read).transpose()?;
    let tls_key_pem = args.tls_key_path.as_ref().map(std::fs::read).transpose()?;

    let auth_token_provider = if let Some(token) = args.auth_token {
        Some(Arc::new(AuthTokenProvider::Static(token)))
    } else {
        args.auth_token_file
            .map(|path| Arc::new(AuthTokenProvider::File(path.into())))
    };

    let reverse_mode = args.reverse_tunnel;
    let tunnel = if reverse_mode {
        None
    } else {
        let quic_addr: SocketAddr = args.quic_listen_addr.parse()?;
        let mut tunnel_config = build_direct_tunnel_config(DirectTunnelConfigParams {
            listen_addr: quic_addr,
            upstream_http_base_url: upstream.clone(),
            inference_server_id: args.inference_server_id.clone(),
            tls_cert_pem,
            tls_key_pem,
            quic_insecure: args.quic_insecure,
            tunnel_protocol: args.tunnel_protocol,
            retry: pylon_retry.clone(),
            queue_mismatch_retry: queue_mismatch_retry.clone(),
            queue_tracker: queue_tracker.clone(),
            request_quality_monitor: request_quality_monitor.clone(),
            metrics: metrics.clone(),
        });
        tunnel_config.request_observation_tx = Some(request_observation_tx.clone());
        let t = start_quic_http_tunnel(tunnel_config).await?;
        info!(addr = %t.listen_addr(), url = %format!("quic://{}", t.listen_addr()), "QUIC tunnel listening");
        Some(t)
    };

    let quic_url = tunnel
        .as_ref()
        .map(|t| format!("quic://{}", t.listen_addr()))
        .unwrap_or_default();

    let mut registration_client = InferenceServerRegistrationClient::default();
    let channels = registration_client.start(
        InferenceServerRegistrationConfig {
            seeds: vec![args.stargate_address.clone()],
            inference_server_id: args.inference_server_id.clone(),
            cluster_id,
            inference_server_url: if reverse_mode {
                upstream.clone()
            } else {
                quic_url.clone()
            },
            upstream_http_base_url: Some(upstream.clone()),
            min_update_interval: Duration::from_millis(args.min_update_interval_ms),
            status: InferenceServerStatus::Active,
            reverse_tunnel: reverse_mode,
            quic_insecure: args.quic_insecure,
            tunnel_protocol: args.tunnel_protocol,
            bringup: BringupConfig {
                enabled: !args.disable_bringup,
                active_canary_interval: Duration::from_millis(args.active_canary_interval_ms),
                canary_timeout: Duration::from_millis(args.bringup_canary_timeout_ms),
                canary_max_generation_threshold: args.canary_max_generation_threshold,
                calibration_requests: args.calibration_requests,
                calibration_prompt_units: args.calibration_prompt_units,
                calibration_max_concurrency: args.calibration_max_concurrency,
                calibration_timeout: Duration::from_millis(args.bringup_calibration_timeout_ms),
            },
            output_token_parser_factory: OutputTokenParserFactory::vllm(),
            request_observation_tx: Some(request_observation_tx),
            request_quality_monitor,
            metrics: Some(metrics),
            retry: pylon_retry,
            queue_mismatch_retry,
            queue_tracker,
            auth_token_provider,
        },
        args.model_name.clone(),
    )?;
    let stats_collector = start_stats_collector_with_engine_stats(
        metrics_config,
        request_observation_rx,
        stats_update_rx,
        channels.model_stats.clone(),
        metrics_stop_rx,
    );

    if reverse_mode {
        info!(
            stargate = %args.stargate_address,
            inference_server_id = %args.inference_server_id,
            upstream = %upstream,
            "registered with stargate (reverse tunnel mode)"
        );
    } else {
        info!(
            stargate = %args.stargate_address,
            inference_server_id = %args.inference_server_id,
            inference_server_url = %quic_url,
            upstream = %upstream,
            "registered with stargate"
        );
    }

    info!("pylon running; Ctrl+C to exit");

    tokio::signal::ctrl_c().await?;
    info!("shutting down");

    registration_client.shutdown().await;
    if let Some(engine_stats_stream) = engine_stats_stream {
        engine_stats_stream.shutdown().await;
    }
    stats_collector.shutdown().await;
    metrics_task.abort();
    let _ = metrics_task.await;
    if let Some(t) = tunnel {
        t.shutdown().await;
    }

    Ok(())
}

fn normalize_base_url(url: &str) -> String {
    url.trim_end_matches('/').to_string()
}

fn effective_cluster_id(args: &Args) -> String {
    args.cluster_id
        .clone()
        .unwrap_or_else(|| args.inference_server_id.clone())
}

fn pylon_retry_config_from_args(args: &Args) -> Result<PylonRetryConfig> {
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

fn pylon_queue_mismatch_retry_config_from_args(
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

fn benchmark_fixed_last_mean_input_tps_from_args(args: &Args) -> Result<Option<f64>> {
    if let Some(last_mean_input_tps) = args.benchmark_fixed_last_mean_input_tps {
        ensure!(
            last_mean_input_tps.is_finite() && last_mean_input_tps > 0.0,
            "benchmark fixed last mean input TPS must be finite and positive"
        );
    }
    Ok(args.benchmark_fixed_last_mean_input_tps)
}

fn parse_retryable_status_codes(value: &str) -> Result<Vec<reqwest::StatusCode>> {
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

fn request_quality_monitor_config_from_args(args: &Args) -> RequestQualityMonitorConfig {
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
    use super::*;

    fn parse_args(extra: &[&str]) -> Args {
        let mut args = vec![
            "pylon",
            "--upstream-http-base-url",
            "http://127.0.0.1:8090/",
        ];
        args.extend_from_slice(extra);
        <Args as clap::Parser>::try_parse_from(args).expect("args should parse")
    }

    #[test]
    fn cli_command_is_named_pylon() {
        assert_eq!(
            <Args as clap::CommandFactory>::command().get_name(),
            "pylon"
        );
    }

    #[test]
    fn otel_endpoint_help_matches_grpc_exporter_transport() {
        let mut command = <Args as clap::CommandFactory>::command();
        let mut help = Vec::new();
        command
            .write_long_help(&mut help)
            .expect("help should render");
        let help = std::str::from_utf8(&help).expect("help should be UTF-8");

        assert!(help.contains("OTLP/gRPC endpoint for trace export"));
        assert!(!help.contains("OTLP/HTTP/protobuf endpoint for trace export"));
    }

    #[test]
    fn inference_server_id_defaults_to_pylon() {
        let args = parse_args(&[]);

        assert_eq!(args.inference_server_id, "pylon");
    }

    #[test]
    fn pylon_retry_cli_defaults_match_runtime_defaults() {
        let args = parse_args(&[]);
        let retry = pylon_retry_config_from_args(&args).expect("retry config should parse");
        let defaults = PylonRetryConfig::default();

        assert_eq!(
            retry.retryable_upstream_status_codes,
            defaults.retryable_upstream_status_codes
        );
        assert_eq!(
            retry.require_upstream_retry_header,
            defaults.require_upstream_retry_header
        );
        assert_eq!(retry.upstream_retry_header, defaults.upstream_retry_header);
        assert_eq!(retry.propagate_retry_after, defaults.propagate_retry_after);
        assert_eq!(
            retry.local_connect_failures_retryable,
            defaults.local_connect_failures_retryable
        );
    }

    #[test]
    fn pylon_retry_cli_overrides_are_applied() {
        let args = parse_args(&[
            "--pylon-retryable-upstream-status-codes",
            "418, 429,503",
            "--pylon-require-upstream-retry-header=false",
            "--pylon-upstream-retry-header",
            "x-can-retry",
            "--pylon-propagate-retry-after=false",
            "--pylon-local-connect-failures-retryable=true",
        ]);
        let retry = pylon_retry_config_from_args(&args).expect("retry config should parse");

        assert_eq!(
            retry.retryable_upstream_status_codes,
            vec![
                reqwest::StatusCode::IM_A_TEAPOT,
                reqwest::StatusCode::TOO_MANY_REQUESTS,
                reqwest::StatusCode::SERVICE_UNAVAILABLE,
            ]
        );
        assert!(!retry.require_upstream_retry_header);
        assert_eq!(
            retry.upstream_retry_header,
            HeaderName::from_static("x-can-retry")
        );
        assert!(!retry.propagate_retry_after);
        assert!(retry.local_connect_failures_retryable);
    }

    #[test]
    fn empty_pylon_retryable_status_codes_disable_status_retries() {
        let args = parse_args(&["--pylon-retryable-upstream-status-codes", ""]);
        let retry = pylon_retry_config_from_args(&args).expect("retry config should parse");

        assert!(retry.retryable_upstream_status_codes.is_empty());
    }

    #[test]
    fn pylon_queue_mismatch_retry_cli_defaults_match_runtime_defaults() {
        let args = parse_args(&[]);
        let config = pylon_queue_mismatch_retry_config_from_args(&args)
            .expect("queue mismatch config should parse");
        let defaults = PylonQueueMismatchRetryConfig::default();

        assert_eq!(config.enabled, defaults.enabled);
        assert_eq!(config.min_delta_ms, defaults.min_delta_ms);
        assert_eq!(config.tolerance_factor, defaults.tolerance_factor);
        assert_eq!(config.retry_after_ms, defaults.retry_after_ms);
    }

    #[test]
    fn pylon_queue_mismatch_retry_cli_overrides_are_applied() {
        let args = parse_args(&[
            "--pylon-queue-mismatch-retry-enabled=false",
            "--pylon-queue-mismatch-min-delta-ms",
            "50",
            "--pylon-queue-mismatch-tolerance-factor",
            "1.5",
            "--pylon-queue-mismatch-retry-after-ms",
            "250",
        ]);
        let config = pylon_queue_mismatch_retry_config_from_args(&args)
            .expect("queue mismatch config should parse");

        assert!(!config.enabled);
        assert_eq!(config.min_delta_ms, 50);
        assert_eq!(config.tolerance_factor, 1.5);
        assert_eq!(config.retry_after_ms, Some(250));
    }

    #[test]
    fn invalid_pylon_queue_mismatch_tolerance_factor_is_rejected() {
        let args = parse_args(&["--pylon-queue-mismatch-tolerance-factor", "0"]);

        assert!(pylon_queue_mismatch_retry_config_from_args(&args).is_err());
    }

    #[test]
    fn benchmark_fixed_input_tps_is_validated_and_added_to_stats_config() {
        let args = parse_args(&["--benchmark-fixed-last-mean-input-tps", "2200"]);
        let fixed_last_mean_input_tps = benchmark_fixed_last_mean_input_tps_from_args(&args)
            .expect("fixed benchmark input TPS should parse");
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let config = stats_collector_config_from_args(
            &args,
            "http://127.0.0.1:8090",
            metrics,
            QueueAdmissionTracker::default(),
            fixed_last_mean_input_tps,
        );

        assert_eq!(config.fixed_last_mean_input_tps, Some(2_200.0));
    }

    #[test]
    fn invalid_benchmark_fixed_input_tps_is_rejected() {
        let args = parse_args(&["--benchmark-fixed-last-mean-input-tps", "0"]);

        assert!(benchmark_fixed_last_mean_input_tps_from_args(&args).is_err());
    }

    #[test]
    fn engine_stats_stream_defaults_to_auto_mode_and_v1_path() {
        let args = parse_args(&[]);
        let upstream = normalize_base_url(&args.upstream_http_base_url);
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let metrics_config = stats_collector_config_from_args(
            &args,
            &upstream,
            metrics,
            QueueAdmissionTracker::default(),
            None,
        );

        assert_eq!(args.engine_stats_stream, EngineStatsStreamMode::Auto);
        assert_eq!(args.engine_stats_stream_path, "/pylon/v1/stats/stream");
        assert!(metrics_config.kv_cache_stats_url.is_none());
        assert!(
            !metrics_config.openai_fallback_stats_enabled,
            "auto mode should wait for a permanent unsupported stream response before fallback stats"
        );
    }

    #[test]
    fn engine_stats_stream_can_be_disabled() {
        let args = parse_args(&["--engine-stats-stream", "off"]);
        let upstream = normalize_base_url(&args.upstream_http_base_url);
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let metrics_config = stats_collector_config_from_args(
            &args,
            &upstream,
            metrics,
            QueueAdmissionTracker::default(),
            None,
        );

        assert_eq!(args.engine_stats_stream, EngineStatsStreamMode::Off);
        assert!(metrics_config.kv_cache_stats_url.is_none());
        assert!(metrics_config.openai_fallback_stats_enabled);
    }

    #[test]
    fn kv_cache_stats_path_enables_explicit_kv_cache_polling() {
        let args = parse_args(&["--kv-cache-stats-path", "/kv-cache/stats"]);
        let upstream = normalize_base_url(&args.upstream_http_base_url);
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let metrics_config = stats_collector_config_from_args(
            &args,
            &upstream,
            metrics,
            QueueAdmissionTracker::default(),
            None,
        );

        assert_eq!(
            metrics_config.kv_cache_stats_url,
            Some("http://127.0.0.1:8090/kv-cache/stats".to_string())
        );
    }

    #[test]
    fn required_engine_stats_stream_disables_openai_fallback_stats() {
        let args = parse_args(&["--engine-stats-stream", "required"]);
        let upstream = normalize_base_url(&args.upstream_http_base_url);
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let metrics_config = stats_collector_config_from_args(
            &args,
            &upstream,
            metrics,
            QueueAdmissionTracker::default(),
            None,
        );

        assert_eq!(args.engine_stats_stream, EngineStatsStreamMode::Required);
        assert!(!metrics_config.openai_fallback_stats_enabled);
    }

    #[test]
    fn cluster_id_defaults_to_inference_server_id() {
        let args = parse_args(&["--inference-server-id", "client-a"]);

        assert_eq!(effective_cluster_id(&args), "client-a");
    }

    #[test]
    fn cluster_id_can_be_set_independently() {
        let args = parse_args(&[
            "--inference-server-id",
            "client-a",
            "--cluster-id",
            "cluster-shared",
        ]);

        assert_eq!(effective_cluster_id(&args), "cluster-shared");
    }

    #[test]
    fn invalid_pylon_retryable_status_code_is_rejected() {
        let args = parse_args(&["--pylon-retryable-upstream-status-codes", "429,nope"]);

        assert!(pylon_retry_config_from_args(&args).is_err());
    }

    #[test]
    fn tunnel_protocol_cli_defaults_to_custom() {
        let args = parse_args(&[]);

        assert_eq!(args.tunnel_protocol, TunnelTransportProtocol::Custom);
    }

    #[test]
    fn tunnel_protocol_cli_accepts_http3() {
        let args = parse_args(&["--tunnel-protocol", "http3"]);

        assert_eq!(args.tunnel_protocol, TunnelTransportProtocol::Http3);
    }

    #[test]
    fn tunnel_protocol_cli_accepts_webtransport() {
        let args = parse_args(&["--tunnel-protocol", "webtransport"]);

        assert_eq!(args.tunnel_protocol, TunnelTransportProtocol::WebTransport);
    }

    #[test]
    fn direct_tunnel_config_propagates_metrics() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");

        let config = build_direct_tunnel_config(DirectTunnelConfigParams {
            listen_addr: "127.0.0.1:0".parse().unwrap(),
            upstream_http_base_url: "http://127.0.0.1:8090/".to_string(),
            inference_server_id: "inst-a".to_string(),
            tls_cert_pem: None,
            tls_key_pem: None,
            quic_insecure: true,
            tunnel_protocol: TunnelTransportProtocol::Http3,
            retry: PylonRetryConfig::default(),
            queue_mismatch_retry: PylonQueueMismatchRetryConfig::default(),
            queue_tracker: QueueAdmissionTracker::default(),
            request_quality_monitor: RequestQualityMonitorConfig::default(),
            metrics: metrics.clone(),
        });

        assert!(
            Arc::ptr_eq(config.metrics.as_ref().unwrap(), &metrics),
            "direct tunnel config should carry pylon metrics"
        );
        assert_eq!(config.tunnel_protocol, TunnelTransportProtocol::Http3);
    }

    #[test]
    fn direct_tunnel_config_propagates_request_quality_monitor() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let request_quality_monitor = RequestQualityMonitorConfig {
            collect_quality_metrics: true,
            collect_quality_metrics_min_tokens: 7,
            output_tokens_threshold_min: Some(9),
            output_compression_threshold_max: Some(0.4),
            output_degeneracy_threshold_min: Some(0.5),
            output_repetition_1gram_threshold_min: Some(0.6),
            output_repetition_2gram_threshold_min: Some(0.7),
            output_repetition_3gram_threshold_min: Some(0.8),
            median_logprob_threshold_max: Some(-6.5),
        };

        let config = build_direct_tunnel_config(DirectTunnelConfigParams {
            listen_addr: "127.0.0.1:0".parse().unwrap(),
            upstream_http_base_url: "http://127.0.0.1:8090/".to_string(),
            inference_server_id: "inst-a".to_string(),
            tls_cert_pem: None,
            tls_key_pem: None,
            quic_insecure: true,
            tunnel_protocol: TunnelTransportProtocol::Custom,
            retry: PylonRetryConfig::default(),
            queue_mismatch_retry: PylonQueueMismatchRetryConfig::default(),
            queue_tracker: QueueAdmissionTracker::default(),
            request_quality_monitor: request_quality_monitor.clone(),
            metrics,
        });

        assert!(config.request_quality_monitor.collect_quality_metrics);
        assert_eq!(
            config
                .request_quality_monitor
                .collect_quality_metrics_min_tokens,
            7
        );
        assert_eq!(
            config.request_quality_monitor.output_tokens_threshold_min,
            Some(9)
        );
        assert_eq!(
            config
                .request_quality_monitor
                .output_compression_threshold_max,
            Some(0.4)
        );
        assert_eq!(
            config
                .request_quality_monitor
                .output_degeneracy_threshold_min,
            Some(0.5)
        );
        assert_eq!(
            config
                .request_quality_monitor
                .output_repetition_1gram_threshold_min,
            Some(0.6)
        );
        assert_eq!(
            config
                .request_quality_monitor
                .output_repetition_2gram_threshold_min,
            Some(0.7)
        );
        assert_eq!(
            config
                .request_quality_monitor
                .output_repetition_3gram_threshold_min,
            Some(0.8)
        );
        assert_eq!(
            config.request_quality_monitor.median_logprob_threshold_max,
            Some(-6.5)
        );
    }

    #[test]
    fn quality_monitor_cli_overrides_are_applied() {
        let args = parse_args(&[
            "--collect-quality-metrics",
            "--collect-quality-metrics-min-tokens",
            "5",
            "--quality-output-tokens-threshold-min",
            "99",
            "--quality-output-compression-threshold-max",
            "0.3",
            "--quality-output-degeneracy-threshold-min",
            "0.5",
            "--quality-output-repetition-1gram-threshold-min",
            "0.7",
            "--quality-output-repetition-2gram-threshold-min",
            "0.8",
            "--quality-output-repetition-3gram-threshold-min",
            "0.9",
            "--quality-median-logprob-threshold-max=-6.5",
        ]);
        let config = request_quality_monitor_config_from_args(&args);

        assert!(config.collect_quality_metrics);
        assert_eq!(config.collect_quality_metrics_min_tokens, 5);
        assert_eq!(config.output_tokens_threshold_min, Some(99));
        assert_eq!(config.output_compression_threshold_max, Some(0.3));
        assert_eq!(config.output_degeneracy_threshold_min, Some(0.5));
        assert_eq!(config.output_repetition_1gram_threshold_min, Some(0.7));
        assert_eq!(config.output_repetition_2gram_threshold_min, Some(0.8));
        assert_eq!(config.output_repetition_3gram_threshold_min, Some(0.9));
        assert_eq!(config.median_logprob_threshold_max, Some(-6.5));
    }
}
