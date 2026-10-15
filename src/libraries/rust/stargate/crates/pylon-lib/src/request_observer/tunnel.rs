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

use reqwest::header::HeaderMap;

use crate::runtime_state::{ModelGeneration, PylonRuntimeState};

use super::embeddings::embedding_items_from_request_body;
use super::{RequestObservationEndpoint, RequestObserver, RequiredTunnelHeaders};

pub(crate) struct TunnelRequestObserver {
    observer: RequestObserver,
}

impl TunnelRequestObserver {
    pub(crate) fn accepted(
        endpoint: RequestObservationEndpoint,
        required: RequiredTunnelHeaders,
        generation: Option<ModelGeneration>,
        runtime_state: PylonRuntimeState,
    ) -> Self {
        Self {
            observer: RequestObserver::from_required(endpoint, required, generation, runtime_state),
        }
    }

    pub(crate) fn is_streaming(&self) -> bool {
        self.observer.endpoint != RequestObservationEndpoint::Embeddings
    }

    pub(crate) fn generation_mut(&mut self) -> Option<&mut RequestObserver> {
        self.is_streaming().then_some(&mut self.observer)
    }

    pub(crate) fn observe_request_body(&mut self, body_bytes: &[u8]) {
        if !self.is_streaming() {
            self.observer
                .update_embedding_items(embedding_items_from_request_body(body_bytes));
        }
    }

    pub(crate) fn on_upstream_response_headers(
        &mut self,
        response_headers: &HeaderMap,
        status: u16,
    ) {
        self.observer
            .on_upstream_response_headers(response_headers, status);
    }

    pub(crate) fn finish(&mut self) {
        self.observer.finish();
    }

    pub(crate) fn fail(&mut self) {
        self.observer.fail();
    }

    fn is_terminal(&self) -> bool {
        self.observer.is_terminal()
    }
}

impl Drop for TunnelRequestObserver {
    fn drop(&mut self) {
        if !self.is_terminal() {
            self.fail();
        }
    }
}
