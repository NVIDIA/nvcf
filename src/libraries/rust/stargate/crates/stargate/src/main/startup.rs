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

use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, Result, bail};
use stargate::auth::OpenAuthenticator;
use stargate::config::{StargateConfig, WorkerAuthenticationConfig};
use stargate::discovery::{
    Discovery, KubernetesPodDiscovery, KubernetesPodDiscoveryConfig, SelfOnlyDiscovery,
};
use stargate::proxy::{ProxyRetryConfig, ProxyTransportConfig, QuicTunnelConfig};
use stargate::runtime::{
    BoundStargateListeners, ReverseTunnelConfig, StargateRuntime, StargateRuntimeConfig,
};
use stargate_forwarding::{ForwardingResolver, HeadlessDnsResolver, render_hostname};
use stargate_tls::{ServerIdentityReloader, ServerTlsIdentity};

pub(super) struct RuntimeStartup {
    pub(super) runtime: StargateRuntime,
    pub(super) shutdown_drain_timeout: Duration,
}

pub(super) type DiscoveryAndForwarding = (Box<dyn Discovery>, Option<Arc<dyn ForwardingResolver>>);

pub(super) type WorkerAuthStartup = (String, Option<stargate_auth::AuthTokenProvider>);

pub(super) async fn runtime_from_config(config: StargateConfig) -> Result<RuntimeStartup> {
    config.validate()?;
    let proxy_transport = proxy_transport_config_from_config(&config)?;
    let reverse_tunnel = bind_reverse_tunnel_from_config(&config)?;
    let worker_auth = worker_auth_startup_from_config(config.worker_authentication.as_ref())?;

    let mut runtime_config = runtime_config_from_config(&config, proxy_transport);
    let listeners = BoundStargateListeners::bind(&mut runtime_config)?;
    let (discovery, forwarding) = make_discovery_with_resolver_and_addresses(
        &config,
        runtime_config.advertise_addr,
        runtime_config.http_listen_addr,
        make_resolver,
    )?;
    runtime_config.forwarding = forwarding;
    if let Some((endpoint, token_provider)) = worker_auth {
        let authenticator =
            stargate::auth::GrpcWorkerAuthenticator::connect(&endpoint, token_provider)
                .await
                .context("failed to connect to worker auth endpoint")?;
        runtime_config.authenticator = Arc::new(authenticator);
    }
    Ok(RuntimeStartup {
        runtime: StargateRuntime::new(runtime_config, discovery, listeners, reverse_tunnel),
        shutdown_drain_timeout: config.process_lifecycle.shutdown_drain_timeout,
    })
}

pub(super) fn proxy_transport_config_from_config(
    config: &StargateConfig,
) -> Result<ProxyTransportConfig> {
    let retry = proxy_retry_config_from_config(config)?;
    let reverse = config.pylon_transport.reverse.as_ref();
    let server_identity_reloader = match reverse {
        Some(reverse) => match (&reverse.certificate_path, &reverse.private_key_path) {
            (Some(cert_path), Some(key_path)) => Some(
                ServerIdentityReloader::load(cert_path.clone(), key_path.clone())
                    .context("load initial reverse listener TLS server identity")?,
            ),
            (None, None) => None,
            (Some(_), None) => {
                bail!("pylon_transport.reverse.private_key_path is required with certificate_path")
            }
            (None, Some(_)) => {
                bail!("pylon_transport.reverse.certificate_path is required with private_key_path")
            }
        },
        None => None,
    };
    let (tls_cert_pem, tls_key_pem) = match server_identity_reloader
        .as_ref()
        .map(ServerIdentityReloader::current_identity)
    {
        Some(ServerTlsIdentity::Provided { cert_pem, key_pem }) => {
            (Some(cert_pem.clone()), Some(key_pem.clone()))
        }
        Some(ServerTlsIdentity::SelfSigned) | None => (
            config
                .direct_transport()
                .trust_bundle_path
                .as_ref()
                .map(std::fs::read)
                .transpose()?,
            None,
        ),
    };
    Ok(ProxyTransportConfig {
        quic: QuicTunnelConfig {
            connect_timeout: config.pylon_transport.quic_connect_timeout,
            request_timeout: config.pylon_transport.quic_request_timeout,
            server_tls_identity: if reverse.is_some() {
                ServerTlsIdentity::from_optional_pem(tls_cert_pem.clone(), tls_key_pem)?
            } else {
                ServerTlsIdentity::SelfSigned
            },
            server_identity_reloader,
            tls_reload_interval: stargate_tls::DEFAULT_TLS_RELOAD_INTERVAL,
            tls_cert_pem,
            quic_insecure: config.pylon_transport.tls.insecure_skip_verify,
            tunnel_protocol: config.pylon_transport.tunnel_protocol,
            direct_quic_connections: config.direct_transport().connections.get(),
        },
        retry,
    })
}

