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

use std::fmt::Display;
use std::net::SocketAddr;
use std::num::NonZeroUsize;
use std::path::{Path, PathBuf};
use std::str::FromStr;
use std::time::Duration;

use anyhow::{Context, Result, ensure};
use serde::{Deserialize, Deserializer, Serialize};
use serde_with::{DurationMilliSeconds, serde_as};
use stargate_protocol::{TunnelTransportProtocol, parse_explicit_http_uri};

pub const SCHEMA_VERSION: u32 = 1;
pub const DEFAULT_PROXY_MAX_REPLAY_BODY_BYTES: usize = 64 * 1024 * 1024;

fn default_schema_version() -> u32 {
    SCHEMA_VERSION
}

fn default_grpc_listen_addr() -> SocketAddr {
    SocketAddr::from(([0, 0, 0, 0], 50071))
}

fn default_model_discovery_listen_addr() -> SocketAddr {
    SocketAddr::from(([0, 0, 0, 0], 50073))
}

fn default_http_listen_addr() -> SocketAddr {
    SocketAddr::from(([0, 0, 0, 0], 8000))
}

fn default_metrics_listen_addr() -> SocketAddr {
    SocketAddr::from(([0, 0, 0, 0], 9090))
}

fn default_advertised_hostname_template() -> String {
    "{pod_name}.stargate.external".to_string()
}

fn default_poll_interval() -> Duration {
    Duration::from_millis(1_000)
}

fn default_watch_heartbeat() -> Duration {
    Duration::from_millis(5_000)
}

fn default_shutdown_drain_timeout() -> Duration {
    Duration::from_millis(30_000)
}

fn default_quic_connect_timeout() -> Duration {
    Duration::from_millis(2_000)
}

fn default_quic_request_timeout() -> Duration {
    Duration::from_millis(30_000)
}

fn default_reverse_connect_timeout() -> Duration {
    Duration::from_millis(10_000)
}

fn default_direct_connections() -> NonZeroUsize {
    NonZeroUsize::MIN
}

fn default_proxy_connect_retries() -> u32 {
    2
}

fn default_proxy_request_retries() -> u32 {
    2
}

fn default_proxy_replay_body_bytes() -> usize {
    DEFAULT_PROXY_MAX_REPLAY_BODY_BYTES
}

fn default_true() -> bool {
    true
}

fn default_request_budget_header() -> String {
    "x-stargate-max-wait-ms".to_string()
}

fn default_service_name() -> String {
    crate::telemetry::DEFAULT_SERVICE_NAME.to_string()
}

fn default_metrics_prefix() -> String {
    crate::metrics::DEFAULT_PREFIX.to_string()
}

fn default_bearer_json_path() -> Vec<String> {
    vec!["authToken".to_string()]
}

