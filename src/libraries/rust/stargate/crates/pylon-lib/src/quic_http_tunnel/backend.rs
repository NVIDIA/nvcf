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
//! Pylon presents one contract upward (the platform tunnel headers, notably
//! `x-priority`) and translates it into the dialect of the engine it fronts
//! at the last hop. The gateway and Stargate stay backend-agnostic; all
//! engine-specific names and encodings live in this module.

use std::fmt;
use std::str::FromStr;

/// Which engine dialect pylon speaks to its local upstream.
///
/// One enum rather than per-backend flags: future engines add a variant and
/// a submodule here, never a new CLI flag.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum UpstreamBackend {
    /// Forward requests unchanged. No engine priority headers are derived;
    /// inbound engine-control headers are still stripped.
    Passthrough,
    /// Dynamo dialect: derive the engine priority headers from `x-priority`.
    #[default]
    Dynamo,
}

impl UpstreamBackend {
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Passthrough => "passthrough",
            Self::Dynamo => "dynamo",
        }
    }
}

impl fmt::Display for UpstreamBackend {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
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

/// Default seconds of scheduling head start for the most urgent platform
/// priority (`x-priority: 0`); see [`dynamo::request_priority`].
pub const DEFAULT_PRIORITY_CEILING: u32 = 3600;

pub(crate) mod dynamo {
    use reqwest::header::{HeaderMap, HeaderName, HeaderValue};

    /// Engine-facing priority headers in the Dynamo contract. Pylon owns
    /// this contract, so the names stay out of the shared tunnel contract.
    pub(crate) const HEADER_REQUEST_PRIORITY: &str = "x-dynamo-request-priority";
    pub(crate) const HEADER_REQUEST_STRICT_PRIORITY: &str = "x-dynamo-request-strict-priority";
    const REQUEST_HEADER_PREFIX: &str = "x-dynamo-request-";

    /// Inbound headers under the Dynamo request-priority prefix are always
    /// stripped, in every backend mode: pylon is the only writer of these
    /// values, so a client cannot set engine priority through them.
    /// Dynamo's non-priority routing headers (worker pinning, tenant cache
    /// salt) are outside this prefix and tracked separately.
    pub(crate) fn is_engine_priority_header(name: &HeaderName) -> bool {
        name.as_str().starts_with(REQUEST_HEADER_PREFIX)
    }

    /// Map platform priority to Dynamo request priority.
    ///
    /// Dynamo reads the value as seconds of arrival-time head start in its
    /// router queue (higher wins, i32), while `x-priority` is a rank (lower
    /// wins, u32, absent = unconfigured). The mapping is
    /// `max(0, ceiling - x)`, with absent treated as the lowest priority:
    /// a bounded head start that queue aging can overcome, rather than a
    /// permanent tier above unconfigured traffic.
    pub(crate) fn request_priority(priority: Option<u32>, ceiling: u32) -> i32 {
        let ceiling = ceiling.min(i32::MAX as u32);
        let rank = priority.unwrap_or(ceiling).min(ceiling);
        (ceiling - rank) as i32
    }

    /// Emit both Dynamo priority headers on every inference request.
    ///
    /// Dynamo resolves each priority field from the header when present and
    /// well-formed, falling back to the client-controlled request body
    /// (`nvext.agent_hints`) otherwise. Always emitting both headers makes
    /// the platform the only source of engine priority: requests without a
    /// platform priority carry the lowest value instead of leaving the body
    /// fallback open, and the strict tier is pinned to the default.
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
