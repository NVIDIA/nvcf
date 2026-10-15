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

use std::collections::HashMap;
use std::sync::Arc;

use tokio::sync::watch;
use tokio_util::sync::CancellationToken;

use stargate_runtime::{OwnedTask, TASK_SHUTDOWN_TIMEOUT};

use super::discovery::run_watch_stargate_discovery;
use super::grpc_endpoint::StargateGrpcEndpoint;
use super::router_stream::run_router_registration_stream;
use super::topology::RegistrationRouterTopology;
use super::types::{ClientError, InferenceServerRegistrationConfig, RegistrationSessionConfig};

async fn run_registration_session(config: RegistrationSessionConfig, stop: CancellationToken) {
    let config = Arc::new(config);
    let (router_topology_tx, router_topology_rx) =
        watch::channel(RegistrationRouterTopology::default());
    let watch_seeds = config.watch_seeds.clone();
    let watch_task = OwnedTask::spawn_child("watch stargate discovery", &stop, move |watch_stop| {
        run_watch_stargate_discovery(watch_seeds, router_topology_tx, watch_stop)
    });

    let registration_task =
        OwnedTask::spawn_child("registration supervisor", &stop, move |registration_stop| {
            run_registration_supervisor(config, router_topology_rx, registration_stop)
        });

    let tasks = vec![watch_task, registration_task];

    stop.cancelled().await;
    OwnedTask::shutdown_all(tasks, TASK_SHUTDOWN_TIMEOUT).await;
}

async fn run_registration_supervisor(
    config: Arc<RegistrationSessionConfig>,
    mut router_topology_rx: watch::Receiver<RegistrationRouterTopology>,
    stop: CancellationToken,
) {
    let mut per_router_tasks: HashMap<StargateGrpcEndpoint, OwnedTask> = HashMap::new();

    loop {
        if stop.is_cancelled() {
            break;
        }

        let desired_routers = router_topology_rx
            .borrow_and_update()
            .published_routers()
            .cloned()
            .unwrap_or_default();
        let retired_tasks = per_router_tasks
            .extract_if(|router, _| !desired_routers.contains(router))
            .map(|(_, task)| task)
            .collect();
        OwnedTask::shutdown_all(retired_tasks, TASK_SHUTDOWN_TIMEOUT).await;

        for router in &desired_routers {
            if !per_router_tasks.contains_key(router) {
                let router_endpoint = router.clone();
                let config = config.clone();
                let task = OwnedTask::spawn_child(
                    "router registration stream",
                    &stop,
                    move |worker_stop| {
                        run_router_registration_stream(router_endpoint, config, worker_stop)
                    },
                );
                per_router_tasks.insert(router.clone(), task);
            }
        }

        let finished = tokio::select! {
            _ = stop.cancelled() => true,
            changed = router_topology_rx.changed() => changed.is_err(),
        };
        if finished {
            break;
        }
    }

    OwnedTask::shutdown_all(
        per_router_tasks.into_values().collect(),
        TASK_SHUTDOWN_TIMEOUT,
    )
    .await;
}

#[derive(Default)]
pub struct InferenceServerRegistrationClient {
    running: Option<OwnedTask>,
}

impl InferenceServerRegistrationClient {
    pub fn stop(&mut self) {
        self.running = None;
    }

    pub async fn shutdown(&mut self) {
        if let Some(running) = self.running.take() {
            running.shutdown(TASK_SHUTDOWN_TIMEOUT).await;
        }
    }

    pub async fn wait_for_exit(&mut self) -> std::result::Result<(), tokio::task::JoinError> {
        let Some(running) = self.running.as_mut() else {
            return std::future::pending().await;
        };
        let result = running.wait_for_exit().await;
        self.running = None;
        result
    }

    pub fn start(&mut self, config: InferenceServerRegistrationConfig) -> Result<(), ClientError> {
        self.stop();
        let config = config.try_into()?;
        self.running = Some(OwnedTask::spawn("registration session", move |stop| {
            run_registration_session(config, stop)
        }));
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use std::future::pending;
    use std::time::Duration;

    use tokio::sync::oneshot;

    use super::*;

    struct DropNotifier(Option<oneshot::Sender<()>>);

    impl Drop for DropNotifier {
        fn drop(&mut self) {
            if let Some(tx) = self.0.take() {
                let _ = tx.send(());
            }
        }
    }

    async fn spawn_pending_owned_task() -> (OwnedTask, oneshot::Receiver<()>) {
        let (entered_tx, entered_rx) = oneshot::channel();
        let (dropped_tx, dropped_rx) = oneshot::channel();
        let task = OwnedTask::spawn("pending registration session task", |_| async move {
            let _drop_notifier = DropNotifier(Some(dropped_tx));
            let _ = entered_tx.send(());
            pending::<()>().await;
        });
        entered_rx.await.expect("owned task should start");
        (task, dropped_rx)
    }

    #[tokio::test]
    async fn registration_client_drop_aborts_running_session_root() {
        let (running, dropped) = spawn_pending_owned_task().await;
        let client = InferenceServerRegistrationClient {
            running: Some(running),
        };

        // Dropping the client must tear down its one physical session root.
        drop(client);

        tokio::time::timeout(Duration::from_secs(1), dropped)
            .await
            .expect("session root should be aborted when the client drops")
            .expect("session root drop notifier should send");
    }
}
