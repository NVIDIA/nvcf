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

//! Upstream inference-server dialects.
//!
//! Pylon speaks the platform tunnel contract upward and translates it into
//! the dialect of the engine it fronts. All engine-specific header names and
//! encodings live in this module.

use std::fmt;
use std::str::FromStr;

/// Which engine dialect pylon speaks to its local upstream. Future engines
/// add a variant and a submodule here, never a new CLI flag.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum UpstreamBackend {
    /// Forward requests unchanged; derive nothing. Inbound engine priority
    /// headers are still stripped.
    Passthrough,
    /// Derive the engine priority headers from `x-priority`.
    #[default]
    Dynamo,
}

impl fmt::Display for UpstreamBackend {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(match self {
            Self::Passthrough => "passthrough",
            Self::Dynamo => "dynamo",
        })
    }
}

impl FromStr for UpstreamBackend {
    type Err = String;

    fn from_str(value: &str) -> Result<Self, Self::Err> {
        match value.trim().to_ascii_lowercase().as_str() {
            "passthrough" => Ok(Self::Passthrough),
            "dynamo" => Ok(Self::Dynamo),
            other => Err(format!(
                "unknown upstream backend {other:?}; expected \"passthrough\" or \"dynamo\""
            )),
        }
    }
}

/// Default priority band ceiling; see [`dynamo::request_priority`].
pub const DEFAULT_PRIORITY_CEILING: u32 = 3600;

pub(crate) mod dynamo {
    use reqwest::header::{HeaderMap, HeaderName, HeaderValue};
    use stargate_protocol::tunnel_contract::{HEADER_MODEL, HEADER_REQUEST_ID, HEADER_ROUTING_KEY};
    use uuid::Uuid;

    /// Engine priority headers pylon derives; the names stay out of the
    /// shared tunnel contract because only pylon speaks them.
    pub(crate) const HEADER_STATS_CORRELATION_ID: &str = "x-dynamo-stats-correlation-id";
    pub(crate) const HEADER_REQUEST_PRIORITY: &str = "x-dynamo-request-priority";
    pub(crate) const HEADER_REQUEST_STRICT_PRIORITY: &str = "x-dynamo-request-strict-priority";

    /// Denylist of engine headers pylon owns: inbound values are stripped in
    /// every backend mode so pylon stays their only writer.
    const STRIPPED_REQUEST_HEADERS: [&str; 5] = [
        "request-id",
        "x-dynamo-request-id",
        HEADER_STATS_CORRELATION_ID,
        HEADER_REQUEST_PRIORITY,
        HEADER_REQUEST_STRICT_PRIORITY,
    ];

    pub(crate) fn is_stripped_engine_header(name: &HeaderName) -> bool {
        STRIPPED_REQUEST_HEADERS.contains(&name.as_str())
    }

    /// Replace platform identity headers with an engine-local stats correlation ID.
    pub(crate) fn translate_stats_correlation(upstream_headers: &mut HeaderMap) -> String {
        for name in [HEADER_REQUEST_ID, HEADER_MODEL, HEADER_ROUTING_KEY] {
            upstream_headers.remove(name);
        }
        let correlation_id = Uuid::new_v4().to_string();
        upstream_headers.insert(
            HeaderName::from_static(HEADER_STATS_CORRELATION_ID),
            HeaderValue::from_str(&correlation_id).expect("UUID should be a valid header value"),
        );
        correlation_id
    }

    /// Map the platform rank (lower wins, absent = unconfigured) to the
    /// engine value (higher wins, read as seconds of queue head start):
    /// `max(0, ceiling - rank)`, with absent as the lowest value. The head
    /// start is bounded so prioritized traffic cannot starve the rest.
    pub(crate) fn request_priority(priority: Option<u32>, ceiling: u32) -> i32 {
        let ceiling = ceiling.min(i32::MAX as u32);
        let rank = priority.unwrap_or(ceiling).min(ceiling);
        (ceiling - rank) as i32
    }

    /// Emit both priority headers on every inference request.
    pub(crate) fn apply_priority_headers(
        priority: Option<u32>,
        ceiling: u32,
        upstream_headers: &mut HeaderMap,
    ) -> i32 {
        let dynamo_priority = request_priority(priority, ceiling);
        upstream_headers.insert(
            HeaderName::from_static(HEADER_REQUEST_PRIORITY),
            HeaderValue::from(dynamo_priority),
        );
        upstream_headers.insert(
            HeaderName::from_static(HEADER_REQUEST_STRICT_PRIORITY),
            HeaderValue::from_static("0"),
        );
        dynamo_priority
    }
}
