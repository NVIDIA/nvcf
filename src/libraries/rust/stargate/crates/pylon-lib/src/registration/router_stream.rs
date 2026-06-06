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
use std::sync::Arc;
use std::time::{Duration, Instant};

use anyhow::Context;
use tokio::sync::{mpsc, watch};
use tokio::task::JoinHandle;
use tokio_stream::wrappers::ReceiverStream;
use tokio_util::sync::CancellationToken;

use stargate_auth::AuthTokenProvider;
use stargate_proto::REGISTRATION_HEARTBEAT_MS_METADATA;
use stargate_proto::pb::stargate_control_plane_client::StargateControlPlaneClient;
use stargate_proto::pb::{InferenceServerAck, InferenceServerRegistration, InferenceServerStatus};
use stargate_protocol::TunnelTransportProtocol;

use crate::quic_http_tunnel::TunnelForwardingConfig;
use crate::stats::PylonMetrics;

use super::calibration::{ClusterCalibrationDirective, publish_cluster_calibration_directives};
use super::grpc_endpoint::{StargateGrpcEndpoint, connect_stargate_grpc_channel};
use super::reverse_tunnel::{
    ReverseTunnelLoopConfig, reverse_tunnel_endpoint_from_ack, run_reverse_tunnel_loop,
    stop_reverse_tunnel_task,
};
use super::state::{
    AdvertisedModelStatus, SharedInstState, advertised_model_statuses,
    build_inference_server_registration,
};
use super::types::InferenceServerRegistrationConfig;
use super::{
    NamedJoinHandle, REGISTRATION_TASK_SHUTDOWN_TIMEOUT, await_named_join_handle,
    registration_should_stop, sleep_until_registration_stop,
};

#[derive(Debug, Clone)]
pub(super) struct RouterRegistrationTaskTemplate {
    inference_server_id: String,
    cluster_id: String,
    inference_server_url: String,
    min_update_interval: Duration,
    reverse_tunnel: bool,
    pub(super) coordinated_calibration: bool,
    quic_insecure: bool,
    tunnel_protocol: TunnelTransportProtocol,
    cluster_calibration_directive_tx: flume::Sender<ClusterCalibrationDirective>,
    forwarding: TunnelForwardingConfig,
    cancel_token: CancellationToken,
    auth_token_provider: Option<Arc<AuthTokenProvider>>,
}

impl RouterRegistrationTaskTemplate {
    pub(super) fn from_registration_config(
        register_config: &InferenceServerRegistrationConfig,
        cluster_id: &str,
        upstream_http_base_url: &str,
        cluster_calibration_directive_tx: flume::Sender<ClusterCalibrationDirective>,
        cancel_token: &CancellationToken,
    ) -> Self {
        let inference_server_url = if register_config.reverse_tunnel {
            upstream_http_base_url.to_string()
        } else {
            register_config.inference_server_url.clone()
        };
        Self {
            inference_server_id: register_config.inference_server_id.clone(),
            cluster_id: cluster_id.to_string(),
            inference_server_url,
            min_update_interval: register_config.min_update_interval,
            reverse_tunnel: register_config.reverse_tunnel,
            coordinated_calibration: register_config.bringup.enabled
                && register_config.bringup.calibration_requests > 0,
            quic_insecure: register_config.quic_insecure,
            tunnel_protocol: register_config.tunnel_protocol,
            cluster_calibration_directive_tx,
            forwarding: tunnel_forwarding_config_from_registration_config(
                register_config,
                upstream_http_base_url,
            ),
            cancel_token: cancel_token.clone(),
            auth_token_provider: register_config.auth_token_provider.clone(),
        }
    }

    pub(super) fn build_for_router(
        &self,
        router_endpoint: StargateGrpcEndpoint,
    ) -> RouterRegistrationTaskConfig {
        RouterRegistrationTaskConfig {
            router_endpoint,
            inference_server_id: self.inference_server_id.clone(),
            cluster_id: self.cluster_id.clone(),
            inference_server_url: self.inference_server_url.clone(),
            min_update_interval: self.min_update_interval,
            reverse_tunnel: self.reverse_tunnel,
            coordinated_calibration: self.coordinated_calibration,
            quic_insecure: self.quic_insecure,
            tunnel_protocol: self.tunnel_protocol,
            cluster_calibration_directive_tx: self.cluster_calibration_directive_tx.clone(),
            forwarding: self.forwarding.clone(),
            cancel_token: self.cancel_token.clone(),
            auth_token_provider: self.auth_token_provider.clone(),
        }
    }
}

