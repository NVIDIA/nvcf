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

use axum::Json;
use axum::extract::State;
use axum::http::{StatusCode, Uri};
use serde::Serialize;
use stargate_proto::pb::{ListModelsRequest, ListModelsResponse};
use stargate_protocol::BackendConnectivity;
use url::form_urlencoded;

use crate::control_plane::list_models_for_state;

use super::ProxyAppState;

/// Static settings that are useful to an operator and safe to expose locally.
#[derive(Clone, Debug, Default, Serialize)]
pub(crate) struct DebugConfig {
    pub(crate) stargate_id: String,
    pub(crate) grpc_listen_addr: String,
    pub(crate) model_discovery_listen_addr: String,
    pub(crate) http_listen_addr: String,
    pub(crate) metrics_listen_addr: Option<String>,
    pub(crate) advertise_addr: String,
    pub(crate) stargate_discovery_dns_name: String,
    pub(crate) tunnel_protocol: String,
    pub(crate) backend_connectivity: BackendConnectivity,
    pub(crate) direct_quic_connections: usize,
}

#[derive(Serialize)]
pub(super) struct DebugStateResponse {
    config: DebugConfig,
    active_model_ids: Vec<String>,
}

pub(super) async fn http_list_models(
    State(app): State<ProxyAppState>,
    uri: Uri,
) -> Result<Json<ListModelsResponse>, StatusCode> {
    list_models_for_state(
        app.state.as_ref(),
        list_models_request_from_query(uri.query()),
    )
    .await
    .map(Json)
    .map_err(|_| StatusCode::BAD_REQUEST)
}

pub(super) async fn debug_state(State(app): State<ProxyAppState>) -> Json<DebugStateResponse> {
    let active_model_ids = app.state.list_active_models_for_debug().await;
    Json(DebugStateResponse {
        config: app.debug_config,
        active_model_ids,
    })
}

fn list_models_request_from_query(query: Option<&str>) -> ListModelsRequest {
    ListModelsRequest {
        model_ids: query
            .into_iter()
            .flat_map(|query| form_urlencoded::parse(query.as_bytes()))
            .filter_map(|(key, value)| (key == "model_ids").then(|| value.into_owned()))
            .collect(),
        ..Default::default()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn http_model_query_preserves_repeated_model_filters_for_list_models() {
        let request =
            list_models_request_from_query(Some("model_ids=%20model-a%20&model_ids=model-b"));

        assert_eq!(request.model_ids, vec![" model-a ", "model-b"]);
    }
}
