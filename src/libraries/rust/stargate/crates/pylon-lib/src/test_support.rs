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

use std::ops::Deref;

use axum::Router;
use tokio::net::TcpListener;
use tokio::task::JoinHandle;

pub(crate) struct TestHttpServer {
    base_url: String,
    task: Option<JoinHandle<()>>,
}

impl TestHttpServer {
    pub(crate) async fn spawn(app: Router) -> Self {
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .expect("test HTTP listener should bind");
        let addr = listener
            .local_addr()
            .expect("test HTTP listener should have an address");
        let task = tokio::spawn(async move {
            axum::serve(listener, app)
                .await
                .expect("test HTTP server should run");
        });
        Self {
            base_url: format!("http://{addr}"),
            task: Some(task),
        }
    }

    pub(crate) fn as_str(&self) -> &str {
        &self.base_url
    }

    pub(crate) async fn shutdown(mut self) {
        if let Some(task) = self.task.take() {
            task.abort();
            let _ = task.await;
        }
    }
}

impl Deref for TestHttpServer {
    type Target = str;

    fn deref(&self) -> &Self::Target {
        &self.base_url
    }
}

impl Drop for TestHttpServer {
    fn drop(&mut self) {
        if let Some(task) = self.task.take() {
            task.abort();
        }
    }
}