fn tunnel_forwarding_config_from_registration_config(
    register_config: &InferenceServerRegistrationConfig,
    upstream_http_base_url: &str,
) -> TunnelForwardingConfig {
    let mut forwarding = TunnelForwardingConfig::new(upstream_http_base_url.to_string());
    forwarding.output_token_parser_factory = register_config.output_token_parser_factory.clone();
    forwarding.request_observation_tx = register_config.request_observation_tx.clone();
    forwarding.request_quality_monitor = register_config.request_quality_monitor.clone();
    forwarding.metrics = register_config.metrics.clone();
    forwarding.retry = register_config.retry.clone();
    forwarding.queue_mismatch_retry = register_config.queue_mismatch_retry.clone();
    forwarding.queue_tracker = register_config.queue_tracker.clone();
    forwarding
}

pub(super) fn desired_registration_routers(
    active_routers: &BTreeSet<StargateGrpcEndpoint>,
) -> BTreeSet<StargateGrpcEndpoint> {
    active_routers.clone()
}

pub(super) struct RouterRegistrationWorker {
    pub(super) stop_tx: watch::Sender<bool>,
    pub(super) task: JoinHandle<()>,
}

pub(super) async fn stop_router_registration_worker(worker: RouterRegistrationWorker) {
    let _ = worker.stop_tx.send(true);
    await_named_join_handle(
        NamedJoinHandle::new("router registration stream", worker.task),
        REGISTRATION_TASK_SHUTDOWN_TIMEOUT,
    )
    .await;
}

