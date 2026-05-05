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

#![allow(unreachable_code)]

use clap::Parser;
use serde::{Deserialize, Serialize};
use std::path::PathBuf;

use crate::nats::NatsProperties;
use crate::nvcf_api::NVCFApiProperties;
use crate::s3::S3Properties;
use crate::telemetry::settings::ServerSettings;
use crate::worker_streams::WorkerStreamProperties;
use anyhow::Context;
use http::Uri;
use std::time::Duration;
use tonic::transport::{Channel, ClientTlsConfig};

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct GrpcClientConfig {
    /// Connection timeout for establishing gRPC connections
    pub connect_timeout: Duration,
    /// Request timeout for individual gRPC calls
    pub request_timeout: Duration,
    /// Keepalive interval
    pub keepalive_interval: Duration,
    /// Keepalive timeout
    pub keepalive_timeout: Duration,
    /// Enable keepalive while idle
    pub keepalive_while_idle: bool,
    /// OAuth2 client timeout
    pub oauth2_client_timeout: Duration,
}

impl Default for GrpcClientConfig {
    fn default() -> Self {
        Self {
            connect_timeout: Duration::from_secs(4),
            request_timeout: Duration::from_secs(10),
            // Set keepalive interval to 5 minutes to align with Go gRPC server defaults
            // and comply with server enforcement policies. See:
            // https://github.com/grpc/proposal/blob/master/A8-client-side-keepalive.md#server-enforcement
            keepalive_interval: Duration::from_secs(300), // 5 minutes
            keepalive_timeout: Duration::from_secs(5),
            keepalive_while_idle: true,
            oauth2_client_timeout: Duration::from_secs(10),
        }
    }
}

impl GrpcClientConfig {
    /// Build a configured channel builder with common gRPC settings
    fn build_channel_builder(
        &self,
        address: &str,
    ) -> anyhow::Result<tonic::transport::channel::Endpoint> {
        let uri: Uri = address.parse().context("parsing grpc address")?;
        let mut builder = Channel::builder(uri.clone())
            .connect_timeout(self.connect_timeout)
            .timeout(self.request_timeout)
            .http2_keep_alive_interval(self.keepalive_interval)
            .keep_alive_timeout(self.keepalive_timeout)
            .keep_alive_while_idle(self.keepalive_while_idle);

        if matches!(uri.scheme_str(), Some(scheme) if scheme == "https") {
            builder = builder.tls_config(ClientTlsConfig::default().with_native_roots())?;
        }

        Ok(builder)
    }

    /// Build a gRPC channel with common configuration
    pub async fn build_grpc_channel(&self, address: &str) -> anyhow::Result<Channel> {
        self.build_channel_builder(address)?
            .connect()
            .await
            .context("connecting to grpc service")
    }

