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

use std::time::Duration;

use tokio::sync::watch;
use tokio::task::JoinHandle;
use tokio_util::sync::CancellationToken;

pub(super) const REVERSE_TUNNEL_CONNECT_TIMEOUT: Duration = Duration::from_secs(5);
pub(super) const REGISTRATION_TASK_SHUTDOWN_TIMEOUT: Duration = Duration::from_secs(5);
pub(super) const CLUSTER_CALIBRATION_SUBMISSION_TIMEOUT: Duration = Duration::from_secs(5);

pub(super) struct NamedJoinHandle {
    name: &'static str,
    task: JoinHandle<()>,
}

impl NamedJoinHandle {
    pub(super) fn new(name: &'static str, task: JoinHandle<()>) -> Self {
        Self { name, task }
    }
}

pub(super) fn abort_named_join_handle(task: NamedJoinHandle) {
    task.task.abort();
}

struct AbortOnDropJoinHandle {
    handle: Option<JoinHandle<()>>,
}

impl AbortOnDropJoinHandle {
    fn new(handle: JoinHandle<()>) -> Self {
        Self {
            handle: Some(handle),
        }
    }

    fn abort(&self) {
        if let Some(handle) = &self.handle {
            handle.abort();
        }
    }

    async fn join(&mut self) -> Result<(), tokio::task::JoinError> {
        self.handle
            .as_mut()
            .expect("join handle should not be disarmed before join")
            .await
    }

    fn disarm(&mut self) {
        let _completed = self.handle.take();
    }
}

impl Drop for AbortOnDropJoinHandle {
    fn drop(&mut self) {
        if let Some(handle) = &self.handle {
            // A parent shutdown deadline can cancel the join wait; abort before
            // dropping the handle so the child task is not detached.
            handle.abort();
        }
    }
}

pub(super) async fn await_named_join_handles(tasks: Vec<NamedJoinHandle>, timeout: Duration) {
    for task in tasks {
        await_named_join_handle(task, timeout).await;
    }
}

pub(super) async fn await_named_join_handle(task: NamedJoinHandle, timeout: Duration) {
    await_named_join_handle_until(task, tokio::time::Instant::now() + timeout).await;
}

async fn await_named_join_handle_until(task: NamedJoinHandle, deadline: tokio::time::Instant) {
    let name = task.name;
    let mut handle = AbortOnDropJoinHandle::new(task.task);
    let remaining = match deadline.checked_duration_since(tokio::time::Instant::now()) {
        Some(duration) if !duration.is_zero() => duration,
        _ => {
            tracing::warn!(task = name, "task did not stop before shutdown deadline");
            // Cooperative shutdown missed the shared deadline; abort is the final fallback.
            handle.abort();
            let result = handle.join().await;
            handle.disarm();
            finish_joined_task(name, result);
            return;
        }
    };

    match tokio::time::timeout(remaining, handle.join()).await {
        Ok(result) => {
            handle.disarm();
            finish_joined_task(name, result);
        }
        Err(_) => {
            tracing::warn!(
                task = name,
                timeout_ms = remaining.as_millis(),
                "task did not stop before shutdown timeout"
            );
            // Cooperative shutdown missed the timeout; abort is the final fallback.
            handle.abort();
            let result = handle.join().await;
            handle.disarm();
            finish_joined_task(name, result);
        }
    }
}

fn finish_joined_task(name: &'static str, result: Result<(), tokio::task::JoinError>) {
    match result {
        Ok(()) => {}
        Err(error) if error.is_cancelled() => {}
        Err(error) if error.is_panic() => std::panic::resume_unwind(error.into_panic()),
        Err(error) => {
            tracing::warn!(task = name, error = %error, "task join failed");
        }
    }
}

pub(super) fn stop_channel_changed(
    changed: std::result::Result<(), watch::error::RecvError>,
    stop_rx: &watch::Receiver<bool>,
) -> bool {
    changed.is_err() || *stop_rx.borrow()
}

pub(super) async fn sleep_until_registration_stop(
    parent_stop_rx: &mut watch::Receiver<bool>,
    local_stop_rx: &mut watch::Receiver<bool>,
    cancel_token: &CancellationToken,
    duration: Duration,
) -> bool {
    tokio::select! {
        _ = cancel_token.cancelled() => true,
        changed = parent_stop_rx.changed() => changed.is_err() || registration_should_stop(parent_stop_rx, local_stop_rx, cancel_token),
        changed = local_stop_rx.changed() => changed.is_err() || registration_should_stop(parent_stop_rx, local_stop_rx, cancel_token),
        _ = tokio::time::sleep(duration) => registration_should_stop(parent_stop_rx, local_stop_rx, cancel_token),
    }
}

pub(super) fn should_stop(
    parent_stop_rx: &watch::Receiver<bool>,
    local_stop_rx: &watch::Receiver<bool>,
) -> bool {
    *parent_stop_rx.borrow() || *local_stop_rx.borrow()
}

pub(super) fn registration_should_stop(
    parent_stop_rx: &watch::Receiver<bool>,
    local_stop_rx: &watch::Receiver<bool>,
    cancel_token: &CancellationToken,
) -> bool {
    should_stop(parent_stop_rx, local_stop_rx) || cancel_token.is_cancelled()
}
