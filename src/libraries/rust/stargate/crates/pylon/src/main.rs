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

use anyhow::Result;
use pylon_lib::{EngineStatsStreamMode, ModelDiscoveryProvider, TunnelTransportProtocol};
use stargate_protocol::BackendConnectivity;
use stargate_protocol::tunnel_contract::HEADER_STARGATE_UPSTREAM_RETRYABLE;

const DEFAULT_PYLON_RETRYABLE_UPSTREAM_STATUS_CODES: &str = "429,503";
const DEFAULT_PYLON_UPSTREAM_RETRY_HEADER: &str = HEADER_STARGATE_UPSTREAM_RETRYABLE;
const DEFAULT_OTEL_SERVICE_NAME: &str = "pylon";

mod startup;

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
    #[arg(long, value_name = "MODEL")]
    model_name: Vec<String>,
    /// Runtime API used to discover model IDs when --model-name is omitted
    #[arg(long, default_value_t = ModelDiscoveryProvider::Dynamo, value_name = "PROVIDER")]
    model_discovery_provider: ModelDiscoveryProvider,
    /// Interval between model-discovery polls in milliseconds
    #[arg(long, default_value_t = 5000, value_name = "MS")]
    model_discovery_poll_interval_ms: u64,
    /// Timeout for one model-discovery request in milliseconds
    #[arg(long, default_value_t = 5000, value_name = "MS")]
    model_discovery_request_timeout_ms: u64,
    /// Stargate gRPC address for registration
    #[arg(long, default_value = "127.0.0.1:50071", value_name = "ADDR")]
    stargate_address: String,
    /// Inference server id for registration
    #[arg(long, default_value = "pylon", value_name = "ID")]
    inference_server_id: String,
    /// Logical cluster id for registration. Defaults to inference-server-id.
    #[arg(long, value_name = "ID")]
    cluster_id: Option<String>,
    /// Path to the QUIC server identity in direct mode or trust anchor in reverse mode
    #[arg(long, env = "STARGATE_TLS_CERT_PATH", value_name = "PATH")]
    tls_cert_path: Option<String>,
    /// Path to the QUIC server private key in direct mode
    #[arg(long, env = "STARGATE_TLS_KEY_PATH", value_name = "PATH")]
    tls_key_path: Option<String>,
    /// Skip QUIC TLS certificate verification for reverse tunnel connections
    #[arg(long, default_value_t = false, env = "STARGATE_QUIC_INSECURE")]
    quic_insecure: bool,
    /// Tunnel connection direction: direct listens for Stargate; reverse connects to Stargate.
    #[arg(long, default_value_t = BackendConnectivity::Direct, value_name = "MODE")]
    backend_connectivity: BackendConnectivity,
    /// Tunnel protocol used for proxied request streams
    #[arg(long, default_value_t = TunnelTransportProtocol::RawQuic, value_name = "PROTOCOL")]
    tunnel_protocol: TunnelTransportProtocol,
    /// Disable ongoing upstream health monitoring and active canaries
    #[arg(long, default_value_t = false)]
    disable_bringup: bool,
    /// Run local input-TPS calibration before contacting Stargate. Use only when this is the cluster's sole Pylon
    #[arg(long, default_value_t = false)]
    do_calibration: bool,
    /// Bootstrap input TPS for every configured model instead of running calibration
    #[arg(long, value_name = "TPS")]
    initial_input_tps: Option<f64>,
    /// Interval between active canary requests in milliseconds. `0` disables active canaries
    #[arg(long, default_value_t = 5000, value_name = "MS")]
    active_canary_interval_ms: u64,
    /// Treat canary responses that generate this many tokens as runaway generation
    #[arg(long, default_value_t = 237, value_name = "TOKENS")]
    canary_max_generation_threshold: u32,
    /// Initial calibration request count; doubles after each completed load step
    #[arg(long, default_value_t = 5, value_name = "N")]
    calibration_requests: usize,
    /// Maximum approximate prompt units used by the calibration load ramp
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
    /// Keep --initial-input-tps fixed for deterministic benchmark/test experiments
    #[arg(long, default_value_t = false, hide = true)]
    benchmark_pin_input_tps: bool,
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
        default_value = DEFAULT_OTEL_SERVICE_NAME,
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
    startup::run(args).await
}

#[cfg(test)]
mod tests {
    use pylon_lib::{
        EngineStatsStreamMode, ModelDiscoveryProvider, PylonQueueMismatchRetryConfig,
        PylonRetryConfig, TunnelTransportProtocol,
    };
    use reqwest::header::HeaderName;

    use super::startup::{
        effective_cluster_id, normalize_base_url, pylon_queue_mismatch_retry_config_from_args,
        pylon_retry_config_from_args, request_quality_monitor_config_from_args,
        stats_collector_config_from_args,
    };
    use super::*;

    fn parse_args(extra: &str) -> Args {
        try_parse_argv(&extra.split_whitespace().collect::<Vec<_>>()).expect("args should parse")
    }

    fn parse_argv(extra: &[&str]) -> Args {
        try_parse_argv(extra).expect("args should parse")
    }

