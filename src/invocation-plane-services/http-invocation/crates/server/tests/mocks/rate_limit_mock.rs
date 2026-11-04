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

use nvcf_invocation_service::rate_limit::rate_limit_api::{
    rate_limit_service_server::{RateLimitService, RateLimitServiceServer},
    RateLimitRequest, RateLimitResponse,
};
use std::net::SocketAddr;
use tokio::net::TcpListener;
use tonic::transport::Server;
use tonic::{Request, Response, Status};

pub struct RateLimitMock {
    pub callback:
        Box<dyn Fn(RateLimitRequest) -> Result<Response<RateLimitResponse>, Status> + Send + Sync>,
}

#[tonic::async_trait]
impl RateLimitService for RateLimitMock {
    async fn rate_limit(
        &self,
        request: Request<RateLimitRequest>,
    ) -> Result<Response<RateLimitResponse>, Status> {
        (self.callback)(request.into_inner())
    }
}

#[allow(unused)]
impl RateLimitMock {
    pub async fn into_server(self) -> RateLimitMockServer {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        let serve = Server::builder()
            .add_service(RateLimitServiceServer::new(self))
            .serve_with_incoming(tokio_stream::wrappers::TcpListenerStream::new(listener));
        let handle =
            tokio::spawn(async move { serve.await.expect("mock rate limit api should start up") });
        tracing::info!("rate limit mock started");
        RateLimitMockServer { address, handle }
    }
}

pub struct RateLimitMockServer {
    address: SocketAddr,
    handle: tokio::task::JoinHandle<()>,
}

#[allow(unused)]
impl RateLimitMockServer {
    pub fn address(&self) -> String {
        self.address.to_string()
    }
}

impl Drop for RateLimitMockServer {
    fn drop(&mut self) {
        self.handle.abort()
    }
}