#[derive(Debug, Clone)]
pub(super) struct RouterRegistrationTaskConfig {
    pub(super) router_endpoint: StargateGrpcEndpoint,
    pub(super) inference_server_id: String,
    pub(super) cluster_id: String,
    pub(super) inference_server_url: String,
    pub(super) min_update_interval: Duration,
    pub(super) reverse_tunnel: bool,
    pub(super) coordinated_calibration: bool,
    pub(super) quic_insecure: bool,
    pub(super) tunnel_protocol: TunnelTransportProtocol,
    pub(super) cluster_calibration_directive_tx: flume::Sender<ClusterCalibrationDirective>,
    pub(super) forwarding: TunnelForwardingConfig,
    pub(super) cancel_token: CancellationToken,
    pub(super) auth_token_provider: Option<Arc<AuthTokenProvider>>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum RegistrationStreamExit {
    Stop,
    Retry,
}

pub(super) async fn run_router_registration_stream(
    task_config: RouterRegistrationTaskConfig,
    shared_state: SharedInstState,
    parent_stop_rx: watch::Receiver<bool>,
    local_stop_rx: watch::Receiver<bool>,
) {
    let RouterRegistrationTaskConfig {
        router_endpoint,
        inference_server_id,
        cluster_id,
        inference_server_url,
        min_update_interval,
        reverse_tunnel,
        coordinated_calibration,
        quic_insecure,
        tunnel_protocol,
        cluster_calibration_directive_tx,
        forwarding,
        cancel_token,
        auth_token_provider,
    } = task_config;
    let router_addr = router_endpoint.authority_addr().to_string();
    let mut parent_stop_rx = parent_stop_rx;
    let mut local_stop_rx = local_stop_rx;

    loop {
        if registration_should_stop(&parent_stop_rx, &local_stop_rx, &cancel_token) {
            return;
        }

        let connection = tokio::select! {
            _ = cancel_token.cancelled() => return,
            changed = parent_stop_rx.changed() => {
                if changed.is_err()
                    || registration_should_stop(&parent_stop_rx, &local_stop_rx, &cancel_token)
                {
                    return;
                }
                continue;
            }
            changed = local_stop_rx.changed() => {
                if changed.is_err()
                    || registration_should_stop(&parent_stop_rx, &local_stop_rx, &cancel_token)
                {
                    return;
                }
                continue;
            }
            connection = open_registration_stream(
                &router_endpoint,
                auth_token_provider.as_deref(),
                min_update_interval,
            ) => connection,
        };
        let Ok((mut ack_stream, update_tx)) = connection else {
            if sleep_until_registration_stop(
                &mut parent_stop_rx,
                &mut local_stop_rx,
                &cancel_token,
                Duration::from_secs(1),
            )
            .await
            {
                return;
            }
            continue;
        };

        let (endpoint_tx, endpoint_rx) = watch::channel(None);
        let (connected_tx, mut connected_rx) = watch::channel(false);
        let reverse_cancel_token = cancel_token.child_token();
        let reverse_task = if reverse_tunnel {
            Some(tokio::spawn(run_reverse_tunnel_loop(
                ReverseTunnelLoopConfig {
                    router_addr: router_addr.clone(),
                    inference_server_id: inference_server_id.clone(),
                    quic_insecure,
                    tunnel_protocol,
                    forwarding: forwarding.clone(),
                    auth_token_provider: auth_token_provider.clone(),
                },
                endpoint_rx,
                connected_tx,
                parent_stop_rx.clone(),
                local_stop_rx.clone(),
                reverse_cancel_token.clone(),
            )))
        } else {
            None
        };

        shared_state.drain_updates();
        let initial_registration = build_inference_server_registration(
            &inference_server_id,
            &cluster_id,
            &inference_server_url,
            &shared_state.snapshot(),
            reverse_tunnel,
            coordinated_calibration,
            *connected_rx.borrow(),
        );
        let mut advertised_status =
            RouterAdvertisedStatusTracker::new(forwarding.metrics.as_deref(), &router_addr);
        advertised_status.record_reverse_tunnel_connected(false);
        let advertised = advertised_model_statuses(&initial_registration);
        if update_tx.send(initial_registration).await.is_err() {
            if let Some(task) = reverse_task {
                stop_reverse_tunnel_task(reverse_cancel_token, task).await;
            }
            if sleep_until_registration_stop(
                &mut parent_stop_rx,
                &mut local_stop_rx,
                &cancel_token,
                Duration::from_millis(200),
            )
            .await
            {
                return;
            }
            continue;
        }
        advertised_status.record_successful_advertisement(advertised);
        let mut last_send = Instant::now();

        let mut tick_interval = tokio::time::interval(min_update_interval);
        tick_interval.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);

        let stream_exit = loop {
            tokio::select! {
                _ = cancel_token.cancelled() => break RegistrationStreamExit::Stop,
                changed = parent_stop_rx.changed() => {
                    if changed.is_err()
                        || registration_should_stop(&parent_stop_rx, &local_stop_rx, &cancel_token)
                    {
                        break RegistrationStreamExit::Stop;
                    }
                }
                changed = local_stop_rx.changed() => {
                    if changed.is_err()
                        || registration_should_stop(&parent_stop_rx, &local_stop_rx, &cancel_token)
                    {
                        break RegistrationStreamExit::Stop;
                    }
                }
                _ = tick_interval.tick() => {
                    let changed = shared_state.drain_updates();
                    let heartbeat_due = last_send.elapsed() >= min_update_interval;

                    if changed || heartbeat_due {
                        // Heartbeats resend the full current snapshot; identical model
                        // stats across sends are normal liveness traffic.
                        let registration_update = build_inference_server_registration(
                            &inference_server_id,
                            &cluster_id,
                            &inference_server_url,
                            &shared_state.snapshot(),
                            reverse_tunnel,
                            coordinated_calibration,
                            *connected_rx.borrow(),
                        );
                        let advertised = advertised_model_statuses(&registration_update);
                        if update_tx.send(registration_update).await.is_err() {
                            break RegistrationStreamExit::Retry;
                        }
                        advertised_status.record_successful_advertisement(advertised);
                        last_send = Instant::now();
                    }
                }
                connected_changed = connected_rx.changed(), if reverse_tunnel => {
                    if connected_changed.is_ok() {
                        let reverse_connected = *connected_rx.borrow();
                        advertised_status.record_reverse_tunnel_connected(reverse_connected);
                        let registration_update = build_inference_server_registration(
                            &inference_server_id,
                            &cluster_id,
                            &inference_server_url,
                            &shared_state.snapshot(),
                            reverse_tunnel,
                            coordinated_calibration,
                            reverse_connected,
                        );
                        let advertised = advertised_model_statuses(&registration_update);
                        if update_tx.send(registration_update).await.is_err() {
                            break RegistrationStreamExit::Retry;
                        }
                        advertised_status.record_successful_advertisement(advertised);
                        last_send = Instant::now();
                    } else {
                        break RegistrationStreamExit::Retry;
                    }
                }
                maybe_ack = ack_stream.message() => {
                    match maybe_ack {
                        Ok(Some(ack)) => {
                            publish_cluster_calibration_directives(
                                &cluster_calibration_directive_tx,
                                &router_endpoint,
                                ack.model_calibration_directives.clone(),
                            )
                            .await;
                            if reverse_tunnel {
                                let endpoint = reverse_tunnel_endpoint_from_ack(&ack);
                                endpoint_tx.send_if_modified(move |current| {
                                    if *current == endpoint {
                                        return false;
                                    }
                                    *current = endpoint;
                                    true
                                });
                            }
                        }
                        Ok(None) => break RegistrationStreamExit::Retry,
                        Err(_) => break RegistrationStreamExit::Retry,
                    }
                }
            }
        };

        if let Some(task) = reverse_task {
            stop_reverse_tunnel_task(reverse_cancel_token, task).await;
        }
        if stream_exit == RegistrationStreamExit::Stop {
            return;
        }
    }
}

