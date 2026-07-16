#![recursion_limit = "256"]

/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

use rs_autoscaler::health::HealthStatus;
use rs_autoscaler::{
    health::Health,
    metrics,
    nvcf_api::{nvcf_client::NvcfApiService, oauth2_client::OAuth2Client},
    routes,
    scaling::{policy_cache::PolicyCache, ScalingPolicy, ScalingSettings},
    secrets::secrets_file_watcher::SecretFileWatcher,
    settings, startup, timeseries_db, work,
    work::new_function_state_cache,
};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};
use std::{collections::HashMap, net::SocketAddr};

use axum::{http::StatusCode, response::IntoResponse, routing::get, Router};
use rand::Rng;
use rs_autoscaler::tracing_init;
use tokio::signal;

const NODE_HEALTH_CHECK_INTERVAL: Duration = Duration::from_secs(30);

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let project_name = env!("CARGO_PKG_NAME");
    let node_id = gethostname::gethostname()
        .into_string()
        .unwrap_or_else(|_| uuid::Uuid::new_v4().to_string());
    let region: String = std::env::var("AWS_REGION").unwrap_or_default();
    tracing::info!("Node ID: {}", node_id);
    let node_id = if !region.is_empty() {
        format!("{}.{}", node_id, region)
    } else {
        node_id
    };
    // Initialize shutdown signal
    let shutdown = Arc::new(AtomicBool::new(false));

    let (_cli_args, config) = settings::parse_settings();

    let secrets_path = config
        .secrets_path
        .as_ref()
        .ok_or("secrets_path is required but not provided in configuration")?;

    let metrics_settings = &config.server.metrics;
    metrics::init_metrics(metrics_settings).expect("failed initializing metrics");

    let secrets_file_watcher = Arc::new(SecretFileWatcher::new(secrets_path).await?);

    let tracing_key = secrets_file_watcher.get_config().tracing;

    let mut tracing_settings = config.server.tracing.clone();
    if let Some(tracing_key) = tracing_key {
        // Preserve configured tracing headers while replacing the token placeholder.
        let headers = tracing_settings.headers.get_or_insert_with(HashMap::new);
        headers.insert("lightstep-access-token".to_string(), tracing_key.access_key);
    }

    let _tracing_guard = tracing_init::initialize_tracing(
        project_name,
        &tracing_settings,
        config.server.resource.as_ref(),
        config.server.envfilter_directive.clone(),
    );
    tracing::info!(
        "Starting {} version {}",
        project_name,
        env!("CARGO_PKG_VERSION")
    );
    tracing::warn!("App settings are: {:?}", config);

    tracing::info!("Initializing health");
    let health = Arc::new(Health::new());

    // Pre-register dependencies as unhealthy before the probe server starts,
    // so readiness returns 503 from the very first request.
    health.set_component_unhealthy("cassandra_client", Some("initializing".to_string()));
    health.set_component_unhealthy("timeseries_db_client", Some("initializing".to_string()));

    // Start the probe server immediately so liveness probes respond while waiting for dependencies.
    // Liveness always returns 200; readiness returns 503 until Cassandra/TimeseriesDb are healthy.
    const PROBE_PORT: u16 = 8181;
    let health_app = Router::new()
        .route("/admin/health/liveness", get(routes::get_liveness))
        .route("/admin/health/readiness", get(routes::get_readiness))
        .route("/health", get(routes::get_health))
        .with_state(health.clone());
    let probe_addr = SocketAddr::from(([0, 0, 0, 0], PROBE_PORT));
    let probe_listener = tokio::net::TcpListener::bind(probe_addr).await?;
    tracing::info!("Probe server listening on {}", probe_addr);
    let shutdown_probe = shutdown.clone();
    tokio::spawn(async move {
        if let Err(e) = axum::serve(probe_listener, health_app.into_make_service())
            .with_graceful_shutdown(async move {
                while !shutdown_probe.load(Ordering::Relaxed) {
                    tokio::time::sleep(Duration::from_millis(100)).await;
                }
            })
            .await
        {
            tracing::error!(error = ?e, "Probe server failed");
        }
    });

    tracing::info!("Initializing Cassandra client (waiting with exponential backoff until up)");
    let cassandra_service_manager =
        startup::wait_for_cassandra(&config.cassandra, secrets_file_watcher.clone()).await;

    health.register_health_checker(cassandra_service_manager.clone());

    tracing::info!("Initializing TimeseriesDb client (waiting with exponential backoff until up)");
    let timeseries_db_credential_provider = if config.timeseries_db.effective_auth_mode()
        == timeseries_db::TimeseriesDbAuthMode::Token
    {
        tracing::info!("Initializing TimeseriesDb credentials provider for token generation");
        Some(Arc::new(
            timeseries_db::timeseries_db_client::AuthnCredentialProvider::new(
                &config.timeseries_db,
                secrets_file_watcher.clone(),
            ),
        )
            as Arc<
                dyn timeseries_db::timeseries_db_client::CredentialProvider + Send + Sync,
            >)
    } else {
        None
    };

    let timeseries_db_client =
        startup::wait_for_timeseries_db(&config.timeseries_db, timeseries_db_credential_provider)
            .await?;

    health.register_health_checker(timeseries_db_client.clone());

    // Start coordinated health monitoring for all registered services
    health
        .clone()
        .start_health_monitoring(NODE_HEALTH_CHECK_INTERVAL);

    // Initialize scaling settings with policy cache if using Custom policy
    tracing::info!("Initializing scaling settings");
    let scaling_settings = initialize_scaling_settings(
        config.scaling.clone(),
        config.nvcf_api.oauth2_token_api_address.clone(),
        config.nvcf_api.oauth2_token_scope.clone(),
        config.nvcf_api.nvcf_api_grpc_address.clone(),
        secrets_file_watcher.clone(),
    )
    .await;

    tracing::info!("Starting thread for node health reporting");
    let cassandra_health_manager = Arc::clone(&cassandra_service_manager);
    let health_monitor = Arc::clone(&health);
    let node_id_health = node_id.clone();

    tracing::info!(
        "Creating a new node entry for {} if not exists",
        node_id_health
    );
    // Create a new node entry in Cassandra
    let current_health = health_monitor.get_health();
    if current_health.overall_status == HealthStatus::Healthy {
        work::create_new_node(
            &node_id_health,
            &HealthStatus::Healthy,
            &cassandra_health_manager,
        )
        .await?;
    } else {
        panic!(
            "Node is not healthy, cannot start service: {:?}",
            current_health.overall_status
        );
    }

    tokio::spawn(async move {
        let mut interval = tokio::time::interval(NODE_HEALTH_CHECK_INTERVAL);

        loop {
            interval.tick().await;
            let current_health = health_monitor.get_health();
            tracing::info!("Node health check - Overall Status: {:?}, Components: {}, Last updated: {}, Message: {:?}", 
                current_health.overall_status,
                current_health.components.len(),
                current_health.last_updated,
                current_health.message
            );
            if let Err(e) = work::create_new_node(
                &node_id_health,
                &current_health.overall_status,
                &cassandra_health_manager,
            )
            .await
            {
                tracing::error!("Failed to report node health: {}", e);
            }
        }
    });

    tracing::info!("Initializing thread to find buckets");
    let bucket_manager =
        work::bucket::NodeBucketManager::new(node_id.clone(), cassandra_service_manager.clone());
    let lock_manager = Arc::new(
        rs_autoscaler::cassandra::distributed_lock::DistributedLockManager::new(
            cassandra_service_manager.clone(),
            node_id.clone(),
        ),
    );

    let function_state_cache = Arc::new(new_function_state_cache());

    tracing::info!("Initializing NVCF API client");
    let cassandra_service_manager_nvcf = Arc::clone(&cassandra_service_manager);
    let nvcf_api_service = Arc::new(NvcfApiService::new(
        config.nvcf_api.clone(),
        Some(cassandra_service_manager_nvcf),
        secrets_file_watcher.clone(),
        bucket_manager.clone(),
        lock_manager.clone(),
        Some(Arc::clone(&function_state_cache)),
    ));

    tracing::info!("Starting thread to discover new functions");
    let cassandra_function_manager = Arc::clone(&cassandra_service_manager);
    let timeseries_db_client_discover = Arc::clone(&timeseries_db_client);
    let timeseries_db_env_discover = config.timeseries_db.env.clone();
    let timeseries_db_ignore_env = config.timeseries_db.ignore_env;
    let lock_manager_discover = Arc::clone(&lock_manager);
    let discovery_task = tokio::spawn(async move {
        let jitter_millis = rand::rng().random_range(0..5000);
        let discovery_interval =
            config.scaling.discover_new_functions_interval + Duration::from_millis(jitter_millis);
        let mut interval = tokio::time::interval(discovery_interval);
        loop {
            let start_time = Instant::now();
            tracing::info!(
                "Autoscaler discovering new functions in env: {}",
                timeseries_db_env_discover,
            );
            match work::discovery::discover_new_functions(
                cassandra_function_manager.clone(),
                lock_manager_discover.clone(),
                &timeseries_db_client_discover,
                &timeseries_db_env_discover,
                timeseries_db_ignore_env,
                config.scaling.discovery_lock_duration.as_secs() as i32,
            )
            .await
            {
                Err(work::discovery::FunctionDiscoveryError::LockAcquisitionFailed) => {
                    tracing::debug!(
                        "Autoscaler discovering new functions skipped - lock unavailable"
                    );
                }
                Err(e) => {
                    tracing::error!("Autoscaler discovering new functions failed: {}", e);
                }
                Ok(()) => {
                    let duration = start_time.elapsed();
                    tracing::info!(
                        "Autoscaler discovering new functions completed in env: {} in {:?} seconds",
                        timeseries_db_env_discover,
                        duration.as_secs()
                    );
                    metrics::record_function_discovery_duration(duration);
                }
            }
            interval.tick().await;
        }
    });

    tracing::info!("Starting thread to run autoscaling logic- P0");
    let cassandra_autoscaling_manager = Arc::clone(&cassandra_service_manager);
    let timeseries_db_client_autoscaling = Arc::clone(&timeseries_db_client);
    let nvcf_api_service_autoscaling = Arc::clone(&nvcf_api_service);
    let scaling_settings_p0 = Arc::new(scaling_settings.clone());
    let timeseries_db_env_p0 = config.timeseries_db.env.clone();
    let bucket_manager_p0 = Arc::clone(&bucket_manager);
    let lock_manager_p0 = Arc::clone(&lock_manager);
    let function_state_cache_p0 = Arc::clone(&function_state_cache);
    let scaling_loop_interval = config.scaling.scaling_loop_interval;
    tokio::spawn(async move {
        let mut interval = tokio::time::interval(scaling_loop_interval);
        loop {
            if let Err(e) = work::run_autoscaling_logic_p0(
                cassandra_autoscaling_manager.clone(),
                timeseries_db_client_autoscaling.clone(),
                nvcf_api_service_autoscaling.clone(),
                scaling_settings_p0.clone(),
                &timeseries_db_env_p0,
                timeseries_db_ignore_env,
                &bucket_manager_p0,
                &lock_manager_p0,
                Arc::clone(&function_state_cache_p0),
            )
            .await
            {
                tracing::error!("Failed to run autoscaling logic: {}", e);
            }
            interval.tick().await;
        }
    });

    // Main app on 8080
    let app = Router::new()
        .route("/health", get(routes::get_health))
        .route("/admin/health/liveness", get(routes::get_liveness))
        .route("/admin/health/readiness", get(routes::get_readiness))
        .with_state(health.clone())
        .fallback(handler_404);
    let addr = SocketAddr::from(([0, 0, 0, 0], 8080));

    let shutdown_clone = shutdown.clone();
    let shutdown_signal = async move {
        signal::ctrl_c()
            .await
            .expect("failed to install Ctrl+C handler");
        tracing::warn!("Received SIGINT signal - shutting down gracefully");
        shutdown_clone.store(true, Ordering::Relaxed);
    };

    let listener = tokio::net::TcpListener::bind(addr).await?;
    tracing::info!("Listening on {}", addr);

    let server =
        axum::serve(listener, app.into_make_service()).with_graceful_shutdown(shutdown_signal);

    tokio::select! {
        result = server => result?,
        result = discovery_task => {
            let message = match result {
                Ok(()) => "function discovery task exited unexpectedly".to_string(),
                Err(error) => format!("function discovery task failed: {error}"),
            };
            tracing::error!("{message}");
            return Err(std::io::Error::other(message).into());
        }
    }

    tracing::warn!("Server terminated");
    Ok(())
}