    fn try_parse_argv(extra: &[&str]) -> std::result::Result<Args, clap::Error> {
        let mut args = vec![
            "pylon",
            "--upstream-http-base-url",
            "http://127.0.0.1:8090/",
        ];
        args.extend_from_slice(extra);
        <Args as clap::Parser>::try_parse_from(args)
    }

    #[test]
    fn cli_command_is_named_pylon() {
        assert_eq!(
            <Args as clap::CommandFactory>::command().get_name(),
            "pylon"
        );
    }

    #[test]
    fn inference_server_id_defaults_to_pylon() {
        let args = parse_args("");

        assert_eq!(args.inference_server_id, "pylon");
    }

    #[test]
    fn model_names_default_to_discovery_mode() {
        let args = parse_args("");

        assert!(args.model_name.is_empty());
    }

    #[test]
    fn model_discovery_defaults_to_dynamo_with_five_second_intervals() {
        let args = parse_args("");

        assert_eq!(
            args.model_discovery_provider,
            ModelDiscoveryProvider::Dynamo
        );
        assert_eq!(args.model_discovery_poll_interval_ms, 5_000);
        assert_eq!(args.model_discovery_request_timeout_ms, 5_000);
    }

    #[test]
    fn invalid_model_discovery_provider_is_rejected_by_cli() {
        let error = try_parse_argv(&["--model-discovery-provider", "unknown"])
            .expect_err("unknown provider must be rejected");

        assert!(error.to_string().contains("dynamo"));
    }

    #[test]
    fn startup_model_source_is_either_static_or_discovered() {
        let static_plan = startup::PylonStartupPlan::from_args(&parse_args(
            "--initial-input-tps 100 --model-name model-b --model-name model-a --model-name model-b",
        ))
        .expect("static startup plan should build");
        let discovered_plan = startup::PylonStartupPlan::from_args(&parse_args(
            "--initial-input-tps 100 --model-discovery-provider dynamo",
        ))
        .expect("discovered startup plan should build");

        assert_eq!(
            static_plan.static_model_ids(),
            Some(&std::collections::BTreeSet::from([
                "model-a".to_string(),
                "model-b".to_string(),
            ]))
        );
        assert!(discovered_plan.static_model_ids().is_none());
    }

    #[test]
    fn empty_static_model_id_is_rejected() {
        let args = parse_argv(&["--initial-input-tps", "100", "--model-name", "   "]);

        assert!(startup::PylonStartupPlan::from_args(&args).is_err());
    }

    #[test]
    fn zero_model_discovery_intervals_are_rejected() {
        for option in [
            "--model-discovery-poll-interval-ms 0",
            "--model-discovery-request-timeout-ms 0",
        ] {
            let args = parse_args(&format!("--initial-input-tps 100 {option}"));
            assert!(startup::PylonStartupPlan::from_args(&args).is_err());
        }
    }

