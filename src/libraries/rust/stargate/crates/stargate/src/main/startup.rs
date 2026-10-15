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
use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, Result, ensure};
use stargate::auth::OpenAuthenticator;
use stargate::discovery::{
    Discovery, DnsDiscovery, HeadlessDnsDiscovery, HeadlessDnsDiscoveryConfig, SelfOnlyDiscovery,
};
use stargate::proxy::{ProxyRetryConfig, ProxyTransportConfig, QuicTunnelConfig};
use stargate::runtime::{
    BoundStargateListeners, ReverseTunnelConfig, StargateRuntime, StargateRuntimeConfig,
};
use stargate_forwarding::{ForwardingResolver, HeadlessDnsResolver, render_hostname};
use stargate_protocol::BackendConnectivity;
use stargate_tls::ServerTlsIdentity;

use super::Args;

pub(super) struct RuntimeStartup {
    pub(super) runtime: StargateRuntime,
    pub(super) shutdown_drain_timeout: Duration,
}

pub(super) type DiscoveryAndForwarding = (Box<dyn Discovery>, Option<Arc<dyn ForwardingResolver>>);

pub(super) type WorkerAuthStartup = (String, Option<stargate_auth::AuthTokenProvider>);

pub(super) async fn runtime_from_args(mut args: Args) -> Result<RuntimeStartup> {
    validate_backend_connectivity_args(&args)?;
    validate_discovery_args(&args)?;
    let proxy_transport = proxy_transport_config_from_args(&args)?;
    let reverse_tunnel = bind_reverse_tunnel_from_args(&args)?;
    let worker_auth = worker_auth_startup_from_args(
        args.worker_auth_endpoint.take(),
        args.secrets_path.take(),
        args.secrets_json_path.take(),
        args.oauth2_provider_host.take(),
    )?;

    let mut runtime_config = runtime_config_from_args(&args, proxy_transport)?;
    let listeners = BoundStargateListeners::bind(&mut runtime_config)?;
    let (discovery, forwarding) = make_discovery_with_resolver_and_addresses(
        &args,
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
        shutdown_drain_timeout: Duration::from_millis(args.shutdown_drain_timeout_ms),
    })
}

fn validate_backend_connectivity_args(args: &Args) -> Result<()> {
    match args.backend_connectivity {
        BackendConnectivity::Direct => {
            ensure!(
                args.reverse_tunnel_listen_addr.is_none(),
                "--reverse-tunnel-listen-addr requires --backend-connectivity=reverse"
            );
            ensure!(
                args.reverse_tunnel_pylon_dial_addr.is_none(),
                "--reverse-tunnel-pylon-dial-addr requires --backend-connectivity=reverse"
            );
        }
        BackendConnectivity::Reverse => ensure!(
            args.reverse_tunnel_listen_addr.is_some(),
            "--backend-connectivity=reverse requires --reverse-tunnel-listen-addr"
        ),
    }
    Ok(())
}

pub(super) fn proxy_transport_config_from_args(args: &Args) -> Result<ProxyTransportConfig> {
    let retry = proxy_retry_config_from_args(args)?;
    let tls_cert_pem = args.tls_cert_path.as_ref().map(std::fs::read).transpose()?;
    let tls_key_pem = args.tls_key_path.as_ref().map(std::fs::read).transpose()?;
    Ok(ProxyTransportConfig {
        quic: QuicTunnelConfig {
            connect_timeout: Duration::from_millis(args.quic_connect_timeout_ms),
            request_timeout: Duration::from_millis(args.quic_request_timeout_ms),
            server_tls_identity: if args.reverse_tunnel_listen_addr.is_some() {
                ServerTlsIdentity::from_optional_pem(tls_cert_pem.clone(), tls_key_pem)?
            } else {
                ServerTlsIdentity::SelfSigned
            },
            tls_cert_pem,
            quic_insecure: args.quic_insecure,
            tunnel_protocol: args.tunnel_protocol,
            direct_quic_connections: args.direct_quic_connections,
        },
        retry,
    })
}

pub(super) fn bind_reverse_tunnel_from_args(args: &Args) -> Result<Option<ReverseTunnelConfig>> {
    let Some(listen_addr) = args.reverse_tunnel_listen_addr.as_deref() else {
        return Ok(None);
    };
    Ok(Some(ReverseTunnelConfig::bind(
        listen_addr.parse()?,
        render_hostname(
            args.advertised_hostname_template
                .as_deref()
                .unwrap_or("{pod_name}.stargate.external"),
            args.pod_name.as_deref().unwrap_or(&args.stargate_id),
            args.pod_namespace.as_deref().unwrap_or(""),
        ),
        args.reverse_tunnel_pylon_dial_addr.clone(),
        Duration::from_millis(args.reverse_tunnel_connect_timeout_ms),
    )?))
}

pub(super) fn runtime_config_from_args(
    args: &Args,
    proxy_transport: ProxyTransportConfig,
) -> Result<StargateRuntimeConfig> {
    let millis = Duration::from_millis;
    Ok(StargateRuntimeConfig {
        stargate_id: args.stargate_id.clone(),
        grpc_listen_addr: args.listen_addr.parse()?,
        model_discovery_listen_addr: args.model_discovery_listen_addr.parse()?,
        http_listen_addr: args.http_listen_addr.parse()?,
        metrics_listen_addr: Some(SocketAddr::from(([0, 0, 0, 0], args.metrics_port))),
        advertise_addr: args.advertise_addr,
        stargate_discovery_dns_name: args.stargate_discovery_dns_name.clone(),
        remote_watch_stargate_urls: args.remote_stargate_url.clone(),
        grpc_pylon_dial_addr: args.grpc_pylon_dial_addr.clone(),
        dns_poll_interval: millis(args.dns_poll_ms),
        watch_heartbeat_interval: millis(args.watch_heartbeat_ms),
        registration_update_idle_timeout: millis(args.registration_update_idle_timeout_ms),
        registration_update_max_idle_timeout: millis(args.registration_update_max_idle_timeout_ms),
        proxy_transport,
        lb_config_path: args.lb_config_path.clone(),
        metrics_prefix: args.metrics_prefix.clone(),
        forwarding: None,
        authenticator: Arc::new(OpenAuthenticator),
    })
}

