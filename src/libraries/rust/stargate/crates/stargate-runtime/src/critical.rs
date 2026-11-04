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

use std::{any::Any, fmt, future::Future, panic::AssertUnwindSafe};

use futures::FutureExt;
use tokio_util::{sync::CancellationToken, task::TaskTracker};

pub type CriticalTaskFailureReceiver = flume::Receiver<CriticalTaskFailure>;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CriticalTaskFailure {
    process: &'static str,
    name: &'static str,
    detail: String,
}

impl CriticalTaskFailure {
    pub fn process_name(&self) -> &'static str {
        self.process
    }

    pub fn task_name(&self) -> &'static str {
        self.name
    }

    pub fn detail(&self) -> &str {
        &self.detail
    }
}

impl fmt::Display for CriticalTaskFailure {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            formatter,
            "critical {} task `{}` exited: {}",
            self.process, self.name, self.detail
        )
    }
}

impl std::error::Error for CriticalTaskFailure {}

#[derive(Clone)]
pub struct CriticalTaskGroup {
    process: &'static str,
    stop: CancellationToken,
    tasks: TaskTracker,
    failure_tx: flume::Sender<CriticalTaskFailure>,
}

impl CriticalTaskGroup {
    pub fn new(process: &'static str) -> (Self, CriticalTaskFailureReceiver) {
        let (failure_tx, failure_rx) = flume::bounded(1);
        (
            Self {
                process,
                stop: CancellationToken::new(),
                tasks: TaskTracker::new(),
                failure_tx,
            },
            failure_rx,
        )
    }

    pub fn shutdown_signal(&self) -> CancellationToken {
        self.stop.child_token()
    }

    pub fn task_tracker(&self) -> TaskTracker {
        self.tasks.clone()
    }

    pub fn is_stopping(&self) -> bool {
        self.stop.is_cancelled()
    }

    pub fn spawn_critical<F, Fut>(&self, name: &'static str, task: F)
    where
        F: FnOnce(CancellationToken) -> Fut + Send + 'static,
        Fut: Future<Output = anyhow::Result<()>> + Send + 'static,
    {
        let group = self.clone();
        let stop = self.shutdown_signal();
        self.tasks.spawn(async move {
            let outcome = AssertUnwindSafe(task(stop)).catch_unwind().await;
            if group.is_stopping() {
                return;
            }

            let detail = match outcome {
                Ok(Ok(())) => "exited unexpectedly".to_string(),
                Ok(Err(error)) => format!("{error:#}"),
                Err(panic) => format!("panicked: {}", panic_detail(panic.as_ref())),
            };
            tracing::error!(
                process = group.process,
                task = name,
                error = detail,
                "critical task exited"
            );
            let _ = group.failure_tx.try_send(CriticalTaskFailure {
                process: group.process,
                name,
                detail,
            });
            group.begin_shutdown();
        });
    }

    pub fn begin_shutdown(&self) {
        self.stop.cancel();
        self.tasks.close();
    }

    pub async fn wait(&self) {
        self.tasks.wait().await;
    }
}

fn panic_detail(panic: &(dyn Any + Send)) -> &str {
    panic
        .downcast_ref::<&'static str>()
        .copied()
        .or_else(|| panic.downcast_ref::<String>().map(String::as_str))
        .unwrap_or("unknown panic payload")
}

#[cfg(test)]
mod tests {
    use std::time::Duration;

    use flume::TryRecvError;

    use super::{CriticalTaskFailure, CriticalTaskGroup};

    async fn receive_failure(
        failures: &flume::Receiver<CriticalTaskFailure>,
        message: &'static str,
    ) -> CriticalTaskFailure {
        tokio::time::timeout(Duration::from_secs(1), failures.recv_async())
            .await
            .expect(message)
            .expect("critical failure channel should remain open")
    }

    #[tokio::test]
    async fn unexpected_critical_return_cancels_runtime_and_reports_root() {
        let (group, failures) = CriticalTaskGroup::new("test process");

        group.spawn_critical("finite root", |_| async { Ok(()) });

        let failure = receive_failure(&failures, "critical return should report failure").await;
        assert_eq!(failure.process_name(), "test process");
        assert_eq!(failure.task_name(), "finite root");
        assert_eq!(failure.detail(), "exited unexpectedly");
        assert!(group.is_stopping());
    }

    #[tokio::test]
    async fn critical_error_cancels_runtime_and_preserves_detail() {
        let (group, failures) = CriticalTaskGroup::new("test process");

        group.spawn_critical("failing root", |_| async {
            anyhow::bail!("listener failed")
        });

        let failure = receive_failure(&failures, "critical error should report failure").await;
        assert_eq!(failure.task_name(), "failing root");
        assert!(failure.detail().contains("listener failed"));
        assert!(group.is_stopping());
    }

    #[tokio::test]
    async fn critical_panic_cancels_runtime_and_reports_panic() {
        let (group, failures) = CriticalTaskGroup::new("test process");

        group.spawn_critical("panicking root", |_| async { panic!("root panic") });

        let failure = receive_failure(&failures, "critical panic should report failure").await;
        assert_eq!(failure.task_name(), "panicking root");
        assert!(failure.detail().contains("root panic"));
        assert!(group.is_stopping());
    }

    #[tokio::test]
    async fn requested_shutdown_does_not_report_critical_failure() {
        let (group, failures) = CriticalTaskGroup::new("test process");
        group.spawn_critical("stoppable root", |stop| async move {
            stop.cancelled().await;
            Ok(())
        });

        group.begin_shutdown();
        tokio::time::timeout(Duration::from_secs(1), group.wait())
            .await
            .expect("requested shutdown should finish");

        assert_eq!(failures.try_recv(), Err(TryRecvError::Empty));
    }

    #[tokio::test]
    async fn dynamic_task_completion_does_not_stop_runtime() {
        let (group, failures) = CriticalTaskGroup::new("test process");

        group.task_tracker().spawn(async {});
        tokio::task::yield_now().await;

        assert!(!group.is_stopping());
        assert_eq!(failures.try_recv(), Err(TryRecvError::Empty));
        group.begin_shutdown();
        group.wait().await;
    }

    #[tokio::test]
    async fn cancelling_observer_signal_does_not_stop_runtime_owner() {
        let (group, failures) = CriticalTaskGroup::new("test process");
        let observer = group.shutdown_signal();

        observer.cancel();

        assert!(observer.is_cancelled());
        assert!(!group.is_stopping());
        assert_eq!(failures.try_recv(), Err(TryRecvError::Empty));
        group.begin_shutdown();
        group.wait().await;
    }
}
