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

use std::collections::{BTreeSet, HashMap};
use std::time::Duration;

use tokio::sync::{mpsc, watch};
use tokio::task::JoinHandle;
use tokio_util::sync::CancellationToken;

use stargate_proto::pb::InferenceServerStatus;

use crate::bringup::{self, BringupModelUpdate, start_bringup_supervisor};

use super::calibration::{
    ClusterCalibrationDirective, ClusterCalibrationExecutorTaskConfig,
    run_cluster_calibration_executor,
};
use super::discovery::run_watch_stargate_discovery;
use super::grpc_endpoint::StargateGrpcEndpoint;
use super::router_stream::{
    RouterRegistrationTaskTemplate, RouterRegistrationWorker, desired_registration_routers,
    run_router_registration_stream, stop_router_registration_worker,
};
use super::state::{CurrentModelStats, SharedInstState, SharedInstStateChannels};
use super::task_lifecycle::{
    NamedJoinHandle, REGISTRATION_TASK_SHUTDOWN_TIMEOUT, abort_named_join_handle,
    await_named_join_handles, stop_channel_changed,
};
use super::types::{
    ClientError, InferenceServerRegistrationConfig, InferenceServerUpdateChannels,
    RegistrationStartPlan,
};

#[derive(Default)]
pub struct InferenceServerRegistrationClient {
    watch_task: Option<JoinHandle<()>>,
    bringup_task: Option<JoinHandle<()>>,
    calibration_task: Option<JoinHandle<()>>,
    register_task: Option<JoinHandle<()>>,
    stop_tx: Option<watch::Sender<bool>>,
    cancel_token: CancellationToken,
}

impl InferenceServerRegistrationClient {
    pub fn stop(&mut self) {
        self.request_stop();
        self.abort_owned_tasks();
    }

    pub async fn shutdown(&mut self) {
        self.request_stop();
        await_named_join_handles(self.take_owned_tasks(), REGISTRATION_TASK_SHUTDOWN_TIMEOUT).await;
    }

    fn request_stop(&mut self) {
        self.cancel_token.cancel();
        if let Some(tx) = self.stop_tx.take() {
            let _ = tx.send(true);
        }
    }

    fn abort_owned_tasks(&mut self) {
        for task in self.take_owned_tasks() {
            // The synchronous stop API cannot wait for cooperative shutdown, so
            // abort after sending stop signals to avoid detached background work.
            abort_named_join_handle(task);
        }
    }

    fn take_owned_tasks(&mut self) -> Vec<NamedJoinHandle> {
        let mut tasks = Vec::new();
        if let Some(task) = self.watch_task.take() {
            tasks.push(NamedJoinHandle::new("watch stargate discovery", task));
        }
        if let Some(task) = self.bringup_task.take() {
            tasks.push(NamedJoinHandle::new("bringup supervisor", task));
        }
        if let Some(task) = self.calibration_task.take() {
            tasks.push(NamedJoinHandle::new("cluster calibration executor", task));
        }
        if let Some(task) = self.register_task.take() {
            tasks.push(NamedJoinHandle::new("registration supervisor", task));
        }
        tasks
    }

