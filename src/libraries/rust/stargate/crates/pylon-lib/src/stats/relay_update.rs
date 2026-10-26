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

//! Updates the Dynamo KV DC Relay session sends to the stats collector.
//!
//! Each update replaces the Relay's whole view: a model this pylon serves that
//! is absent from an update has no Relay load (or KV usage) and is cleared, not
//! left at its previous value. Updates are keyed by advertised model name and
//! apply only to a model's live generation; they never create a generation.

use std::collections::BTreeMap;

/// Model load from one `SubscribeServingLoad` window.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct RelayLoadUpdate {
    /// The window's Relay clock (`ServingLoadUpdate.observed_ms`).
    pub(crate) observed_at_unix_ms: u64,
    pub(crate) models: BTreeMap<String, RelayModelLoad>,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct RelayModelLoad {
    /// The Relay reports a ready frontend and a serving pool for the model,
    /// whatever the window's `DataStatus`.
    pub(crate) ready: bool,
    /// `Some` only when the model's load is complete in this window; an
    /// incomplete window clears the gauges instead of reporting them.
    pub(crate) load: Option<RelayModelGauges>,
}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct RelayModelGauges {
    /// Rates since the previous window of the same `relay_incarnation`; `None`
    /// on the first window of an incarnation.
    pub(crate) input_tps: Option<f64>,
    pub(crate) output_tps: Option<f64>,
    pub(crate) queue_size: u64,
    pub(crate) queued_input_size: u64,
    pub(crate) num_running_queries: u64,
    pub(crate) total_query_input_size: u64,
    pub(crate) input_processing_queries: u64,
    pub(crate) output_generation_queries: u64,
    pub(crate) max_engine_concurrency: Option<u64>,
}

/// KV usage from one `SubscribeKvPoolLoad` window. Only models with complete
/// usage are present, so a model with partial rank coverage reads as zero.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct RelayKvUpdate {
    /// The window's Relay clock (`KvPoolLoadUpdate.observed_ms`).
    pub(crate) observed_at_unix_ms: u64,
    pub(crate) models: BTreeMap<String, RelayModelKv>,
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct RelayModelKv {
    pub(crate) capacity_tokens: u64,
    pub(crate) used_tokens: u64,
}
