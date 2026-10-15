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

use std::collections::HashMap;

use stargate_proto::pb::{
    InferenceServerModelRegistration, InferenceServerRegistration, InferenceServerStatus,
};

#[derive(Debug, Clone)]
pub(super) struct AdvertisedModelStatus {
    pub(super) model_id: String,
    pub(super) status: InferenceServerStatus,
}

pub(super) fn advertised_model_statuses(
    update: &InferenceServerRegistration,
) -> Vec<AdvertisedModelStatus> {
    update
        .models
        .iter()
        .map(|(model_id, registration)| AdvertisedModelStatus {
            model_id: model_id.clone(),
            status: InferenceServerStatus::try_from(registration.status)
                .unwrap_or(InferenceServerStatus::Unknown),
        })
        .collect()
}

pub(super) fn build_inference_server_registration(
    inference_server_id: &str,
    cluster_id: &str,
    inference_server_url: &str,
    models: &HashMap<String, InferenceServerModelRegistration>,
    reverse_tunnel: bool,
    reverse_connected: bool,
) -> InferenceServerRegistration {
    let mut models = models.clone();
    for model in models.values_mut() {
        let model_status =
            InferenceServerStatus::try_from(model.status).unwrap_or(InferenceServerStatus::Unknown);
        model.status =
            router_advertised_status(model_status, reverse_tunnel, reverse_connected).into();
    }
    InferenceServerRegistration {
        inference_server_id: inference_server_id.to_string(),
        cluster_id: cluster_id.to_string(),
        inference_server_url: inference_server_url.to_string(),
        models,
        reverse_tunnel,
    }
}

pub(super) fn router_advertised_status(
    model_status: InferenceServerStatus,
    reverse_tunnel: bool,
    reverse_connected: bool,
) -> InferenceServerStatus {
    if reverse_tunnel && model_status == InferenceServerStatus::Active && !reverse_connected {
        InferenceServerStatus::Inactive
    } else {
        model_status
    }
}