    pub fn start(
        &mut self,
        config: InferenceServerRegistrationConfig,
        model_ids: Vec<String>,
    ) -> Result<InferenceServerUpdateChannels, ClientError> {
        self.stop();
        let start_plan = RegistrationStartPlan::from_config(&config)?;

        let (stop_tx, stop_rx) = watch::channel(false);
        self.stop_tx = Some(stop_tx);
        let cancel_token = CancellationToken::new();
        self.cancel_token = cancel_token.clone();

        let (status_tx, status_rx) = flume::bounded::<InferenceServerStatus>(64);
        let (stats_tx, stats_rx) = flume::bounded::<(String, CurrentModelStats)>(256);
        let (bringup_state_tx, bringup_state_rx) = flume::bounded::<BringupModelUpdate>(256);
        let (cluster_calibration_directive_tx, cluster_calibration_directive_rx) =
            flume::bounded::<ClusterCalibrationDirective>(256);
        let (calibration_router_tx, calibration_router_rx) =
            watch::channel(BTreeSet::<StargateGrpcEndpoint>::new());

        let (stargate_updates_tx, mut stargate_updates_rx) =
            mpsc::channel::<BTreeSet<StargateGrpcEndpoint>>(8);
        let watch_seeds = start_plan.watch_seeds.clone();
        let watch_stop_rx = stop_rx.clone();
        self.watch_task = Some(tokio::spawn(run_watch_stargate_discovery(
            watch_seeds,
            stargate_updates_tx,
            watch_stop_rx,
        )));

        let register_config = config.clone();
        let task_template = RouterRegistrationTaskTemplate::from_registration_config(
            &register_config,
            &start_plan.cluster_id,
            &start_plan.upstream_http_base_url,
            cluster_calibration_directive_tx,
            &cancel_token,
        );
        let coordinated_calibration = task_template.coordinated_calibration;

        self.bringup_task = Some(start_bringup_supervisor(
            model_ids
                .iter()
                .cloned()
                .map(|model_id| bringup::BringupTaskConfig {
                    upstream_http_base_url: start_plan.upstream_http_base_url.clone(),
                    model_id,
                    config: register_config.bringup.clone(),
                    metrics: register_config.metrics.clone(),
                })
                .collect(),
            bringup_state_tx,
            stop_rx.clone(),
        ));
        self.calibration_task = coordinated_calibration.then(|| {
            tokio::spawn(run_cluster_calibration_executor(
                ClusterCalibrationExecutorTaskConfig {
                    inference_server_id: register_config.inference_server_id.clone(),
                    cluster_id: start_plan.cluster_id.clone(),
                    retry_interval: register_config.min_update_interval,
                    upstream_http_base_url: start_plan.upstream_http_base_url.clone(),
                    bringup: register_config.bringup.clone(),
                    metrics: register_config.metrics.clone(),
                    auth_token_provider: register_config.auth_token_provider.clone(),
                    cancel_token: cancel_token.clone(),
                },
                cluster_calibration_directive_rx,
                calibration_router_rx,
            ))
        });

        let mut register_stop_rx = stop_rx.clone();
        self.register_task = Some(tokio::spawn(async move {
            let mut active_routers = BTreeSet::<StargateGrpcEndpoint>::new();
            let mut per_router_tasks: HashMap<StargateGrpcEndpoint, RouterRegistrationWorker> =
                HashMap::new();

            let shared_state = SharedInstState::new(
                register_config.status,
                &model_ids,
                SharedInstStateChannels {
                    status_rx,
                    stats_rx,
                    bringup_state_rx,
                },
                register_config.bringup.enabled,
            );

            loop {
                if *register_stop_rx.borrow() {
                    break;
                }

                while let Ok(new_set) = stargate_updates_rx.try_recv() {
                    active_routers = new_set;
                }
                shared_state.drain_updates();

                let desired_routers = desired_registration_routers(&active_routers);
                let calibration_routers_changed = {
                    let current = calibration_router_tx.borrow();
                    *current != desired_routers
                };
                if calibration_routers_changed {
                    calibration_router_tx.send_replace(desired_routers.clone());
                }

                let current_routers: Vec<StargateGrpcEndpoint> =
                    per_router_tasks.keys().cloned().collect();
                for router in current_routers {
                    if desired_routers.contains(&router) {
                        continue;
                    }
                    if let Some(worker) = per_router_tasks.remove(&router) {
                        stop_router_registration_worker(worker).await;
                    }
                }

                for router in &desired_routers {
                    if per_router_tasks.contains_key(router) {
                        continue;
                    }

                    let (worker_stop_tx, worker_stop_rx) = watch::channel(false);
                    let task_config = task_template.build_for_router(router.clone());
                    let task = tokio::spawn(run_router_registration_stream(
                        task_config,
                        shared_state.clone(),
                        register_stop_rx.clone(),
                        worker_stop_rx,
                    ));
                    per_router_tasks.insert(
                        router.clone(),
                        RouterRegistrationWorker {
                            stop_tx: worker_stop_tx,
                            task,
                        },
                    );
                }

                tokio::select! {
                    changed = register_stop_rx.changed() => {
                        if stop_channel_changed(changed, &register_stop_rx) {
                            break;
                        }
                    }
                    _ = tokio::time::sleep(Duration::from_millis(100)) => {}
                }
            }

            for (_, worker) in per_router_tasks {
                stop_router_registration_worker(worker).await;
            }
        }));

        Ok(InferenceServerUpdateChannels {
            status: status_tx,
            model_stats: stats_tx,
        })
    }
}