fn default_tracing_json_path() -> Vec<String> {
    vec!["tracingAccessToken".to_string()]
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct EnvironmentReference {
    env: String,
}

#[derive(Debug, Deserialize)]
#[serde(untagged)]
enum ValueSource<T> {
    Literal(T),
    Environment(EnvironmentReference),
}

impl<T> ValueSource<T>
where
    T: FromStr,
    T::Err: Display,
{
    fn resolve_with(
        self,
        read_environment: impl FnOnce(&str) -> std::result::Result<String, String>,
    ) -> std::result::Result<T, String> {
        match self {
            Self::Literal(value) => Ok(value),
            Self::Environment(reference) => {
                if reference.env.trim().is_empty() {
                    return Err("environment variable name must not be empty".to_string());
                }
                let value = read_environment(&reference.env)?;
                if value.is_empty() {
                    return Err(format!(
                        "environment variable '{}' must not be empty",
                        reference.env
                    ));
                }
                value.parse().map_err(|error| {
                    format!(
                        "environment variable '{}' has an invalid value: {error}",
                        reference.env
                    )
                })
            }
        }
    }

    fn resolve_environment(self) -> std::result::Result<T, String> {
        self.resolve_with(|name| {
            std::env::var(name)
                .map_err(|error| format!("failed to read environment variable '{name}': {error}"))
        })
    }
}

fn deserialize_value_source<'de, D, T>(deserializer: D) -> std::result::Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Deserialize<'de> + FromStr,
    T::Err: Display,
{
    ValueSource::<T>::deserialize(deserializer)?
        .resolve_environment()
        .map_err(serde::de::Error::custom)
}

fn deserialize_optional_value_source<'de, D, T>(
    deserializer: D,
) -> std::result::Result<Option<T>, D::Error>
where
    D: Deserializer<'de>,
    T: Deserialize<'de> + FromStr,
    T::Err: Display,
{
    Option::<ValueSource<T>>::deserialize(deserializer)?
        .map(ValueSource::resolve_environment)
        .transpose()
        .map_err(serde::de::Error::custom)
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct StargateConfig {
    #[serde(default = "default_schema_version")]
    pub schema_version: u32,
    pub stargate_identity: StargateIdentityConfig,
    pub stargate_network: StargateNetworkConfig,
    #[serde(default)]
    pub process_lifecycle: ProcessLifecycleConfig,
    #[serde(default)]
    pub pylon_registration: PylonRegistrationConfig,
    #[serde(default)]
    pub stargate_discovery: StargateDiscoveryConfig,
    #[serde(default)]
    pub pylon_transport: PylonTransportConfig,
    #[serde(default)]
    pub request_proxy: RequestProxyConfig,
    #[serde(default)]
    pub observability: ObservabilityConfig,
    #[serde(default)]
    pub worker_authentication: Option<WorkerAuthenticationConfig>,
}

impl StargateConfig {
    pub fn from_toml_file(path: &Path) -> Result<Self> {
        let source = std::fs::read_to_string(path)
            .with_context(|| format!("failed to read config file '{}'", path.display()))?;
        let mut config: Self = toml::from_str(&source)
            .with_context(|| format!("failed to parse config file '{}'", path.display()))?;
        config.resolve_relative_paths(path.parent().unwrap_or_else(|| Path::new(".")));
        config
            .validate()
            .with_context(|| format!("invalid config file '{}'", path.display()))?;
        Ok(config)
    }

    pub fn from_toml_str(source: &str) -> Result<Self> {
        let config: Self = toml::from_str(source).context("failed to parse TOML configuration")?;
        config.validate().context("invalid TOML configuration")?;
        Ok(config)
    }

    pub fn to_toml_string(&self) -> Result<String> {
        self.validate()?;
        toml::to_string_pretty(self).context("failed to serialize Stargate configuration")
    }

    pub fn validate(&self) -> Result<()> {
        ensure!(
            self.schema_version == SCHEMA_VERSION,
            "unsupported schema_version {}; expected {SCHEMA_VERSION}",
            self.schema_version
        );
        ensure!(
            !self.stargate_identity.id.trim().is_empty(),
            "stargate_identity.id must not be empty"
        );
        ensure!(
            !self
                .stargate_identity
                .advertised_hostname_template
                .trim()
                .is_empty(),
            "stargate_identity.advertised_hostname_template must not be empty"
        );

        if let Some(kubernetes) = &self.stargate_identity.kubernetes {
            ensure!(
                !kubernetes.pod_name.trim().is_empty(),
                "stargate_identity.kubernetes.pod_name must not be empty"
            );
            ensure!(
                !kubernetes.namespace.trim().is_empty(),
                "stargate_identity.kubernetes.namespace must not be empty"
            );
        }

        for url in &self.stargate_discovery.remote_watch_urls {
            let normalized = parse_explicit_http_uri(url)
                .map_err(anyhow::Error::msg)
                .with_context(|| {
                    format!("invalid stargate_discovery.remote_watch_urls entry '{url}'")
                })?;
            ensure!(
                self.stargate_discovery.allow_insecure_remote_watch_http
                    || !normalized.starts_with("http://"),
                "http:// remote Watch URLs require \
                 stargate_discovery.allow_insecure_remote_watch_http=true"
            );
        }

        if let Some(pods) = &self.stargate_discovery.kubernetes_pods {
            ensure!(
                self.stargate_identity.kubernetes.is_some(),
                "stargate_discovery.kubernetes_pods requires \
                 stargate_identity.kubernetes"
            );
            ensure!(
                !pods.headless_service_dns_name.trim().is_empty(),
                "stargate_discovery.kubernetes_pods.headless_service_dns_name \
                 must not be empty"
            );
            ensure!(
                !pods.poll_interval.is_zero(),
                "stargate_discovery.kubernetes_pods.poll_interval_ms must be \
                 greater than 0"
            );
        }

        if let Some(uri) = &self.pylon_transport.pylon_grpc_dial_uri {
            parse_explicit_http_uri(uri)
                .map_err(anyhow::Error::msg)
                .context("invalid pylon_transport.pylon_grpc_dial_uri")?;
        }

        ensure!(
            !(self.pylon_transport.direct.is_some() && self.pylon_transport.reverse.is_some()),
            "pylon_transport.direct and pylon_transport.reverse are mutually exclusive"
        );
        if let Some(reverse) = &self.pylon_transport.reverse {
            ensure!(
                reverse.certificate_path.is_some() == reverse.private_key_path.is_some(),
                "pylon_transport.reverse.certificate_path and private_key_path \
                 must be supplied together"
            );
        }

        let header = self.request_proxy.retry.request_budget_header.trim();
        if !header.is_empty() {
            http::HeaderName::from_bytes(header.as_bytes())
                .with_context(|| format!("invalid request proxy budget header '{header}'"))?;
        }

        if let Some(tracing) = &self.observability.tracing
            && let Some(token) = &tracing.access_token
        {
            validate_json_path(
                &token.json_path,
                "observability.tracing.access_token.json_path",
            )?;
        }

        if let Some(auth) = &self.worker_authentication {
            ensure!(
                !auth.endpoint.trim().is_empty(),
                "worker_authentication.endpoint must not be empty"
            );
            ensure!(
                !(auth.bearer_token.is_some() && auth.oauth2.is_some()),
                "worker_authentication.bearer_token and \
                 worker_authentication.oauth2 are mutually exclusive"
            );
            if let Some(token) = &auth.bearer_token {
                validate_json_path(
                    &token.json_path,
                    "worker_authentication.bearer_token.json_path",
                )?;
            }
            if let Some(oauth2) = &auth.oauth2 {
                ensure!(
                    !oauth2.provider_host.trim().is_empty(),
                    "worker_authentication.oauth2.provider_host must not be empty"
                );
            }
        }
        Ok(())
    }

    pub fn direct_transport(&self) -> DirectPylonTransportConfig {
        self.pylon_transport.direct.clone().unwrap_or_default()
    }

    fn resolve_relative_paths(&mut self, base_dir: &Path) {
        if let Some(direct) = &mut self.pylon_transport.direct {
            resolve_relative_path(&mut direct.trust_bundle_path, base_dir);
        }
        if let Some(reverse) = &mut self.pylon_transport.reverse {
            resolve_relative_path(&mut reverse.certificate_path, base_dir);
            resolve_relative_path(&mut reverse.private_key_path, base_dir);
        }
        if let Some(load_balancer) = &mut self.request_proxy.load_balancer {
            resolve_path(&mut load_balancer.config_path, base_dir);
        }
        if let Some(tracing) = &mut self.observability.tracing
            && let Some(token) = &mut tracing.access_token
        {
            resolve_path(&mut token.secrets_path, base_dir);
        }
        if let Some(auth) = &mut self.worker_authentication {
            if let Some(token) = &mut auth.bearer_token {
                resolve_path(&mut token.secrets_path, base_dir);
            }
            if let Some(oauth2) = &mut auth.oauth2 {
                resolve_path(&mut oauth2.secrets_path, base_dir);
            }
        }
    }
}

fn validate_json_path(path: &[String], field: &str) -> Result<()> {
    ensure!(!path.is_empty(), "{field} must contain at least one key");
    ensure!(
        path.iter().all(|component| !component.is_empty()),
        "{field} components must not be empty"
    );
    Ok(())
}

fn resolve_relative_path(path: &mut Option<PathBuf>, base_dir: &Path) {
    if let Some(path) = path {
        resolve_path(path, base_dir);
    }
}

fn resolve_path(path: &mut PathBuf, base_dir: &Path) {
    if path.is_relative() {
        *path = base_dir.join(&*path);
    }
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct StargateIdentityConfig {
    #[serde(deserialize_with = "deserialize_value_source")]
    pub id: String,
    #[serde(default = "default_advertised_hostname_template")]
    pub advertised_hostname_template: String,
    #[serde(default)]
    pub kubernetes: Option<KubernetesIdentityConfig>,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct KubernetesIdentityConfig {
    #[serde(deserialize_with = "deserialize_value_source")]
    pub pod_name: String,
    #[serde(deserialize_with = "deserialize_value_source")]
    pub namespace: String,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct StargateNetworkConfig {
    #[serde(default = "default_grpc_listen_addr")]
    pub grpc_listen_addr: SocketAddr,
    #[serde(default = "default_model_discovery_listen_addr")]
    pub model_discovery_listen_addr: SocketAddr,
    #[serde(default = "default_http_listen_addr")]
    pub http_listen_addr: SocketAddr,
    #[serde(deserialize_with = "deserialize_value_source")]
    pub advertise_addr: SocketAddr,
}

#[serde_as]
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct ProcessLifecycleConfig {
    #[serde_as(as = "DurationMilliSeconds<u64>")]
    #[serde(rename = "readiness_warmup_ms")]
    pub readiness_warmup: Duration,
    #[serde_as(as = "DurationMilliSeconds<u64>")]
    #[serde(rename = "shutdown_drain_timeout_ms")]
    pub shutdown_drain_timeout: Duration,
}

impl Default for ProcessLifecycleConfig {
    fn default() -> Self {
        Self {
            readiness_warmup: crate::runtime::DEFAULT_READINESS_WARMUP,
            shutdown_drain_timeout: default_shutdown_drain_timeout(),
        }
    }
}

#[serde_as]
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct PylonRegistrationConfig {
    #[serde_as(as = "DurationMilliSeconds<u64>")]
    #[serde(rename = "update_idle_timeout_ms")]
    pub update_idle_timeout: Duration,
    #[serde_as(as = "DurationMilliSeconds<u64>")]
    #[serde(rename = "update_max_idle_timeout_ms")]
    pub update_max_idle_timeout: Duration,
}

impl Default for PylonRegistrationConfig {
    fn default() -> Self {
        Self {
            update_idle_timeout: crate::registration::DEFAULT_REGISTRATION_UPDATE_IDLE_TIMEOUT,
            update_max_idle_timeout:
                crate::registration::DEFAULT_REGISTRATION_UPDATE_MAX_IDLE_TIMEOUT,
        }
    }
}

#[serde_as]
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct StargateDiscoveryConfig {
    pub remote_watch_urls: Vec<String>,
    pub allow_insecure_remote_watch_http: bool,
    #[serde_as(as = "DurationMilliSeconds<u64>")]
    #[serde(rename = "watch_heartbeat_ms")]
    pub watch_heartbeat: Duration,
    pub kubernetes_pods: Option<KubernetesPodDiscoveryConfig>,
}

impl Default for StargateDiscoveryConfig {
    fn default() -> Self {
        Self {
            remote_watch_urls: Vec::new(),
            allow_insecure_remote_watch_http: false,
            watch_heartbeat: default_watch_heartbeat(),
            kubernetes_pods: None,
        }
    }
}

#[serde_as]
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct KubernetesPodDiscoveryConfig {
    pub headless_service_dns_name: String,
    #[serde_as(as = "DurationMilliSeconds<u64>")]
    #[serde(rename = "poll_interval_ms", default = "default_poll_interval")]
    pub poll_interval: Duration,
    #[serde_as(as = "DurationMilliSeconds<u64>")]
    #[serde(rename = "resolver_ttl_ms", default = "default_poll_interval")]
    pub resolver_ttl: Duration,
    #[serde(default)]
    pub development_peer_forwarding: Option<DevelopmentPeerForwardingConfig>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct DevelopmentPeerForwardingConfig {}

#[serde_as]
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct PylonTransportConfig {
    pub pylon_grpc_dial_uri: Option<String>,
    pub tunnel_protocol: TunnelTransportProtocol,
    #[serde_as(as = "DurationMilliSeconds<u64>")]
    #[serde(rename = "quic_connect_timeout_ms")]
    pub quic_connect_timeout: Duration,
    #[serde_as(as = "DurationMilliSeconds<u64>")]
    #[serde(rename = "quic_request_timeout_ms")]
    pub quic_request_timeout: Duration,
    pub direct: Option<DirectPylonTransportConfig>,
    pub reverse: Option<ReversePylonTransportConfig>,
    pub tls: PylonTlsConfig,
}

impl Default for PylonTransportConfig {
    fn default() -> Self {
        Self {
            pylon_grpc_dial_uri: None,
            tunnel_protocol: TunnelTransportProtocol::RawQuic,
            quic_connect_timeout: default_quic_connect_timeout(),
            quic_request_timeout: default_quic_request_timeout(),
            direct: None,
            reverse: None,
            tls: PylonTlsConfig::default(),
        }
    }
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct DirectPylonTransportConfig {
    #[serde(default = "default_direct_connections")]
    pub connections: NonZeroUsize,
    pub trust_bundle_path: Option<PathBuf>,
}

impl Default for DirectPylonTransportConfig {
    fn default() -> Self {
        Self {
            connections: default_direct_connections(),
            trust_bundle_path: None,
        }
    }
}

#[serde_as]
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ReversePylonTransportConfig {
    pub listen_addr: SocketAddr,
    #[serde(default, deserialize_with = "deserialize_optional_value_source")]
    pub pylon_dial_addr: Option<String>,
    #[serde_as(as = "DurationMilliSeconds<u64>")]
    #[serde(
        rename = "connect_timeout_ms",
        default = "default_reverse_connect_timeout"
    )]
    pub connect_timeout: Duration,
    #[serde(default)]
    pub certificate_path: Option<PathBuf>,
    #[serde(default)]
    pub private_key_path: Option<PathBuf>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct PylonTlsConfig {
    pub insecure_skip_verify: bool,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct RequestProxyConfig {
    pub retry: RequestProxyRetryConfig,
    pub load_balancer: Option<LoadBalancerFileConfig>,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct RequestProxyRetryConfig {
    pub max_connect_retries: u32,
    pub max_request_retries: u32,
    pub max_replay_body_bytes: usize,
    pub require_pylon_retry_signal: bool,
    pub request_budget_header: String,
}

impl Default for RequestProxyRetryConfig {
    fn default() -> Self {
        Self {
            max_connect_retries: default_proxy_connect_retries(),
            max_request_retries: default_proxy_request_retries(),
            max_replay_body_bytes: default_proxy_replay_body_bytes(),
            require_pylon_retry_signal: default_true(),
            request_budget_header: default_request_budget_header(),
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct LoadBalancerFileConfig {
    pub config_path: PathBuf,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct ObservabilityConfig {
    pub service_name: String,
    pub metrics: MetricsConfig,
    pub tracing: Option<TracingConfig>,
}

impl Default for ObservabilityConfig {
    fn default() -> Self {
        Self {
            service_name: default_service_name(),
            metrics: MetricsConfig::default(),
            tracing: None,
        }
    }
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct MetricsConfig {
    pub listen_addr: SocketAddr,
    pub prefix: String,
}

impl Default for MetricsConfig {
    fn default() -> Self {
        Self {
            listen_addr: default_metrics_listen_addr(),
            prefix: default_metrics_prefix(),
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct TracingConfig {
    pub endpoint: String,
    #[serde(default)]
    pub access_token: Option<TracingAccessTokenConfig>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct TracingAccessTokenConfig {
    pub secrets_path: PathBuf,
    #[serde(default = "default_tracing_json_path")]
    pub json_path: Vec<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct WorkerAuthenticationConfig {
    pub endpoint: String,
    #[serde(default)]
    pub bearer_token: Option<BearerTokenConfig>,
    #[serde(default)]
    pub oauth2: Option<OAuth2Config>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct BearerTokenConfig {
    pub secrets_path: PathBuf,
    #[serde(default = "default_bearer_json_path")]
    pub json_path: Vec<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct OAuth2Config {
    pub provider_host: String,
    pub secrets_path: PathBuf,
}

#[cfg(test)]
mod tests {
    use super::*;

    const MINIMAL: &str = r#"
schema_version = 1

[stargate_identity]
id = "stargate-a"

[stargate_network]
advertise_addr = "127.0.0.1:50071"
"#;

    #[test]
    fn minimal_config_uses_runtime_defaults_and_self_only_membership() {
        let config = StargateConfig::from_toml_str(MINIMAL).expect("config should parse");
        assert_eq!(
            config.stargate_network.grpc_listen_addr,
            default_grpc_listen_addr()
        );
        assert_eq!(
            config.process_lifecycle.readiness_warmup,
            crate::runtime::DEFAULT_READINESS_WARMUP
        );
        assert!(config.stargate_discovery.kubernetes_pods.is_none());
        assert_eq!(config.direct_transport().connections, NonZeroUsize::MIN);
    }

    #[test]
    fn complete_reverse_config_parses() {
        let config = StargateConfig::from_toml_str(
            r#"
schema_version = 1

[stargate_identity]
id = "stargate-0"
advertised_hostname_template = "{pod_name}.stargate.example"

[stargate_identity.kubernetes]
pod_name = "stargate-0"
namespace = "inference"

[stargate_network]
advertise_addr = "10.0.0.2:50071"

[stargate_discovery]
remote_watch_urls = ["https://remote.example:50071"]
watch_heartbeat_ms = 5000

[stargate_discovery.kubernetes_pods]
headless_service_dns_name = "stargate-headless.inference.svc.cluster.local"
poll_interval_ms = 1000
resolver_ttl_ms = 1000

[pylon_transport]
tunnel_protocol = "raw-quic"
quic_connect_timeout_ms = 2000
quic_request_timeout_ms = 30000

[pylon_transport.reverse]
listen_addr = "0.0.0.0:50072"
connect_timeout_ms = 10000

[request_proxy.retry]
max_connect_retries = 2
max_request_retries = 2
max_replay_body_bytes = 67108864
require_pylon_retry_signal = true
request_budget_header = "x-stargate-max-wait-ms"
"#,
        )
        .expect("config should parse");
        assert!(config.pylon_transport.reverse.is_some());
        assert!(config.stargate_discovery.kubernetes_pods.is_some());
        let serialized = config
            .to_toml_string()
            .expect("config should serialize to TOML");
        let round_trip = StargateConfig::from_toml_str(&serialized)
            .expect("serialized config should parse again");
        assert_eq!(round_trip, config);
    }

    #[test]
    fn unknown_and_obsolete_discovery_sections_are_rejected() {
        for section in ["[stargate_discovery.dns]", "[unknown]"] {
            let source = format!("{MINIMAL}\n{section}\nname = \"stargate.local\"\n");
            let error = StargateConfig::from_toml_str(&source)
                .expect_err("unknown section should be rejected");
            assert!(error.to_string().contains("failed to parse"));
        }
    }

    #[test]
    fn kubernetes_pod_discovery_requires_kubernetes_identity() {
        let source = format!(
            "{MINIMAL}\n\
             [stargate_discovery.kubernetes_pods]\n\
             headless_service_dns_name = \"stargate-headless\"\n"
        );
        let error =
            StargateConfig::from_toml_str(&source).expect_err("identity should be required");
        assert!(
            format!("{error:#}").contains(
                "stargate_discovery.kubernetes_pods requires stargate_identity.kubernetes"
            )
        );
    }

    #[test]
    fn direct_and_reverse_sections_are_mutually_exclusive() {
        let source = format!(
            "{MINIMAL}\n\
             [pylon_transport.direct]\n\
             connections = 1\n\
             [pylon_transport.reverse]\n\
             listen_addr = \"127.0.0.1:50072\"\n"
        );
        let error = StargateConfig::from_toml_str(&source).expect_err("modes should conflict");
        assert!(format!("{error:#}").contains("mutually exclusive"));
    }

    #[test]
    fn environment_value_source_uses_named_value_and_rejects_empty_values() {
        let source = ValueSource::<SocketAddr>::Environment(EnvironmentReference {
            env: "POD_ADDR".to_string(),
        });
        let address = source
            .resolve_with(|name| {
                assert_eq!(name, "POD_ADDR");
                Ok("127.0.0.1:50071".to_string())
            })
            .expect("environment address should parse");
        assert_eq!(address, "127.0.0.1:50071".parse().unwrap());

        let source = ValueSource::<String>::Environment(EnvironmentReference {
            env: "POD_NAME".to_string(),
        });
        assert!(source.resolve_with(|_| Ok(String::new())).is_err());
    }

    #[test]
    fn config_file_resolves_relative_paths_from_its_directory() {
        let directory = tempfile::tempdir().expect("temp directory should exist");
        let path = directory.path().join("stargate.toml");
        std::fs::write(
            &path,
            format!(
                "{MINIMAL}\n\
                 [request_proxy.load_balancer]\n\
                 config_path = \"lb.json\"\n"
            ),
        )
        .expect("config should be writable");

        let config = StargateConfig::from_toml_file(&path).expect("config should load");
        assert_eq!(
            config
                .request_proxy
                .load_balancer
                .expect("load balancer config should exist")
                .config_path,
            directory.path().join("lb.json")
        );
    }
}
