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

use std::time::{Duration, Instant};

use axum::http::{HeaderMap, Method};

use super::body::remaining_request_timeout;

pub(super) struct OpenTunnelRequest<'a> {
    pub(super) method: Method,
    pub(super) path_and_query: &'a str,
    pub(super) headers: HeaderMap,
    started_at: Instant,
    request_timeout: Duration,
}

impl<'a> OpenTunnelRequest<'a> {
    pub(super) fn new(
        method: Method,
        path_and_query: &'a str,
        headers: HeaderMap,
        request_timeout: Duration,
    ) -> Self {
        Self {
            method,
            path_and_query,
            headers,
            started_at: Instant::now(),
            request_timeout,
        }
    }

    pub(super) fn response_header_timeout(&self) -> Duration {
        remaining_request_timeout(self.started_at, self.request_timeout)
    }
}