    /// Build a lazy gRPC channel with common configuration (for optional services)
    pub fn build_grpc_channel_lazy(&self, address: &str) -> anyhow::Result<Channel> {
        Ok(self.build_channel_builder(address)?.connect_lazy())
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AppConfig {
    // Server settings for the application.
    pub server: ServerSettings,
    // gRPC client configuration
    pub grpc_client: GrpcClientConfig,
    // Application settings for the application
    pub nvcf_api_address: String,
    pub nvcf_api_properties: Option<NVCFApiProperties>,
    pub rate_limit_address: String,
    pub rate_limit_enabled: bool,
    pub nats_properties: NatsProperties,
    pub s3_properties: S3Properties,
    pub regional_nvcf_api_grpc_address: String,
    pub secrets_path: Option<PathBuf>,
    pub secrets_watcher_interval_seconds: u64,
    pub oauth2_base_url: String,
    pub tcp_nodelay: bool,
    pub worker_stream_properties: WorkerStreamProperties,
    pub pod_ip: Option<String>,
}

impl Default for AppConfig {
    fn default() -> Self {
        let addr = "localhost:9090";
        AppConfig::new(
            addr,
            NatsProperties::default(),
            S3Properties::default(),
            Some(NVCFApiProperties::default()),
            WorkerStreamProperties::default(),
        )
    }
}

impl AppConfig {
    pub fn new(
        grpc_addr: &str,
        nats_properties: NatsProperties,
        s3_properties: S3Properties,
        nvcf_api_properties: Option<NVCFApiProperties>,
        worker_stream_properties: WorkerStreamProperties,
    ) -> Self {
        AppConfig {
            server: ServerSettings::default_with_service_name(env!("CARGO_PKG_NAME").to_string()),
            grpc_client: GrpcClientConfig::default(),
            nvcf_api_address: format!("http://{grpc_addr}"),
            nvcf_api_properties,
            rate_limit_address: format!("http://{grpc_addr}"),
            rate_limit_enabled: false,
            nats_properties,
            s3_properties,
            // should be different in practice, since we'll use the cluster local address for
            // interactions with the grpc api and this address is passed to external workers
            regional_nvcf_api_grpc_address: format!("http://{grpc_addr}"),
            secrets_path: None,
            secrets_watcher_interval_seconds: 5,
            oauth2_base_url: "".into(),
            tcp_nodelay: true,
            worker_stream_properties,
            pod_ip: None,
        }
    }
}

#[derive(Parser, Debug, Serialize, Deserialize)]
#[command(version, about, long_about = None)]
pub struct AppCliArgs {
    // Add your own CLI args below.
}

/// Build an [`AppConfig`] from a config file path and environment variables.
///
/// - `Some(path)` — explicit path (e.g. from `APP_CONFIG`); a missing or
///   unreadable file is a hard error.
/// - `None` — no path provided; falls back to `/etc/app/config/settings` or
///   `settings` in the working directory, and a missing file is fine.
///
/// Resolution order (later sources win):
/// 1. Struct defaults (`AppConfig::default()`).
/// 2. Config file (see above).
/// 3. Environment variables with `__` separator (e.g. `NVCF_API_ADDRESS=...`).
///    Empty env vars are intentionally NOT ignored: a blank value from a broken
///    k8s secret ref should be a hard failure, not a silent fallback to defaults.
pub fn load_app_config(config_file: Option<&str>) -> Result<AppConfig, config::ConfigError> {
    let default_config = serde_json::to_string(&AppConfig::default()).unwrap();

    let (path, required) = match config_file {
        Some(p) => {
            eprintln!("Loading config from APP_CONFIG: {p}");
            (p.to_string(), true)
        }
        None => {
            let p = if std::path::Path::new("/etc/app/config/settings.yaml").exists() {
                eprintln!("APP_CONFIG not set, using /etc/app/config/settings.yaml");
                "/etc/app/config/settings".to_string()
            } else {
                eprintln!("APP_CONFIG not set and no config file found, using struct defaults and env vars only");
                "settings".to_string()
            };
            (p, false)
        }
    };

    config::Config::builder()
        .add_source(config::File::from_str(
            &default_config,
            config::FileFormat::Json,
        ))
        .add_source(config::File::with_name(&path).required(required))
        .add_source(
            config::Environment::default()
                .separator("__")
                .try_parsing(true),
        )
        .build()
        .and_then(|c| c.try_deserialize::<AppConfig>())
}

/// Load application config from a YAML file and environment variable overrides.
///
/// Resolution order (later sources win):
/// 1. Struct defaults (`AppConfig::default()`).
/// 2. Config file — path from `APP_CONFIG` env var, falling back to
///    `/etc/app/config/settings` then `settings` in the working directory.
/// 3. Environment variables with `__` separator (e.g. `SERVER__METRICS__EXPORTERS`).
///
/// `AppCliArgs` is parsed and returned for callers that need it (e.g. version/help
/// flags) but contains no config overrides — add fields there if CLI overrides are
/// needed in future.
pub fn parse_config() -> (AppCliArgs, AppConfig) {
    let args = AppCliArgs::parse();

    let mut settings =
        load_app_config(std::env::var("APP_CONFIG").ok().as_deref()).unwrap_or_else(|e| {
            eprintln!("Error loading config: {e}");
            std::process::exit(1);
        });

    // Inject OTel resource attributes that are only known at runtime.
    let mut resource_settings = settings.server.resource.clone().unwrap_or_default();

    resource_settings.add_attributes(&[
        (
            opentelemetry_semantic_conventions::resource::SERVICE_NAME,
            env!("CARGO_PKG_NAME").to_string(),
        ),
        (
            opentelemetry_semantic_conventions::resource::SERVICE_VERSION,
            env!("CARGO_PKG_VERSION").to_string(),
        ),
        (
            opentelemetry_semantic_conventions::resource::HOST_ID,
            hostname::get()
                .ok()
                .and_then(|h| h.to_str().map(|s| s.to_string()))
                .unwrap_or_else(|| uuid::Uuid::new_v4().to_string()),
        ),
        (
            opentelemetry_semantic_conventions::resource::CLOUD_REGION,
            std::env::var("AWS_REGION").unwrap_or_else(|_| "ncp".to_string()),
        ),
        (
            "host.dc",
            std::env::var("AWS_REGION").unwrap_or_else(|_| "ncp".to_string()),
        ),
    ]);
    settings.server.resource = Some(resource_settings);

    (args, settings)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;

    // Verify that struct defaults are applied when no config file and no env vars are set.
    // This would have caught the regression where we forgot to seed AppConfig::default()
    // as the base layer, causing required fields to fail deserialization.
    #[test]
    fn test_load_app_config_pure_defaults() {
        // None: no explicit path, missing fallback file is fine, should use defaults
        let config = load_app_config(None).unwrap();
        let expected = AppConfig::default();
        assert_eq!(config.nvcf_api_address, expected.nvcf_api_address);
        assert_eq!(config.rate_limit_address, expected.rate_limit_address);
        assert_eq!(config.rate_limit_enabled, expected.rate_limit_enabled);
        assert_eq!(
            config.secrets_watcher_interval_seconds,
            expected.secrets_watcher_interval_seconds
        );
        assert_eq!(config.tcp_nodelay, expected.tcp_nodelay);
    }

    // Verify that a config file overrides defaults. If APP_CONFIG were still being
    // used as APP_CONFIG_FILE the file path wouldn't resolve and we'd silently get
    // defaults rather than the overridden values.
    #[test]
    fn test_load_app_config_file_overrides_defaults() {
        let dir = tempfile::tempdir().unwrap();
        let config_path = dir.path().join("settings.yaml");
        std::fs::write(
            &config_path,
            "nvcf_api_address: \"http://override-host:1234\"\nrate_limit_enabled: true\n",
        )
        .unwrap();
        // strip the .yaml extension — config::File::with_name auto-detects it
        let stem = config_path.with_extension("");
        let config = load_app_config(stem.to_str()).unwrap();
        assert_eq!(config.nvcf_api_address, "http://override-host:1234");
        assert!(config.rate_limit_enabled);
        // unset fields should still carry their defaults
        assert_eq!(config.tcp_nodelay, AppConfig::default().tcp_nodelay);
    }

    // Verify that a required config file that doesn't exist returns an error rather
    // than silently falling back to defaults.
    #[test]
    fn test_load_app_config_required_missing_file_errors() {
        let result = load_app_config(Some("/nonexistent/path/settings"));
        assert!(result.is_err());
    }

    #[test]
    fn test_grpc_client_config_default() {
        let config = GrpcClientConfig::default();

        assert_eq!(config.connect_timeout, Duration::from_secs(4));
        assert_eq!(config.request_timeout, Duration::from_secs(10));
        assert_eq!(config.keepalive_interval, Duration::from_secs(300));
        assert_eq!(config.keepalive_timeout, Duration::from_secs(5));
        assert!(config.keepalive_while_idle);
        assert_eq!(config.oauth2_client_timeout, Duration::from_secs(10));
    }

    #[test]
    fn test_app_config_includes_grpc_client() {
        let config = AppConfig::default();

        // Verify that AppConfig includes the grpc_client field with proper defaults
        assert_eq!(config.grpc_client.connect_timeout, Duration::from_secs(4));
        assert_eq!(config.grpc_client.request_timeout, Duration::from_secs(10));
    }
}