pub(super) fn make_discovery_with_resolver_and_addresses(
    args: &Args,
    advertise_addr: SocketAddr,
    http_listen_addr: SocketAddr,
    make_resolver: impl FnOnce(Duration) -> Result<hickory_resolver::TokioAsyncResolver>,
) -> Result<DiscoveryAndForwarding> {
    validate_discovery_args(args)?;

    if args.disable_dns_discovery {
        return Ok((
            Box::new(SelfOnlyDiscovery::new(
                advertise_addr,
                args.stargate_id.clone(),
                http_listen_addr.port(),
            )),
            None,
        ));
    }

    let resolver = make_resolver(Duration::from_millis(args.dns_resolver_ttl_ms))?;
    if let (Some(pod_name), Some(pod_namespace)) = (&args.pod_name, &args.pod_namespace) {
        let template = args
            .advertised_hostname_template
            .clone()
            .unwrap_or_else(|| "{pod_name}.stargate.external".to_string());
        let forwarding = args.enable_dev_peer_forwarding.then(|| {
            tracing::warn!(
                development_only = true,
                stargate_id = args.stargate_id.as_str(),
                "development-only peer forwarding is enabled; it must not run in production"
            );
            Arc::new(HeadlessDnsResolver {
                self_pod_name: pod_name.clone(),
                advertised_hostname_template: template.clone(),
                namespace: pod_namespace.clone(),
                headless_dns_suffix: args.stargate_discovery_dns_name.clone(),
            }) as Arc<dyn ForwardingResolver>
        });
        return Ok((
            Box::new(HeadlessDnsDiscovery::new(HeadlessDnsDiscoveryConfig {
                self_pod_name: pod_name.clone(),
                pod_namespace: pod_namespace.clone(),
                advertised_hostname_template: template,
                discovery_dns_name: args.stargate_discovery_dns_name.clone(),
                resolver,
                grpc_port: advertise_addr.port(),
            })),
            forwarding,
        ));
    }

    Ok((
        Box::new(DnsDiscovery::new(
            advertise_addr,
            args.stargate_id.clone(),
            args.stargate_discovery_dns_name.clone(),
            resolver,
            http_listen_addr.port(),
        )),
        None,
    ))
}

pub(super) fn validate_discovery_args(args: &Args) -> Result<()> {
    ensure!(
        !(args.disable_dns_discovery && args.enable_dev_peer_forwarding),
        "--enable-dev-peer-forwarding cannot be combined with --disable-dns-discovery"
    );
    ensure!(
        !args.enable_dev_peer_forwarding
            || (args.pod_name.is_some() && args.pod_namespace.is_some()),
        "--enable-dev-peer-forwarding requires both --pod-name and --pod-namespace"
    );
    Ok(())
}

pub(super) fn proxy_retry_config_from_args(args: &Args) -> Result<ProxyRetryConfig> {
    let header = args.proxy_retry_budget_header.trim();
    let request_retry_budget_ms_header = (!header.is_empty())
        .then(|| {
            http::HeaderName::from_bytes(header.as_bytes())
                .with_context(|| format!("invalid proxy retry budget header: {header}"))
        })
        .transpose()?;
    Ok(ProxyRetryConfig {
        max_connect_retries: args.proxy_max_connect_retries,
        max_request_retries: args.proxy_max_request_retries,
        max_replay_body_bytes: args.proxy_max_replay_body_bytes,
        require_pylon_retry_signal: args.proxy_require_pylon_retry_signal,
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

pub(super) fn worker_auth_startup_from_args(
    endpoint: Option<String>,
    secrets_path: Option<String>,
    secrets_json_path: Option<String>,
    oauth2_provider_host: Option<String>,
) -> Result<Option<WorkerAuthStartup>> {
    let Some(endpoint) = endpoint else {
        return Ok(None);
    };
    let token_provider = if let Some(host) = oauth2_provider_host {
        let path = secrets_path.ok_or_else(|| {
            anyhow::anyhow!(
                "OAUTH2_PROVIDER_HOST is set but SECRETS_PATH is not; client-credentials worker auth needs the secrets file with the id/secret"
            )
        })?;
        Some(stargate_auth::AuthTokenProvider::client_credentials(
            &host,
            path.into(),
            WORKER_AUTH_SCOPE,
        ))
    } else {
        secrets_path.map(|path| stargate_auth::AuthTokenProvider::JsonFile {
            path: path.into(),
            key: secrets_json_path
                .as_deref()
                .unwrap_or("authToken")
                .split('.')
                .map(str::to_owned)
                .collect(),
        })
    };
    Ok(Some((endpoint, token_provider)))
}

#[cfg(test)]
mod tests {
    #[test]
    fn system_resolver_initializes_from_host_configuration() {
        super::make_resolver(std::time::Duration::from_secs(3))
            .expect("the host resolver configuration should initialize");
    }
}
