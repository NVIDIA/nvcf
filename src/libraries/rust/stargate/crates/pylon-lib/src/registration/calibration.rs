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
use std::future::Future;
use std::sync::Arc;
use std::time::Duration;

use anyhow::Context;
use tokio::sync::watch;
use tokio::task::{AbortHandle, Id as TaskId, JoinSet};

use stargate_auth::AuthTokenProvider;
use stargate_proto::pb::stargate_control_plane_client::StargateControlPlaneClient;
use stargate_proto::pb::{
    CalibrationState, ModelCalibrationDirective, SubmitClusterCalibrationRequest,
};

use crate::bringup::{self, BringupConfig, BringupError};
use crate::stats::PylonMetrics;

use super::CLUSTER_CALIBRATION_SUBMISSION_TIMEOUT;
use super::grpc_endpoint::{StargateGrpcEndpoint, connect_stargate_grpc_channel};

#[derive(Debug, Clone)]
pub(super) struct ClusterCalibrationExecutorTaskConfig {
    pub(super) inference_server_id: String,
    pub(super) cluster_id: String,
    pub(super) retry_interval: Duration,
    pub(super) upstream_http_base_url: String,
    pub(super) bringup: BringupConfig,
    pub(super) metrics: Option<Arc<PylonMetrics>>,
    pub(super) auth_token_provider: Option<Arc<AuthTokenProvider>>,
    pub(super) cancel_token: tokio_util::sync::CancellationToken,
}

#[derive(Debug, Clone)]
pub(super) struct ClusterCalibrationDirective {
    pub(super) router_endpoint: StargateGrpcEndpoint,
    pub(super) model_id: String,
    pub(super) state: ClusterCalibrationDirectiveState,
    pub(super) assignment_token: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(super) enum ClusterCalibrationDirectiveState {
    Waiting,
    Run,
    Complete,
}

#[derive(Debug, Clone)]
pub(super) struct PendingRouterCalibration {
    pub(super) assignment_token: String,
    pub(super) measured_last_mean_input_tps: f64,
}

type RouterCalibrationKey = (StargateGrpcEndpoint, String);

#[derive(Debug)]
pub(super) struct OwnedCalibrationTask {
    id: TaskId,
    abort_handle: AbortHandle,
}

impl OwnedCalibrationTask {
    pub(super) fn new(abort_handle: AbortHandle) -> Self {
        Self {
            id: abort_handle.id(),
            abort_handle,
        }
    }

    fn abort(self) {
        self.abort_handle.abort();
    }
}

#[derive(Debug)]
pub(super) enum RouterCalibrationWork {
    Sweeping {
        assignment_token: String,
        task: OwnedCalibrationTask,
    },
    PendingSubmission {
        result: PendingRouterCalibration,
        task: Option<OwnedCalibrationTask>,
    },
    Submitted {
        assignment_token: String,
    },
}

impl RouterCalibrationWork {
    pub(super) fn assignment_token(&self) -> &str {
        match self {
            Self::Sweeping {
                assignment_token, ..
            } => assignment_token,
            Self::PendingSubmission { result, .. } => &result.assignment_token,
            Self::Submitted { assignment_token } => assignment_token,
        }
    }

    fn owns_task(&self, id: TaskId) -> bool {
        match self {
            Self::Sweeping { task, .. } => task.id == id,
            Self::PendingSubmission {
                task: Some(task), ..
            } => task.id == id,
            Self::PendingSubmission { task: None, .. } => false,
            Self::Submitted { .. } => false,
        }
    }

    fn abort(self) {
        match self {
            Self::Sweeping { task, .. } => task.abort(),
            Self::PendingSubmission {
                task: Some(task), ..
            } => task.abort(),
            Self::PendingSubmission { task: None, .. } => {}
            Self::Submitted { .. } => {}
        }
    }
}

#[derive(Debug)]
pub(super) struct CompletedCalibrationSweep {
    key: RouterCalibrationKey,
    assignment_token: String,
    result: Result<f64, BringupError>,
}

#[derive(Debug)]
pub(super) struct CompletedCalibrationSubmission {
    pub(super) key: RouterCalibrationKey,
    pub(super) assignment_token: String,
    pub(super) result: anyhow::Result<()>,
}

pub(super) async fn run_cluster_calibration_executor(
    task_config: ClusterCalibrationExecutorTaskConfig,
    directive_rx: flume::Receiver<ClusterCalibrationDirective>,
    mut active_calibration_routers: watch::Receiver<BTreeSet<StargateGrpcEndpoint>>,
) {
    let ClusterCalibrationExecutorTaskConfig {
        inference_server_id,
        cluster_id,
        retry_interval,
        upstream_http_base_url,
        bringup,
        metrics,
        auth_token_provider,
        cancel_token,
    } = task_config;
    let mut work = HashMap::<RouterCalibrationKey, RouterCalibrationWork>::new();
    let mut sweep_tasks = JoinSet::<CompletedCalibrationSweep>::new();
    let mut submission_tasks = JoinSet::<CompletedCalibrationSubmission>::new();
    // Tokio intervals reject zero; zero update cadence still means retry promptly.
    let retry_interval = retry_interval.max(Duration::from_millis(1));
    let mut retry_tick = tokio::time::interval(retry_interval);
    retry_tick.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);