#[derive(Debug)]
pub(super) struct RouterAdvertisedStatusTracker<'a> {
    metrics: Option<&'a PylonMetrics>,
    router_addr: &'a str,
    last_advertised: Vec<AdvertisedModelStatus>,
}

impl<'a> RouterAdvertisedStatusTracker<'a> {
    pub(super) fn new(metrics: Option<&'a PylonMetrics>, router_addr: &'a str) -> Self {
        Self {
            metrics,
            router_addr,
            last_advertised: Vec::new(),
        }
    }

    pub(super) fn record_successful_advertisement(
        &mut self,
        advertised: Vec<AdvertisedModelStatus>,
    ) {
        if let Some(metrics) = self.metrics {
            metrics.observe_registration_stream_connected(self.router_addr, true);
        }
        observe_advertised_statuses(self.metrics, self.router_addr, &advertised);
        self.last_advertised = advertised;
    }

    pub(super) fn record_reverse_tunnel_connected(&self, connected: bool) {
        if let Some(metrics) = self.metrics {
            metrics.observe_reverse_tunnel_connected(self.router_addr, connected);
        }
    }

    fn clear(&mut self) {
        let Some(metrics) = self.metrics else {
            return;
        };

        metrics.observe_registration_stream_connected(self.router_addr, false);
        metrics.observe_reverse_tunnel_connected(self.router_addr, false);
        for advertised in self.last_advertised.drain(..) {
            metrics.observe_model_advertised_status(
                self.router_addr,
                &advertised.model_id,
                InferenceServerStatus::Inactive,
            );
        }
    }
}

impl Drop for RouterAdvertisedStatusTracker<'_> {
    fn drop(&mut self) {
        self.clear();
    }
}

pub(super) fn observe_advertised_statuses(
    metrics: Option<&PylonMetrics>,
    router_addr: &str,
    advertised: &[AdvertisedModelStatus],
) {
    let Some(metrics) = metrics else {
        return;
    };

    for advertised in advertised {
        metrics.observe_model_advertised_status(
            router_addr,
            &advertised.model_id,
            advertised.status,
        );
    }
}

pub(super) async fn open_registration_stream(
    router_endpoint: &StargateGrpcEndpoint,
    auth_token_provider: Option<&AuthTokenProvider>,
    min_update_interval: Duration,
) -> anyhow::Result<(
    tonic::Streaming<InferenceServerAck>,
    mpsc::Sender<InferenceServerRegistration>,
)> {
    let channel =
        connect_stargate_grpc_channel(router_endpoint, "register_inference_server").await?;
    let mut client = StargateControlPlaneClient::new(channel);

    let (update_tx, update_rx) = mpsc::channel(32);
    let stream = ReceiverStream::new(update_rx);

    let mut request = tonic::Request::new(stream);
    let heartbeat_ms: tonic::metadata::MetadataValue<tonic::metadata::Ascii> =
        min_update_interval.as_millis().to_string().parse()?;
    request
        .metadata_mut()
        .insert(REGISTRATION_HEARTBEAT_MS_METADATA, heartbeat_ms);
    if let Some(provider) = auth_token_provider {
        let token = provider.resolve_token().await?;
        let header_value: tonic::metadata::MetadataValue<tonic::metadata::Ascii> =
            format!("Bearer {token}")
                .parse()
                .context("invalid auth token")?;
        request.metadata_mut().insert("authorization", header_value);
    }

    let ack_stream = client
        .register_inference_server(request)
        .await?
        .into_inner();
    Ok((ack_stream, update_tx))
}
