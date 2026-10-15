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

use std::{future::Future, time::Duration};

use tokio::task::{JoinError, JoinHandle};
use tokio_util::sync::CancellationToken;

pub const TASK_SHUTDOWN_TIMEOUT: Duration = Duration::from_secs(5);

#[derive(Debug)]
pub struct OwnedTask {
    name: &'static str,
    stop: CancellationToken,
    handle: Option<JoinHandle<()>>,
}

struct TaskExitGuard {
    stop: CancellationToken,
    parent_stop: Option<CancellationToken>,
}

impl Drop for TaskExitGuard {
    fn drop(&mut self) {
        let unexpected = !self.stop.is_cancelled();
        self.stop.cancel();
        if unexpected && let Some(parent_stop) = &self.parent_stop {
            parent_stop.cancel();
        }
    }
}

impl OwnedTask {
    pub fn spawn<F, Fut>(name: &'static str, task: F) -> Self
    where
        F: FnOnce(CancellationToken) -> Fut + Send + 'static,
        Fut: Future<Output = ()> + Send + 'static,
    {
        Self::spawn_with_token(name, CancellationToken::new(), None, task)
    }

    pub fn spawn_child<F, Fut>(name: &'static str, parent: &CancellationToken, task: F) -> Self
    where
        F: FnOnce(CancellationToken) -> Fut + Send + 'static,
        Fut: Future<Output = ()> + Send + 'static,
    {
        Self::spawn_with_token(name, parent.child_token(), Some(parent.clone()), task)
    }

    fn spawn_with_token<F, Fut>(
        name: &'static str,
        stop: CancellationToken,
        parent_stop: Option<CancellationToken>,
        task: F,
    ) -> Self
    where
        F: FnOnce(CancellationToken) -> Fut + Send + 'static,
        Fut: Future<Output = ()> + Send + 'static,
    {
        let task_stop = stop.clone();
        Self {
            name,
            stop,
            handle: Some(tokio::spawn(async move {
                let _exit_guard = TaskExitGuard {
                    stop: task_stop.clone(),
                    parent_stop,
                };
                task(task_stop).await;
            })),
        }
    }

    pub fn abort(&self) {
        self.stop.cancel();
        if let Some(handle) = &self.handle {
            handle.abort();
        }
    }

    pub async fn wait_for_exit(&mut self) -> Result<(), JoinError> {
        let result = self
            .handle
            .as_mut()
            .expect("owned task should not be disarmed before join")
            .await;
        self.handle = None;
        result
    }

    pub async fn shutdown(mut self, timeout: Duration) {
        self.stop.cancel();
        let Some(handle) = self.handle.as_mut() else {
            return;
        };
        let result = match tokio::time::timeout(timeout, &mut *handle).await {
            Ok(result) => result,
            Err(_) => {
                tracing::warn!(
                    task = self.name,
                    timeout_ms = timeout.as_millis(),
                    "task did not stop before shutdown timeout"
                );
                handle.abort();
                handle.await
            }
        };
        self.handle = None;
        finish_joined_task(self.name, result);
    }

    pub async fn shutdown_all(tasks: Vec<Self>, timeout: Duration) {
        for task in &tasks {
            task.stop.cancel();
        }
        futures::future::join_all(tasks.into_iter().map(|task| task.shutdown(timeout))).await;
    }
}

impl Drop for OwnedTask {
    fn drop(&mut self) {
        self.abort();
    }
}

fn finish_joined_task(name: &'static str, result: Result<(), JoinError>) {
    match result {
        Ok(()) => {}
        Err(error) if error.is_cancelled() => {}
        Err(error) if error.is_panic() => std::panic::resume_unwind(error.into_panic()),
        Err(error) => {
            tracing::warn!(task = name, error = %error, "task join failed");
        }
    }
}

#[cfg(test)]
mod tests {
    use std::future::pending;
    use std::time::Duration;

    use tokio::sync::oneshot;
    use tokio_util::sync::CancellationToken;

    use super::OwnedTask;

    const TEST_TIMEOUT: Duration = Duration::from_secs(1);

    struct DropNotifier(Option<oneshot::Sender<()>>);

    impl Drop for DropNotifier {
        fn drop(&mut self) {
            if let Some(tx) = self.0.take() {
                let _ = tx.send(());
            }
        }
    }

    async fn expect_signal(rx: oneshot::Receiver<()>, message: &'static str) {
        tokio::time::timeout(TEST_TIMEOUT, rx)
            .await
            .expect(message)
            .expect("signal sender should remain alive");
    }

    async fn pending_task(
        name: &'static str,
        parent: Option<&CancellationToken>,
    ) -> (OwnedTask, oneshot::Receiver<()>) {
        let (entered_tx, entered_rx) = oneshot::channel();
        let (dropped_tx, dropped_rx) = oneshot::channel();
        let task = move |_| async move {
            let _drop_notifier = DropNotifier(Some(dropped_tx));
            let _ = entered_tx.send(());
            pending::<()>().await;
        };
        let task = match parent {
            Some(parent) => OwnedTask::spawn_child(name, parent, task),
            None => OwnedTask::spawn(name, task),
        };
        entered_rx.await.expect("owned task should start");
        (task, dropped_rx)
    }

    #[tokio::test]
    async fn owned_task_drop_aborts_pending_task() {
        let (task, dropped) = pending_task("pending owned task", None).await;

        // Dropping the owner is the behavior under test.
        drop(task);

        expect_signal(dropped, "dropping the owner should abort the task").await;
    }

    #[tokio::test]
    async fn cancelled_owned_task_shutdown_aborts_task() {
        let (task, dropped) = pending_task("pending shutdown task", None).await;

        {
            let shutdown = task.shutdown(Duration::from_secs(30));
            tokio::pin!(shutdown);
            tokio::select! {
                biased;
                _ = &mut shutdown => panic!("pending task should not finish before cancellation"),
                _ = tokio::task::yield_now() => {}
            }
        }

        expect_signal(dropped, "cancelling shutdown should abort the owned task").await;
    }

    #[tokio::test]
    async fn owned_parent_drop_aborts_descendant() {
        let (parent_ready_tx, parent_ready_rx) = oneshot::channel();
        let parent = OwnedTask::spawn("parent with descendant", |parent_stop| async move {
            let (_child, dropped) = pending_task("pending descendant", Some(&parent_stop)).await;
            let _ = parent_ready_tx.send(dropped);
            pending::<()>().await;
        });
        let dropped = parent_ready_rx.await.expect("parent should own descendant");

        drop(parent);

        expect_signal(dropped, "dropping parent should abort descendant").await;
    }

    #[tokio::test]
    async fn owned_parent_shutdown_cancels_descendant_before_abort_fallback() {
        let (child_cancelled_tx, child_cancelled_rx) = oneshot::channel();
        let parent = OwnedTask::spawn("cancellable parent", |parent_stop| async move {
            let child = OwnedTask::spawn_child(
                "cancellable child",
                &parent_stop,
                |child_stop| async move {
                    child_stop.cancelled().await;
                    let _ = child_cancelled_tx.send(());
                },
            );
            parent_stop.cancelled().await;
            child.shutdown(TEST_TIMEOUT).await;
        });

        parent.shutdown(TEST_TIMEOUT).await;

        child_cancelled_rx
            .await
            .expect("parent shutdown should cooperatively cancel descendant");
    }

    #[tokio::test]
    async fn owned_child_shutdown_does_not_cancel_parent_or_sibling() {
        let parent_stop = CancellationToken::new();
        let child = OwnedTask::spawn_child("first child", &parent_stop, |stop| async move {
            stop.cancelled().await;
        });
        let (sibling_cancelled_tx, sibling_cancelled_rx) = oneshot::channel();
        let sibling = OwnedTask::spawn_child("sibling", &parent_stop, |stop| async move {
            stop.cancelled().await;
            let _ = sibling_cancelled_tx.send(());
        });

        child.shutdown(TEST_TIMEOUT).await;

        assert!(!parent_stop.is_cancelled());
        let mut sibling_cancelled_rx = sibling_cancelled_rx;
        assert!(
            tokio::time::timeout(Duration::from_millis(10), &mut sibling_cancelled_rx)
                .await
                .is_err(),
            "stopping one child must not cancel its sibling"
        );

        parent_stop.cancel();
        sibling.shutdown(TEST_TIMEOUT).await;
        sibling_cancelled_rx
            .await
            .expect("parent cancellation should reach sibling");
    }

    #[tokio::test]
    async fn owned_task_group_shutdown_signals_every_task_before_waiting() {
        let (blocking_cancelled_tx, blocking_cancelled_rx) = oneshot::channel();
        let blocking = OwnedTask::spawn("blocking task", |stop| async move {
            stop.cancelled().await;
            let _ = blocking_cancelled_tx.send(());
            pending::<()>().await;
        });
        let (sibling_cancelled_tx, sibling_cancelled_rx) = oneshot::channel();
        let sibling = OwnedTask::spawn("sibling task", |stop| async move {
            stop.cancelled().await;
            let _ = sibling_cancelled_tx.send(());
        });

        let shutdown = tokio::spawn(OwnedTask::shutdown_all(
            vec![blocking, sibling],
            Duration::from_millis(100),
        ));

        blocking_cancelled_rx
            .await
            .expect("blocking task should receive cancellation");
        tokio::time::timeout(Duration::from_millis(50), sibling_cancelled_rx)
            .await
            .expect("sibling cancellation must not wait for blocking task")
            .expect("sibling should receive cancellation");
        shutdown.await.expect("group shutdown should finish");
    }

    #[tokio::test]
    async fn unexpected_child_exit_cancels_parent() {
        let parent_stop = CancellationToken::new();
        let mut child =
            OwnedTask::spawn_child("unexpectedly finite child", &parent_stop, |_| async {});

        child
            .wait_for_exit()
            .await
            .expect("finite child should exit successfully");

        assert!(
            parent_stop.is_cancelled(),
            "unexpected child completion must cancel its physical parent"
        );
    }

    #[tokio::test]
    async fn panicking_child_cancels_parent() {
        let parent_stop = CancellationToken::new();
        let mut child = OwnedTask::spawn_child("panicking child", &parent_stop, |_| async {
            panic!("child task failed");
        });

        let error = child
            .wait_for_exit()
            .await
            .expect_err("panicking child should report join failure");

        assert!(error.is_panic());
        assert!(
            parent_stop.is_cancelled(),
            "child panic must cancel its physical parent"
        );
    }

    #[tokio::test]
    async fn unexpected_descendant_exit_wakes_waiting_parent() {
        let mut parent = OwnedTask::spawn("waiting parent", |parent_stop| async move {
            let child =
                OwnedTask::spawn_child("unexpectedly finite descendant", &parent_stop, |_| async {
                });
            parent_stop.cancelled().await;
            child.shutdown(TEST_TIMEOUT).await;
        });

        tokio::time::timeout(TEST_TIMEOUT, parent.wait_for_exit())
            .await
            .expect("unexpected descendant exit should wake its waiting parent")
            .expect("waiting parent should stop cleanly");
    }

    #[tokio::test]
    async fn root_exit_cancels_its_subtree_before_observation() {
        let (descendant_stop_tx, descendant_stop_rx) = oneshot::channel();
        let mut root = OwnedTask::spawn("finite root", |root_stop| async move {
            let _ = descendant_stop_tx.send(root_stop.child_token());
        });
        let descendant_stop = descendant_stop_rx
            .await
            .expect("root should publish descendant token");

        tokio::time::timeout(TEST_TIMEOUT, descendant_stop.cancelled())
            .await
            .expect("root exit must close the owned subtree before join");
        root.wait_for_exit()
            .await
            .expect("finite root should exit successfully");

        assert!(descendant_stop.is_cancelled());
    }
}
