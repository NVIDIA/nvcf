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

//! Profiling module for memory profiling with jemalloc and pprof

#[cfg(all(feature = "jemalloc", feature = "profiling"))]
pub mod pprof_server {
    use axum::{
        http::{HeaderMap, HeaderValue, StatusCode},
        response::Response,
        routing::get,
        Router,
    };
    use tokio::net::TcpListener;
    use tracing::{error, info};

    /// Start the pprof HTTP server on a separate port
    pub async fn start_pprof_server(port: u16) -> anyhow::Result<()> {
        let app = Router::new().route("/debug/pprof/allocs", get(heap_profile));

        let listener = TcpListener::bind(format!("0.0.0.0:{}", port)).await?;
        info!(
            "pprof server listening on http://{}",
            listener.local_addr()?
        );

        axum::serve(listener, app).await?;
        Ok(())
    }

    /// Handler for heap profile endpoint (pprof format)
    async fn heap_profile() -> Result<Response, StatusCode> {
        let prof_ctl = match jemalloc_pprof::PROF_CTL.as_ref() {
            Some(ctl) => ctl,
            None => {
                error!("Profiling not available - PROF_CTL is None");
                return Err(StatusCode::SERVICE_UNAVAILABLE);
            }
        };

        let mut prof_ctl = prof_ctl.lock().await;

        require_profiling_activated(&prof_ctl)?;

        match prof_ctl.dump_pprof() {
            Ok(profile_data) => {
                let mut headers = HeaderMap::new();
                headers.insert(
                    "Content-Type",
                    HeaderValue::from_static("application/octet-stream"),
                );
                headers.insert(
                    "Content-Disposition",
                    HeaderValue::from_static("attachment; filename=heap.pb.gz"),
                );

                let mut response = Response::new(profile_data.into());
                *response.headers_mut() = headers;
                Ok(response)
            }
            Err(e) => {
                error!("Failed to generate heap profile: {}", e);
                Err(StatusCode::INTERNAL_SERVER_ERROR)
            }
        }
    }

    /// Helper function to check if profiling is activated
    fn require_profiling_activated(
        prof_ctl: &jemalloc_pprof::JemallocProfCtl,
    ) -> Result<(), StatusCode> {
        if prof_ctl.activated() {
            Ok(())
        } else {
            error!("Heap profiling not activated");
            Err(StatusCode::FORBIDDEN)
        }
    }
}