    loop {
        tokio::select! {
            _ = cancel_token.cancelled() => {
                sweep_tasks.abort_all();
                submission_tasks.abort_all();
                return;
            }
            changed = active_calibration_routers.changed() => {
                if changed.is_err() {
                    sweep_tasks.abort_all();
                    submission_tasks.abort_all();
                    return;
                }
                let active_routers = active_calibration_routers.borrow().clone();
                cancel_removed_router_calibration_work(&mut work, &active_routers);
            }
            directive = directive_rx.recv_async() => {
                let Ok(directive) = directive else {
                    sweep_tasks.abort_all();
                    submission_tasks.abort_all();
                    return;
                };
                let key = (
                    directive.router_endpoint.clone(),
                    directive.model_id.clone(),
                );
                if !active_calibration_routers
                    .borrow()
                    .contains(&directive.router_endpoint)
                {
                    cancel_router_calibration_work(&mut work, &key);
                    continue;
                }
                match directive.state {
                    ClusterCalibrationDirectiveState::Run if !directive.assignment_token.is_empty() => {
                        let assignment_is_running_or_pending = work
                            .get(&key)
                            .is_some_and(|state| state.assignment_token() == directive.assignment_token);
                        if !assignment_is_running_or_pending {
                            cancel_router_calibration_work(&mut work, &key);
                            let state = start_assigned_calibration_sweep(
                                &mut sweep_tasks,
                                key.clone(),
                                directive.assignment_token,
                                &upstream_http_base_url,
                                &bringup,
                                metrics.as_ref(),
                            );
                            work.insert(key, state);
                        }
                    }
                    ClusterCalibrationDirectiveState::Waiting
                    | ClusterCalibrationDirectiveState::Complete => {
                        cancel_router_calibration_work(&mut work, &key);
                    }
                    ClusterCalibrationDirectiveState::Run => {}
                }
            }
            completed = sweep_tasks.join_next(), if !sweep_tasks.is_empty() => {
                match completed {
                    Some(Ok(completed)) => {
                        let active_assignment_matches = work
                            .get(&completed.key)
                            .is_some_and(|state| state.assignment_token() == completed.assignment_token);
                        if !active_assignment_matches {
                            continue;
                        }
                        work.remove(&completed.key);
                        match completed.result {
                            Ok(measured_last_mean_input_tps) => {
                                let key = completed.key;
                                work.insert(
                                    key.clone(),
                                    RouterCalibrationWork::PendingSubmission {
                                        result: PendingRouterCalibration {
                                            assignment_token: completed.assignment_token,
                                            measured_last_mean_input_tps,
                                        },
                                        task: None,
                                    },
                                );
                                start_pending_cluster_calibration_submission(
                                    &mut work,
                                    &mut submission_tasks,
                                    &key,
                                    &inference_server_id,
                                    &cluster_id,
                                    auth_token_provider.as_ref(),
                                );
                            }
                            Err(error) => {
                                tracing::warn!(
                                    router_addr = %completed.key.0.authority_addr(),
                                    model_id = %completed.key.1,
                                    error = %error,
                                    "assigned cluster calibration failed; waiting for retry directive"
                                );
                            }
                        };
                    }
                    Some(Err(error)) if !error.is_cancelled() => {
                        remove_panicked_calibration_work(&mut work, error.id());
                        tracing::warn!(error = %error, "assigned cluster calibration task failed unexpectedly");
                    }
                    Some(Err(_)) | None => {}
                }
            }
            completed = submission_tasks.join_next(), if !submission_tasks.is_empty() => {
                match completed {
                    Some(Ok(completed)) => {
                        finish_pending_cluster_calibration_submission(&mut work, completed);
                    }
                    Some(Err(error)) if !error.is_cancelled() => {
                        remove_panicked_calibration_work(&mut work, error.id());
                        tracing::warn!(error = %error, "cluster calibration submission task failed unexpectedly");
                    }
                    Some(Err(_)) | None => {}
                }
            }
            _ = retry_tick.tick() => {
                let retry_keys = work
                    .iter()
                    .filter_map(|(key, state)| match state {
                        RouterCalibrationWork::PendingSubmission { task: None, .. } => Some(key.clone()),
                        _ => None,
                    })
                    .collect::<Vec<_>>();
                for key in retry_keys {
                    start_pending_cluster_calibration_submission(
                        &mut work,
                        &mut submission_tasks,
                        &key,
                        &inference_server_id,
                        &cluster_id,
                        auth_token_provider.as_ref(),
                    );
                }
            }
        }
    }
}