async fn handler_404() -> impl IntoResponse {
    (StatusCode::NOT_FOUND, "route not found")
}

/// Initialize scaling settings with policy cache if using Custom policy mode
async fn initialize_scaling_settings(
    mut settings: ScalingSettings,
    oauth2_token_api_address: String,
    oauth2_token_scope: String,
    nvcf_api_grpc_address: String,
    secrets_watcher: Arc<SecretFileWatcher>,
) -> ScalingSettings {
    match &settings.policy {
        ScalingPolicy::Static(_) => {
            tracing::info!(
                "Using Static scaling policy - global thresholds/factors for all functions"
            );
            settings
        }
        ScalingPolicy::Custom(config) => {
            tracing::info!(
                "Using Custom scaling policy - per-function configs from gRPC endpoint: {}",
                nvcf_api_grpc_address
            );
            tracing::info!("Cache configuration: TTL={}s", config.ttl_seconds);

            // Create OAuth2 client for gRPC auth (same auth mechanism as NVCF API)
            let oauth2_client = match OAuth2Client::new(
                oauth2_token_api_address,
                oauth2_token_scope,
                secrets_watcher,
            ) {
                Ok(client) => Arc::new(client),
                Err(e) => {
                    tracing::error!("Failed to create OAuth2 client for policy cache: {}. Falling back to default thresholds.", e);
                    return settings;
                }
            };

            // Initialize the policy cache. Uses the same NVCF API gRPC
            // endpoint as the scaling loop's Autoscaler client, since both
            // are the same upstream Autoscaler service.
            match PolicyCache::new(
                nvcf_api_grpc_address,
                oauth2_client,
                config.ttl_seconds,
                settings.policy_cache_max_capacity,
                config.default_thresholds.clone(),
                config.default_factors.clone(),
            ) {
                Ok(policy_cache) => {
                    settings.policy_cache = Some(Arc::new(policy_cache));
                    tracing::info!("Policy cache initialized successfully");
                }
                Err(e) => {
                    tracing::error!("Failed to initialize policy cache: {}. Falling back to default thresholds.", e);
                }
            }
            settings
        }
    }
}
