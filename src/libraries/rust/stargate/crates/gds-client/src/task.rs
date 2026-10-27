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

//! A spawned task that stops with its owner.

use std::future::Future;

use tokio::task::JoinHandle;
use tokio_util::sync::CancellationToken;

/// A background task with its own child cancellation token. Dropping the
/// owner cancels and aborts the task; [`OwnedTask::shutdown`] cancels it and
/// waits for it to finish.
#[derive(Debug)]
pub(crate) struct OwnedTask {
    stop: CancellationToken,
    handle: Option<JoinHandle<()>>,
}

impl OwnedTask {
    pub(crate) fn spawn<F, Fut>(parent: &CancellationToken, task: F) -> Self
    where
        F: FnOnce(CancellationToken) -> Fut,
        Fut: Future<Output = ()> + Send + 'static,
    {
        let stop = parent.child_token();
        let handle = tokio::spawn(task(stop.clone()));
        Self {
            stop,
            handle: Some(handle),
        }
    }

    /// Cancels the task and waits until it returns.
    pub(crate) async fn shutdown(mut self) {
        self.stop.cancel();
        if let Some(handle) = self.handle.take()
            && let Err(error) = handle.await
            && error.is_panic()
        {
            std::panic::resume_unwind(error.into_panic());
        }
    }
}

impl Drop for OwnedTask {
    fn drop(&mut self) {
        self.stop.cancel();
        if let Some(handle) = &self.handle {
            handle.abort();
        }
    }
}