fn start_assigned_calibration_sweep(
    tasks: &mut JoinSet<CompletedCalibrationSweep>,
    key: RouterCalibrationKey,
    assignment_token: String,
    upstream_http_base_url: &str,
    bringup: &BringupConfig,
    metrics: Option<&Arc<PylonMetrics>>,
) -> RouterCalibrationWork {
    let task_config = bringup::BringupTaskConfig {
        upstream_http_base_url: upstream_http_base_url.to_string(),
        model_id: key.1.clone(),
        config: bringup.clone(),
        metrics: metrics.cloned(),
    };
    let completed_key = key;
    let completed_token = assignment_token.clone();
    let abort_handle = tasks.spawn(async move {
        CompletedCalibrationSweep {
            key: completed_key,
            assignment_token: completed_token,
            result: bringup::run_assigned_cluster_calibration(&task_config).await,
        }
    });
    RouterCalibrationWork::Sweeping {
        assignment_token,
        task: OwnedCalibrationTask::new(abort_handle),
    }
}

fn start_pending_cluster_calibration_submission(
    work: &mut HashMap<RouterCalibrationKey, RouterCalibrationWork>,
    tasks: &mut JoinSet<CompletedCalibrationSubmission>,
    key: &RouterCalibrationKey,
    inference_server_id: &str,
    cluster_id: &str,
    auth_token_provider: Option<&Arc<AuthTokenProvider>>,
) {
    let Some(RouterCalibrationWork::PendingSubmission { result, task: None }) = work.get(key)
    else {
        return;
    };
    let result = result.clone();
    let completed_key = key.clone();
    let router_endpoint = key.0.clone();
    let model_id = key.1.clone();
    let inference_server_id = inference_server_id.to_string();
    let cluster_id = cluster_id.to_string();
    let auth_token_provider = auth_token_provider.cloned();
    let completed_token = result.assignment_token.clone();
    let abort_handle = tasks.spawn(async move {
        CompletedCalibrationSubmission {
            key: completed_key,
            assignment_token: completed_token,
            result: submit_pending_cluster_calibration(
                &router_endpoint,
                &inference_server_id,
                &cluster_id,
                &model_id,
                &result,
                auth_token_provider.as_deref(),
            )
            .await,
        }
    });
    let Some(RouterCalibrationWork::PendingSubmission { task, .. }) = work.get_mut(key) else {
        abort_handle.abort();
        return;
    };
    *task = Some(OwnedCalibrationTask::new(abort_handle));
}

pub(super) fn finish_pending_cluster_calibration_submission(
    work: &mut HashMap<RouterCalibrationKey, RouterCalibrationWork>,
    completed: CompletedCalibrationSubmission,
) {
    let Some(RouterCalibrationWork::PendingSubmission { result, task }) =
        work.get_mut(&completed.key)
    else {
        return;
    };
    if result.assignment_token != completed.assignment_token {
        return;
    }
    *task = None;
    match completed.result {
        Ok(()) => {
            work.insert(
                completed.key,
                RouterCalibrationWork::Submitted {
                    assignment_token: completed.assignment_token,
                },
            );
        }
        Err(error) => {
            tracing::warn!(
                router_addr = %completed.key.0.authority_addr(),
                model_id = %completed.key.1,
                error = %error,
                "failed to submit assigned cluster calibration; retrying while pending"
            );
        }
    }
}

fn cancel_router_calibration_work(
    work: &mut HashMap<RouterCalibrationKey, RouterCalibrationWork>,
    key: &RouterCalibrationKey,
) {
    if let Some(state) = work.remove(key) {
        state.abort();
    }
}

