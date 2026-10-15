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

use stargate_runtime::OwnedTask;
use tokio::sync::watch;
use tracing::warn;

use crate::routing_state::RegistrationGeneration;
use crate::tunnel::QuicHttpProxy;

const HEALTH_CHECK_INTERVAL: Duration = Duration::from_secs(1);
const HEALTH_CHECK_SHUTDOWN_TIMEOUT: Duration = Duration::from_secs(2);

pub(super) struct HealthCheckHandle {
    quic_proxy: Arc<QuicHttpProxy>,
    registration: Arc<RegistrationGeneration>,
    task: OwnedTask,
    rx: watch::Receiver<HealthCheckStatus>,
}

impl HealthCheckHandle {
    pub(super) fn start(
        quic_proxy: Arc<QuicHttpProxy>,
        registration: Arc<RegistrationGeneration>,
    ) -> Self {
        let task_quic_proxy = quic_proxy.clone();
        let task_registration = registration.clone();
        let (tx, rx) = watch::channel(HealthCheckStatus::Pending);
        let task = OwnedTask::spawn("registration health check", move |stop| async move {
            loop {
                let status = tokio::select! {
                    _ = stop.cancelled() => break,
                    status = sample_health_check_status(&task_quic_proxy, &task_registration) => status,
                };
                let _ = tx.send_replace(status);
                tokio::select! {
                    _ = stop.cancelled() => break,
                    _ = tokio::time::sleep(HEALTH_CHECK_INTERVAL) => {}
                }
            }
        });

        Self {
            quic_proxy,
            registration,
            task,
            rx,
        }
    }

    pub(super) async fn shutdown(self) {
        self.task.shutdown(HEALTH_CHECK_SHUTDOWN_TIMEOUT).await;
    }

    pub(super) async fn latest_ready_rtt_or_probe(&mut self) -> Option<Duration> {
        match self.latest_status() {
            HealthCheckStatus::Ready(rtt) => Some(rtt),
            HealthCheckStatus::Pending => self
                .quic_proxy
                .health_check_rtt(&self.registration)
                .await
                .ok(),
        }
    }

    fn latest_status(&mut self) -> HealthCheckStatus {
        *self.rx.borrow_and_update()
    }

    #[cfg(test)]
    pub(super) async fn changed(&mut self) -> Result<(), watch::error::RecvError> {
        self.rx.changed().await
    }
}

#[derive(Clone, Copy, Debug)]
enum HealthCheckStatus {
    Pending,
    Ready(Duration),
}

async fn sample_health_check_status(
    quic_proxy: &QuicHttpProxy,
    registration: &Arc<RegistrationGeneration>,
) -> HealthCheckStatus {
    if quic_proxy.has_healthy_connection(registration) {
        match quic_proxy.health_check_rtt(registration).await {
            Ok(rtt) => HealthCheckStatus::Ready(rtt),
            Err(error) => {
                warn!(
                    inference_server_id = %registration.inference_server_id(),
                    error = %error,
                    "health check failed"
                );
                HealthCheckStatus::Pending
            }
        }
    } else {
        HealthCheckStatus::Pending
    }
}