pub(super) fn bind_reverse_tunnel_from_config(
    config: &StargateConfig,
) -> Result<Option<ReverseTunnelConfig>> {
    let Some(reverse) = &config.pylon_transport.reverse else {
        return Ok(None);
    };
    let kubernetes = config.stargate_identity.kubernetes.as_ref();
    Ok(Some(ReverseTunnelConfig::bind(
        reverse.listen_addr,
        render_hostname(
            &config.stargate_identity.advertised_hostname_template,
            kubernetes
                .map(|identity| identity.pod_name.as_str())
                .unwrap_or(&config.stargate_identity.id),
            kubernetes
                .map(|identity| identity.namespace.as_str())
                .unwrap_or(""),
        ),
        reverse.pylon_dial_addr.clone(),
        reverse.connect_timeout,
    )?))
}

pub(super) fn runtime_config_from_config(
    config: &StargateConfig,
    proxy_transport: ProxyTransportConfig,
) -> StargateRuntimeConfig {
    let pod_discovery = config.stargate_discovery.kubernetes_pods.as_ref();
    StargateRuntimeConfig {
        stargate_id: config.stargate_identity.id.clone(),
        grpc_listen_addr: config.stargate_network.grpc_listen_addr,
        model_discovery_listen_addr: config.stargate_network.model_discovery_listen_addr,
        http_listen_addr: config.stargate_network.http_listen_addr,
        readiness_warmup: config.process_lifecycle.readiness_warmup,
        metrics_listen_addr: Some(config.observability.metrics.listen_addr),
        advertise_addr: config.stargate_network.advertise_addr,
        kubernetes_pod_discovery_dns_name: pod_discovery
            .map(|pods| pods.headless_service_dns_name.clone()),
        remote_watch_stargate_urls: config.stargate_discovery.remote_watch_urls.clone(),
        grpc_pylon_dial_addr: config.pylon_transport.pylon_grpc_dial_uri.clone(),
        kubernetes_pod_discovery_poll_interval: pod_discovery
            .map(|pods| pods.poll_interval)
            .unwrap_or(Duration::from_secs(1)),
        watch_heartbeat_interval: config.stargate_discovery.watch_heartbeat,
        registration_update_idle_timeout: config.pylon_registration.update_idle_timeout,
        registration_update_max_idle_timeout: config.pylon_registration.update_max_idle_timeout,
        proxy_transport,
        lb_config_path: config
            .request_proxy
            .load_balancer
            .as_ref()
            .map(|load_balancer| load_balancer.config_path.to_string_lossy().into_owned()),
        metrics_prefix: config.observability.metrics.prefix.clone(),
        forwarding: None,
        authenticator: Arc::new(OpenAuthenticator),
    }
}

pub(super) fn make_discovery_with_resolver_and_addresses(
    config: &StargateConfig,
    advertise_addr: std::net::SocketAddr,
    http_listen_addr: std::net::SocketAddr,
    make_resolver: impl FnOnce(Duration) -> Result<hickory_resolver::TokioAsyncResolver>,
) -> Result<DiscoveryAndForwarding> {
    let Some(pods) = &config.stargate_discovery.kubernetes_pods else {
        return Ok((
            Box::new(SelfOnlyDiscovery::new(
                advertise_addr,
                config.stargate_identity.id.clone(),
                http_listen_addr.port(),
            )),
            None,
        ));
    };
    let identity = config
        .stargate_identity
        .kubernetes
        .as_ref()
        .context("stargate_discovery.kubernetes_pods requires stargate_identity.kubernetes")?;
    let resolver = make_resolver(pods.resolver_ttl)?;
    let forwarding = pods.development_peer_forwarding.as_ref().map(|_| {
        tracing::warn!(
            development_only = true,
            stargate_id = config.stargate_identity.id.as_str(),
            "development-only peer forwarding is enabled; it must not run in production"
        );
        Arc::new(HeadlessDnsResolver {
            self_pod_name: identity.pod_name.clone(),
            advertised_hostname_template: config
                .stargate_identity
                .advertised_hostname_template
                .clone(),
            namespace: identity.namespace.clone(),
            headless_dns_suffix: pods.headless_service_dns_name.clone(),
        }) as Arc<dyn ForwardingResolver>
    });
    Ok((
        Box::new(KubernetesPodDiscovery::new(KubernetesPodDiscoveryConfig {
            self_pod_name: identity.pod_name.clone(),
            pod_namespace: identity.namespace.clone(),
            advertised_hostname_template: config
                .stargate_identity
                .advertised_hostname_template
                .clone(),
            discovery_dns_name: pods.headless_service_dns_name.clone(),
            resolver,
            grpc_port: advertise_addr.port(),
        })),
        forwarding,
    ))
}