fn cancel_removed_router_calibration_work(
    work: &mut HashMap<RouterCalibrationKey, RouterCalibrationWork>,
    active_routers: &BTreeSet<StargateGrpcEndpoint>,
) {
    let removed_keys = work
        .keys()
        .filter(|(router_endpoint, _)| !active_routers.contains(router_endpoint))
        .cloned()
        .collect::<Vec<_>>();
    for key in removed_keys {
        cancel_router_calibration_work(work, &key);
    }
}

fn remove_panicked_calibration_work(
    work: &mut HashMap<RouterCalibrationKey, RouterCalibrationWork>,
    task_id: TaskId,
) {
    let failed_key = work
        .iter()
        .find_map(|(key, state)| state.owns_task(task_id).then(|| key.clone()));
    if let Some(failed_key) = failed_key {
        work.remove(&failed_key);
    }
}

async fn submit_pending_cluster_calibration(
    router_endpoint: &StargateGrpcEndpoint,
    inference_server_id: &str,
    cluster_id: &str,
    model_id: &str,
    result: &PendingRouterCalibration,
    auth_token_provider: Option<&AuthTokenProvider>,
) -> anyhow::Result<()> {
    let submission = SubmitClusterCalibrationRequest {
        inference_server_id: inference_server_id.to_string(),
        cluster_id: cluster_id.to_string(),
        model_id: model_id.to_string(),
        assignment_token: result.assignment_token.clone(),
        measured_last_mean_input_tps: result.measured_last_mean_input_tps,
    };
    await_cluster_calibration_submission(
        CLUSTER_CALIBRATION_SUBMISSION_TIMEOUT,
        submit_cluster_calibration_result(router_endpoint, auth_token_provider, submission),
    )
    .await
}

async fn submit_cluster_calibration_result(
    router_endpoint: &StargateGrpcEndpoint,
    auth_token_provider: Option<&AuthTokenProvider>,
    submission: SubmitClusterCalibrationRequest,
) -> anyhow::Result<()> {
    let channel =
        connect_stargate_grpc_channel(router_endpoint, "submit_cluster_calibration").await?;
    let mut client = StargateControlPlaneClient::new(channel);
    let mut request = tonic::Request::new(submission);
    if let Some(provider) = auth_token_provider {
        let token = provider.resolve_token().await?;
        let header_value: tonic::metadata::MetadataValue<tonic::metadata::Ascii> =
            format!("Bearer {token}")
                .parse()
                .context("invalid auth token")?;
        request.metadata_mut().insert("authorization", header_value);
    }
    let response = client
        .submit_cluster_calibration(request)
        .await?
        .into_inner();
    anyhow::ensure!(
        response.state == CalibrationState::Complete as i32,
        "stargate did not acknowledge completed cluster calibration"
    );
    Ok(())
}

pub(super) async fn await_cluster_calibration_submission<F>(
    timeout: Duration,
    attempt: F,
) -> anyhow::Result<()>
where
    F: Future<Output = anyhow::Result<()>>,
{
    tokio::time::timeout(timeout, attempt).await.map_err(|_| {
        anyhow::anyhow!(
            "cluster calibration submission timed out after {}ms",
            timeout.as_millis()
        )
    })?
}

pub(super) async fn publish_cluster_calibration_directives(
    tx: &flume::Sender<ClusterCalibrationDirective>,
    router_endpoint: &StargateGrpcEndpoint,
    directives: Vec<ModelCalibrationDirective>,
) {
    for directive in directives {
        let Some(state) = cluster_calibration_directive_state(directive.state) else {
            continue;
        };
        let _ = tx
            .send_async(ClusterCalibrationDirective {
                router_endpoint: router_endpoint.clone(),
                model_id: directive.model_id,
                state,
                assignment_token: directive.assignment_token,
            })
            .await;
    }
}

fn cluster_calibration_directive_state(state: i32) -> Option<ClusterCalibrationDirectiveState> {
    match CalibrationState::try_from(state).unwrap_or(CalibrationState::Unknown) {
        CalibrationState::Waiting => Some(ClusterCalibrationDirectiveState::Waiting),
        CalibrationState::Run => Some(ClusterCalibrationDirectiveState::Run),
        CalibrationState::Complete => Some(ClusterCalibrationDirectiveState::Complete),
        CalibrationState::Unknown => None,
    }
}
