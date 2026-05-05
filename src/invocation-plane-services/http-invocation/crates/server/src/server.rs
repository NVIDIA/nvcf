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

use axum::serve::ListenerExt;
#[cfg(feature = "jemalloc")]
use nvcf_invocation_service::profiling::pprof_server;
use nvcf_invocation_service::telemetry;
use nvcf_invocation_service::{
    app::app, metrics, secrets::secret_provider::SecretFileWatcher, settings,
};
use std::{collections::HashMap, sync::Arc, time::Duration};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    rustls::crypto::ring::default_provider()
        .install_default()
        .expect("Failed to install default CryptoProvider");

    let project_name = env!("CARGO_PKG_NAME");

    let (_cli_args, mut config) = settings::parse_config();

    // override pod ip if not set in worker stream properties.
    //POD_IP comes in after user supplied env vars are supplied, so it cannot be used in dependent variables.
    if let (Some(pod_ip), None) = (&config.pod_ip, &config.worker_stream_properties.pod_ip) {
        config.worker_stream_properties.pod_ip = Some(pod_ip.to_owned());
    }

    let metrics_settings = &config.server.metrics;
    metrics::init_metrics(metrics_settings)?;

    // Load secrets file directly to get tracing key before initializing tracing
    let tracing_key = if let Some(secrets_path) = &config.secrets_path {
        match SecretFileWatcher::<nvcf_invocation_service::secrets::secret_config::Secrets>::load_config_from_file(secrets_path).await {
            Ok(secrets) => Some(secrets.tracing.access_key.clone()),
            Err(_) => None,
        }
    } else {
        None
    };

    // If Lightstep key found, then inject the value into the tracing settings headers.
    let mut tracing_settings = config.server.tracing.clone();
    if let Some(tracing_key) = tracing_key {
        // Ensure headers is initialized to a default HashMap if None
        let headers = tracing_settings.headers.get_or_insert_with(HashMap::new);
        headers.insert(
            "lightstep-access-token".to_string(),
            tracing_key.to_string(),
        );
    }

    // Initialize tracing first
    let _tracing_guard = telemetry::initialize_tracing(
        project_name,
        &tracing_settings,
        config.server.resource.as_ref(),
        config
            .server
            .envfilter_directive
            .clone()
            .unwrap_or_default(),
    );

    // Create the secrets watcher after tracing is initialized so that its actions are logged.
    let secrets: Option<Arc<SecretFileWatcher>> = match &config.secrets_path {
        None => None,
        Some(secrets) => Some(Arc::new(
            SecretFileWatcher::new(
                secrets,
                Duration::from_secs(config.secrets_watcher_interval_seconds),
            )
            .await?,
        )),
    };
    let tcp_nodelay = config.tcp_nodelay;
    tracing::info!("Starting {}...", &project_name);
    tracing::warn!("App settings are: {:?}", config);

    let app = app(config, secrets).await?;

    // Start pprof server on a separate port when jemalloc and profiling features are enabled
    #[cfg(all(feature = "jemalloc", feature = "profiling"))]
    {
        let pprof_port = 6060; // Standard pprof port
        tokio::spawn(async move {
            if let Err(e) = pprof_server::start_pprof_server(pprof_port).await {
                tracing::error!("Failed to start pprof server: {}", e);
            }
        });
        tracing::info!("Started pprof server on port {}", pprof_port);
    }

    let quit_sig = async {
        let _ = tokio::signal::ctrl_c().await;
        tracing::warn!("Caught Ctrl+C! Initiating graceful shutdown.");
    };

    // Create listener with SO_REUSEADDR (for quick restarts) and explicit backlog.
    // Note: In axum 0.8, `axum::serve().tcp_nodelay()` was removed. TCP_NODELAY is a
    // per-connection socket option that must be set on each accepted connection, not
    // the listening socket. We use `tap_io()` below to apply it correctly.
    let socket = tokio::net::TcpSocket::new_v4()?;
    socket.set_reuseaddr(true)?;
    socket.bind("0.0.0.0:8080".parse()?)?;
    let listener = socket.listen(1024)?;
    tracing::info!("listening on http://{}", listener.local_addr()?);

    // Use tap_io to set TCP_NODELAY on each accepted connection (per-connection option)
    let listener = listener.tap_io(move |tcp_stream| {
        if let Err(err) = tcp_stream.set_nodelay(tcp_nodelay) {
            tracing::trace!("failed to set TCP_NODELAY on incoming connection: {err:#}");
        }
    });

    let nvcf_router =
        axum::serve(listener, app.into_make_service()).with_graceful_shutdown(quit_sig);

    nvcf_router.await?;
    Ok(())
}
