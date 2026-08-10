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
use std::time::{Duration, Instant};

use anyhow::Context;
use tokio::sync::{mpsc, watch};
use tokio_stream::wrappers::ReceiverStream;
use tokio_util::sync::CancellationToken;

use crate::stats::PylonMetrics;
use stargate_auth::AuthTokenProvider;
use stargate_proto::REGISTRATION_HEARTBEAT_MS_METADATA;
use stargate_proto::pb::stargate_control_plane_client::StargateControlPlaneClient;
use stargate_proto::pb::{InferenceServerAck, InferenceServerRegistration, InferenceServerStatus};
use stargate_runtime::{OwnedTask, TASK_SHUTDOWN_TIMEOUT};

use super::grpc_endpoint::{StargateGrpcEndpoint, connect_stargate_grpc_channel};
use super::reverse_tunnel::{
    ReverseTunnelState, reverse_tunnel_endpoint_from_ack, run_reverse_tunnel_loop,
};
use super::state::{
    AdvertisedModelStatus, advertised_model_statuses, build_inference_server_registration,
};
use super::types::RegistrationSessionConfig;

pub(super) async fn run_router_registration_stream(
    router_endpoint: StargateGrpcEndpoint,
    config: Arc<RegistrationSessionConfig>,
    stop: CancellationToken,
) {
    let router_addr = router_endpoint.authority_addr().to_string();

    loop {
        let connection = tokio::select! {
            _ = stop.cancelled() => return,
            connection = open_registration_stream(
                &router_endpoint,
                config.auth_token_provider.as_deref(),
                config.min_update_interval,
            ) => connection,
        };
        let Ok((mut ack_stream, update_tx)) = connection else {
            if stop
                .run_until_cancelled(tokio::time::sleep(Duration::from_secs(1)))
                .await
                .is_none()
            {
                return;
            }
            continue;
        };

        let (reverse_state_tx, mut reverse_state_rx) =
            watch::channel(ReverseTunnelState::default());
        let reverse_task = if config.reverse_tunnel {
            let reverse_task_state_tx = reverse_state_tx.clone();
            let reverse_router_addr = router_addr.clone();
            let reverse_config = config.clone();
            Some(OwnedTask::spawn_child(
                "reverse tunnel registration worker",
                &stop,
                move |reverse_stop| {
                    run_reverse_tunnel_loop(
                        reverse_router_addr,
                        reverse_config,
                        reverse_task_state_tx,
                        reverse_stop,
                    )
                },
            ))
        } else {
            None
        };

        let current_registration = |reverse_connected| {
            build_inference_server_registration(
                &config.inference_server_id,
                &config.cluster_id,
                &config.inference_server_url,
                &config.forwarding.runtime_state.advertised_models(),
                config.reverse_tunnel,
                reverse_connected,
            )
        };
        let initial_registration = current_registration(reverse_state_rx.borrow().is_connected());
        let mut advertised_status =
            RouterAdvertisedStatusTracker::new(config.forwarding.metrics.as_deref(), &router_addr);
        advertised_status.record_reverse_tunnel_connected(false);
        let mut advertised_reverse_connected = false;
        let advertised = advertised_model_statuses(&initial_registration);
        if !send_registration_update(&update_tx, initial_registration, &stop).await {
            if let Some(task) = reverse_task {
                task.shutdown(TASK_SHUTDOWN_TIMEOUT).await;
            }
            if stop
                .run_until_cancelled(tokio::time::sleep(Duration::from_millis(200)))
                .await
                .is_none()
            {
                return;
            }
            continue;
        }
        advertised_status.record_successful_advertisement(advertised);
        let mut last_send = Instant::now();

        let mut tick_interval = tokio::time::interval(config.min_update_interval);
        tick_interval.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);

        let stopped = loop {
            let reverse_connected = tokio::select! {
                _ = stop.cancelled() => break true,
                _ = tick_interval.tick() => {
                    if last_send.elapsed() < config.min_update_interval {
                        continue;
                    }
                    // Heartbeats resend the full current snapshot; identical model
                    // stats across sends are normal liveness traffic.
                    reverse_state_rx.borrow().is_connected()
                }
                state_changed = reverse_state_rx.changed(), if config.reverse_tunnel => {
                    if state_changed.is_err() {
                        break false;
                    }
                    let connected = reverse_state_rx.borrow_and_update().is_connected();
                    advertised_status.record_reverse_tunnel_connected(connected);
                    if connected == advertised_reverse_connected {
                        continue;
                    }
                    connected
                }
                maybe_ack = ack_stream.message() => {
                    let Ok(Some(ack)) = maybe_ack else {
                        break false;
                    };
                    if config.reverse_tunnel {
                        let endpoint = reverse_tunnel_endpoint_from_ack(&ack);
                        reverse_state_tx.send_if_modified(move |state| {
                            state.replace_endpoint(endpoint)
                        });
                    }
                    continue;
                }
            };
            let registration_update = current_registration(reverse_connected);
            let advertised = advertised_model_statuses(&registration_update);
            if !send_registration_update(&update_tx, registration_update, &stop).await {
                break false;
            }
            advertised_status.record_successful_advertisement(advertised);
            advertised_reverse_connected = reverse_connected;
            last_send = Instant::now();
        };

        if let Some(task) = reverse_task {
            task.shutdown(TASK_SHUTDOWN_TIMEOUT).await;
        }
        if stopped {
            return;
        }
    }
}

pub(super) async fn send_registration_update(
    update_tx: &mpsc::Sender<InferenceServerRegistration>,
    update: InferenceServerRegistration,
    stop: &CancellationToken,
) -> bool {
    stop.run_until_cancelled(update_tx.send(update))
        .await
        .is_some_and(|result| result.is_ok())
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
            for previous in &self.last_advertised {
                if !advertised
                    .iter()
                    .any(|current| current.model_id == previous.model_id)
                {
                    metrics.remove_model_advertised_status(self.router_addr, &previous.model_id);
                }
            }
        }
        observe_advertised_statuses(self.metrics, self.router_addr, &advertised);
        self.last_advertised = advertised;
    }

    pub(super) fn record_reverse_tunnel_connected(&self, connected: bool) {
        if let Some(metrics) = self.metrics {
            metrics.observe_reverse_tunnel_connected(self.router_addr, connected);
        }
    }
}

impl Drop for RouterAdvertisedStatusTracker<'_> {
    fn drop(&mut self) {
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

pub(super) fn observe_advertised_statuses(
    metrics: Option<&PylonMetrics>,
    router_addr: &str,
    advertised: &[AdvertisedModelStatus],
) {
    if let Some(metrics) = metrics {
        for advertised in advertised {
            metrics.observe_model_advertised_status(
                router_addr,
                &advertised.model_id,
                advertised.status,
            );
        }
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
    let (update_tx, update_rx) = mpsc::channel(32);
    let mut request = tonic::Request::new(ReceiverStream::new(update_rx));
    request.metadata_mut().insert(
        REGISTRATION_HEARTBEAT_MS_METADATA,
        min_update_interval.as_millis().to_string().parse()?,
    );
    if let Some(provider) = auth_token_provider {
        let token = provider.resolve_token().await?;
        request.metadata_mut().insert(
            "authorization",
            format!("Bearer {token}")
                .parse()
                .context("invalid auth token")?,
        );
    }

    let ack_stream = StargateControlPlaneClient::new(channel)
        .register_inference_server(request)
        .await?
        .into_inner();
    Ok((ack_stream, update_tx))
}