pub(super) fn proxy_retry_config_from_config(config: &StargateConfig) -> Result<ProxyRetryConfig> {
    let retry = &config.request_proxy.retry;
    let header = retry.request_budget_header.trim();
    let request_retry_budget_ms_header = (!header.is_empty())
        .then(|| {
            http::HeaderName::from_bytes(header.as_bytes())
                .with_context(|| format!("invalid proxy retry budget header: {header}"))
        })
        .transpose()?;
    Ok(ProxyRetryConfig {
        max_connect_retries: retry.max_connect_retries,
        max_request_retries: retry.max_request_retries,
        max_replay_body_bytes: retry.max_replay_body_bytes,
        require_pylon_retry_signal: retry.require_pylon_retry_signal,
        request_retry_budget_ms_header,
        ..ProxyRetryConfig::default()
    })
}

pub(super) fn make_resolver(ttl: Duration) -> Result<hickory_resolver::TokioAsyncResolver> {
    let (config, mut options) = hickory_resolver::system_conf::read_system_conf()
        .context("failed to read system resolver config")?;
    options.timeout = Duration::from_secs(1);
    options.attempts = 1;
    options.negative_max_ttl = Some(Duration::from_secs(0));
    options.positive_max_ttl = Some(ttl);
    Ok(hickory_resolver::TokioAsyncResolver::tokio(config, options))
}

/// Router OAuth2 scope, distinct from the gateway invocation scope.
const WORKER_AUTH_SCOPE: &str = "llm:check_worker";

pub(super) fn worker_auth_startup_from_config(
    auth: Option<&WorkerAuthenticationConfig>,
) -> Result<Option<WorkerAuthStartup>> {
    let Some(auth) = auth else {
        return Ok(None);
    };
    let token_provider = if let Some(oauth2) = &auth.oauth2 {
        Some(stargate_auth::AuthTokenProvider::client_credentials(
            &oauth2.provider_host,
            oauth2.secrets_path.clone(),
            WORKER_AUTH_SCOPE,
        ))
    } else {
        auth.bearer_token
            .as_ref()
            .map(|token| stargate_auth::AuthTokenProvider::JsonFile {
                path: token.secrets_path.clone(),
                key: token.json_path.clone(),
            })
    };
    Ok(Some((auth.endpoint.clone(), token_provider)))
}

#[cfg(test)]
pub(super) async fn runtime_from_args(args: super::Args) -> Result<RuntimeStartup> {
    runtime_from_config(super::config_from_legacy_args(args)?).await
}

#[cfg(test)]
pub(super) fn proxy_transport_config_from_args(args: &super::Args) -> Result<ProxyTransportConfig> {
    proxy_transport_config_from_config(&super::config_from_legacy_args(args.clone())?)
}

#[cfg(test)]
pub(super) fn bind_reverse_tunnel_from_args(
    args: &super::Args,
) -> Result<Option<ReverseTunnelConfig>> {
    bind_reverse_tunnel_from_config(&super::config_from_legacy_args(args.clone())?)
}

#[cfg(test)]
pub(super) fn runtime_config_from_args(
    args: &super::Args,
    proxy_transport: ProxyTransportConfig,
) -> Result<StargateRuntimeConfig> {
    Ok(runtime_config_from_config(
        &super::config_from_legacy_args(args.clone())?,
        proxy_transport,
    ))
}

#[cfg(test)]
pub(super) fn proxy_retry_config_from_args(args: &super::Args) -> Result<ProxyRetryConfig> {
    proxy_retry_config_from_config(&super::config_from_legacy_args(args.clone())?)
}

#[cfg(test)]
pub(super) fn validate_discovery_args(args: &super::Args) -> Result<()> {
    super::config_from_legacy_args(args.clone()).map(|_| ())
}

#[cfg(test)]
pub(super) fn worker_auth_startup_from_args(
    endpoint: Option<String>,
    secrets_path: Option<String>,
    secrets_json_path: Option<String>,
    oauth2_provider_host: Option<String>,
) -> Result<Option<WorkerAuthStartup>> {
    let Some(endpoint) = endpoint else {
        return Ok(None);
    };
    let secrets_path = secrets_path.map(std::path::PathBuf::from);
    let (bearer_token, oauth2) = if let Some(provider_host) = oauth2_provider_host {
        (
            None,
            Some(stargate::config::OAuth2Config {
                provider_host,
                secrets_path: secrets_path.context(
                    "OAUTH2_PROVIDER_HOST is set but SECRETS_PATH is not; client-credentials worker auth needs the secrets file with the id/secret",
                )?,
            }),
        )
    } else {
        (
            secrets_path.map(|secrets_path| stargate::config::BearerTokenConfig {
                secrets_path,
                json_path: secrets_json_path
                    .as_deref()
                    .unwrap_or("authToken")
                    .split('.')
                    .map(str::to_owned)
                    .collect(),
            }),
            None,
        )
    };
    worker_auth_startup_from_config(Some(&WorkerAuthenticationConfig {
        endpoint,
        bearer_token,
        oauth2,
    }))
}

#[cfg(test)]
mod tests {
    #[test]
    fn system_resolver_initializes_from_host_configuration() {
        super::make_resolver(std::time::Duration::from_secs(3))
            .expect("the host resolver configuration should initialize");
    }
}