    #[test]
    fn pylon_retry_cli_defaults_match_runtime_defaults() {
        let args = parse_args("");
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
        let args = parse_argv(&[
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
        let args = parse_argv(&["--pylon-retryable-upstream-status-codes", ""]);
        let retry = pylon_retry_config_from_args(&args).expect("retry config should parse");

        assert!(retry.retryable_upstream_status_codes.is_empty());
    }

    #[test]
    fn pylon_queue_mismatch_retry_cli_defaults_match_runtime_defaults() {
        let args = parse_args("");
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
        let args = parse_args(
            "--pylon-queue-mismatch-retry-enabled=false \
             --pylon-queue-mismatch-min-delta-ms 50 \
             --pylon-queue-mismatch-tolerance-factor 1.5 \
             --pylon-queue-mismatch-retry-after-ms 250",
        );
        let config = pylon_queue_mismatch_retry_config_from_args(&args)
            .expect("queue mismatch config should parse");

        assert!(!config.enabled);
        assert_eq!(config.min_delta_ms, 50);
        assert_eq!(config.tolerance_factor, 1.5);
        assert_eq!(config.retry_after_ms, Some(250));
    }

    #[test]
    fn invalid_pylon_queue_mismatch_tolerance_factor_is_rejected() {
        let args = parse_args("--pylon-queue-mismatch-tolerance-factor 0");

        assert!(pylon_queue_mismatch_retry_config_from_args(&args).is_err());
    }

    #[test]
    fn startup_requires_exactly_one_input_tps_bootstrap_source() {
        let neither = parse_args("");
        let both = parse_args("--do-calibration --initial-input-tps 2200");

        assert!(startup::PylonStartupPlan::from_args(&neither).is_err());
        assert!(startup::PylonStartupPlan::from_args(&both).is_err());
        assert!(startup::PylonStartupPlan::from_args(&parse_args("--do-calibration")).is_ok());
        assert!(
            startup::PylonStartupPlan::from_args(&parse_args("--initial-input-tps 2200")).is_ok()
        );
    }

    #[test]
    fn invalid_initial_input_tps_is_rejected() {
        for value in ["0", "-1", "NaN", "inf", "-inf"] {
            let args = parse_args(&format!("--initial-input-tps={value}"));
            assert!(
                startup::PylonStartupPlan::from_args(&args).is_err(),
                "{value} must be rejected"
            );
        }
    }

    #[test]
    fn calibration_ramp_requires_a_positive_request_increment() {
        let args = parse_args("--do-calibration --calibration-requests 0");

        assert!(startup::PylonStartupPlan::from_args(&args).is_err());
    }

    #[test]
    fn benchmark_pin_requires_initial_input_tps() {
        let calibration = parse_args("--do-calibration --benchmark-pin-input-tps");
        let initial = parse_args("--initial-input-tps 2200 --benchmark-pin-input-tps");

        assert!(startup::PylonStartupPlan::from_args(&calibration).is_err());
        assert!(startup::PylonStartupPlan::from_args(&initial).is_ok());
    }

    #[test]
    fn engine_stats_stream_defaults_to_auto_mode_and_v1_path() {
        let args = parse_args("");
        let upstream = normalize_base_url(&args.upstream_http_base_url);
        let metrics_config = stats_collector_config_from_args(&args, &upstream);

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
        let args = parse_args("--engine-stats-stream off");
        let upstream = normalize_base_url(&args.upstream_http_base_url);
        let metrics_config = stats_collector_config_from_args(&args, &upstream);

        assert_eq!(args.engine_stats_stream, EngineStatsStreamMode::Off);
        assert!(metrics_config.kv_cache_stats_url.is_none());
        assert!(metrics_config.openai_fallback_stats_enabled);
    }

    #[test]
    fn kv_cache_stats_path_enables_explicit_kv_cache_polling() {
        let args = parse_args("--kv-cache-stats-path /kv-cache/stats");
        let upstream = normalize_base_url(&args.upstream_http_base_url);
        let metrics_config = stats_collector_config_from_args(&args, &upstream);

        assert_eq!(
            metrics_config.kv_cache_stats_url,
            Some("http://127.0.0.1:8090/kv-cache/stats".to_string())
        );
    }

    #[test]
    fn required_engine_stats_stream_disables_openai_fallback_stats() {
        let args = parse_args("--engine-stats-stream required");
        let upstream = normalize_base_url(&args.upstream_http_base_url);
        let metrics_config = stats_collector_config_from_args(&args, &upstream);

        assert_eq!(args.engine_stats_stream, EngineStatsStreamMode::Required);
        assert!(!metrics_config.openai_fallback_stats_enabled);
    }

    #[test]
    fn cluster_id_defaults_to_inference_server_id() {
        let args = parse_args("--inference-server-id client-a");

        assert_eq!(effective_cluster_id(&args), "client-a");
    }

    #[test]
    fn cluster_id_can_be_set_independently() {
        let args = parse_args("--inference-server-id client-a --cluster-id cluster-shared");

        assert_eq!(effective_cluster_id(&args), "cluster-shared");
    }

    #[test]
    fn invalid_pylon_retryable_status_code_is_rejected() {
        let args = parse_args("--pylon-retryable-upstream-status-codes 429,nope");

        assert!(pylon_retry_config_from_args(&args).is_err());
    }

    #[test]
    fn tunnel_protocol_cli_defaults_to_raw_quic() {
        let args = parse_args("");

        assert_eq!(args.tunnel_protocol, TunnelTransportProtocol::RawQuic);
    }

    #[test]
    fn backend_connectivity_cli_is_explicit_and_defaults_to_direct() {
        assert_eq!(
            parse_args("").backend_connectivity,
            stargate_protocol::BackendConnectivity::Direct
        );
        assert_eq!(
            parse_args("--backend-connectivity reverse").backend_connectivity,
            stargate_protocol::BackendConnectivity::Reverse
        );
        assert!(try_parse_argv(&["--backend-connectivity", "edge"]).is_err());
    }

    #[test]
    fn startup_plan_preserves_direct_and_reverse_registration_inputs() {
        let direct = startup::PylonStartupPlan::from_args(&parse_args("--initial-input-tps 100"))
            .expect("direct startup plan should build");
        assert_eq!(
            direct.direct_tunnel_listen_addr(),
            Some("127.0.0.1:0".parse().unwrap())
        );
        let reverse = startup::PylonStartupPlan::from_args(&parse_args(
            "--backend-connectivity reverse --initial-input-tps 100",
        ))
        .expect("reverse startup plan should build");
        assert_eq!(reverse.direct_tunnel_listen_addr(), None);
    }

    #[test]
    fn quality_monitor_cli_overrides_are_applied() {
        let args = parse_args(
            "--collect-quality-metrics \
             --collect-quality-metrics-min-tokens 5 \
             --quality-output-tokens-threshold-min 99 \
             --quality-output-compression-threshold-max 0.3 \
             --quality-output-degeneracy-threshold-min 0.5 \
             --quality-output-repetition-1gram-threshold-min 0.7 \
             --quality-output-repetition-2gram-threshold-min 0.8 \
             --quality-output-repetition-3gram-threshold-min 0.9 \
             --quality-median-logprob-threshold-max=-6.5",
        );
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
