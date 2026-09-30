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

use std::time::Instant;

use axum::body::Body;
use axum::http::{HeaderMap, Method, StatusCode};
use axum::response::Response;

use crate::load_balancer::LoadBalancerAlgorithmResolution;

use super::ProxyAppState;
use super::attempt::{ProxyAttemptCounters, ProxyAttemptOutcome};
use super::request::ProxyRequestInputs;
use super::request_routing::RequestRouting;
use super::retry::ReplayableRequestBody;
use super::routing::{RoutingRejectionContext, routing_rejection_response};

pub(super) struct PreparedProxyRequest {
    pub(super) request_inputs: ProxyRequestInputs,
    pub(super) lb_resolution: LoadBalancerAlgorithmResolution,
    pub(super) request: ProxyRequest,
}

pub(super) struct ProxyRequest {
    pub(super) endpoint_name: &'static str,
    pub(super) method: Method,
    pub(super) path_and_query: String,
    pub(super) forwarded_headers: HeaderMap,
    pub(super) retry_deadline: Option<Instant>,
    pub(super) request_start: Instant,
    pub(super) replay_body: ReplayableRequestBody,
}

pub(super) struct ProxyRequestRun<'a> {
    app: &'a ProxyAppState,
    request: PreparedProxyRequest,
    routing_started_at: Instant,
}

pub(super) struct ProxyAttemptRun<'a> {
    pub(super) app: &'a ProxyAppState,
    pub(super) request_inputs: &'a ProxyRequestInputs,
    pub(super) request: ProxyRequest,
    pub(super) attempt_counters: ProxyAttemptCounters,
    pub(super) last_attempt_capacity_rejected: bool,
}

impl<'a> ProxyRequestRun<'a> {
    pub(super) fn new(app: &'a ProxyAppState, request: PreparedProxyRequest) -> Self {
        Self {
            app,
            request,
            routing_started_at: Instant::now(),
        }
    }

    pub(super) async fn execute(self) -> Result<Response<Body>, StatusCode> {
        let PreparedProxyRequest {
            request_inputs,
            lb_resolution,
            request,
        } = self.request;
        let mut routing = RequestRouting::new(
            &self.app.state,
            &self.app.lb_router,
            &self.app.metrics,
            &request_inputs,
            &lb_resolution,
            request.request_start,
            self.routing_started_at,
        );
        let mut attempts = ProxyAttemptRun {
            app: self.app,
            request_inputs: &request_inputs,
            request,
            attempt_counters: ProxyAttemptCounters::default(),
            last_attempt_capacity_rejected: false,
        };
        loop {
            let failed_backend_count = routing.failed_backend_count();
            let (selected, backend) = match routing.next().await {
                Ok(selected) => selected,
                Err(rejection) => {
                    return routing_rejection_response(RoutingRejectionContext {
                        metrics: &self.app.metrics,
                        target: &request_inputs.target,
                        rejection,
                        failed_backend_count: routing.failed_backend_count(),
                        failed_cluster_count: routing.failed_cluster_count(),
                        routing_retry_attempts: routing.retry_attempts(),
                        capacity_rejected: attempts.last_attempt_capacity_rejected,
                    });
                }
            };
            match attempts
                .run_proxy_attempt(selected, backend, failed_backend_count)
                .await
            {
                ProxyAttemptOutcome::ReturnFinal(response) => return Ok(response),
                ProxyAttemptOutcome::ProxyError(status) => return Err(status),
                ProxyAttemptOutcome::RetrySameBackend => {}
                ProxyAttemptOutcome::RetryAlternateBackend(backend_id) => {
                    routing.exclude_backend(backend_id)
                }
                ProxyAttemptOutcome::RetryAlternateCluster(cluster_id) => {
                    routing.exclude_cluster(cluster_id)
                }
            }
        }
    }
}
