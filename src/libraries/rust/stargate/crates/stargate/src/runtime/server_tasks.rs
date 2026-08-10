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

use std::net::{SocketAddr, TcpListener};

use anyhow::{Context, Result};
use axum::Router;
use tokio_stream::wrappers::TcpListenerStream;
use tonic::transport::Server;
use tower::util::MapRequestLayer;

use crate::control_plane::StargateService;
use stargate_proto::pb::{
    stargate_control_plane_server::StargateControlPlaneServer,
    stargate_model_discovery_server::StargateModelDiscoveryServer,
};
use stargate_runtime::CriticalTaskGroup;

pub(super) fn into_tokio_tcp_listener(
    listener: TcpListener,
    name: &'static str,
) -> Result<tokio::net::TcpListener> {
    listener
        .set_nonblocking(true)
        .with_context(|| format!("failed to set {name} listener to non-blocking"))?;
    tokio::net::TcpListener::from_std(listener)
        .with_context(|| format!("failed to convert {name} listener"))
}

pub(super) fn spawn_control_plane_grpc_server(
    tasks: &CriticalTaskGroup,
    listener: tokio::net::TcpListener,
    service: StargateService,
) {
    let incoming = TcpListenerStream::new(listener);
    tasks.spawn_critical("control-plane gRPC server", move |stop| async move {
        let authority_layer = MapRequestLayer::new(|mut req: http::Request<_>| {
            if let Some(authority) = req.uri().authority().cloned() {
                req.extensions_mut().insert(authority);
            }
            req
        });
        Server::builder()
            .layer(authority_layer)
            .add_service(StargateControlPlaneServer::new(service))
            .serve_with_incoming_shutdown(incoming, stop.cancelled_owned())
            .await
            .context("control-plane gRPC server failed")
    });
}

pub(super) fn spawn_model_discovery_grpc_server(
    tasks: &CriticalTaskGroup,
    listener: tokio::net::TcpListener,
    service: StargateService,
) {
    let incoming = TcpListenerStream::new(listener);
    tasks.spawn_critical("model-discovery gRPC server", move |stop| async move {
        Server::builder()
            .add_service(StargateModelDiscoveryServer::new(service))
            .serve_with_incoming_shutdown(incoming, stop.cancelled_owned())
            .await
            .context("model-discovery gRPC server failed")
    });
}

pub(super) fn spawn_http_proxy_server(
    tasks: &CriticalTaskGroup,
    listener: tokio::net::TcpListener,
    proxy_router: Router,
) {
    tasks.spawn_critical("HTTP proxy server", move |stop| async move {
        axum::serve(
            listener,
            proxy_router.into_make_service_with_connect_info::<SocketAddr>(),
        )
        .with_graceful_shutdown(stop.cancelled_owned())
        .await
        .context("HTTP proxy server failed")
    });
}
