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

use reqwest::header::HeaderMap;
use stargate_protocol::tunnel_contract::{
    HEADER_INPUT_TOKENS, HEADER_MODEL, HEADER_PRIORITY, HEADER_REQUEST_ID, HEADER_ROUTING_KEY,
};

#[derive(Debug)]
pub(crate) struct MissingRequiredHeaderError {
    pub header_name: &'static str,
    pub kind: RequiredHeaderErrorKind,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum RequiredHeaderErrorKind {
    Missing,
    Invalid,
}

impl MissingRequiredHeaderError {
    fn new(header_name: &'static str) -> Self {
        Self {
            header_name,
            kind: RequiredHeaderErrorKind::Missing,
        }
    }

    fn invalid(header_name: &'static str) -> Self {
        Self {
            header_name,
            kind: RequiredHeaderErrorKind::Invalid,
        }
    }

    pub(crate) fn message(&self) -> String {
        match self.kind {
            RequiredHeaderErrorKind::Missing => {
                format!("missing required {} header", self.header_name)
            }
            RequiredHeaderErrorKind::Invalid => format!("invalid {} header", self.header_name),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct RequiredTunnelHeaders {
    pub request_id: String,
    pub routing_key: Option<String>,
    pub model_id: String,
    pub priority: u32,
    pub input_tokens: u64,
    pub(crate) accepted_at: Instant,
}

pub(crate) fn validate_required_tunnel_headers(
    request_headers: &HeaderMap,
) -> Result<RequiredTunnelHeaders, MissingRequiredHeaderError> {
    let request_id = get_optional_header(request_headers, HEADER_REQUEST_ID)
        .ok_or_else(|| MissingRequiredHeaderError::new(HEADER_REQUEST_ID))?;
    let routing_key = get_optional_header(request_headers, HEADER_ROUTING_KEY);
    let model_id = get_optional_header(request_headers, HEADER_MODEL)
        .ok_or_else(|| MissingRequiredHeaderError::new(HEADER_MODEL))?;
    let input_tokens = parse_optional_numeric_header(request_headers, HEADER_INPUT_TOKENS)?
        .ok_or_else(|| MissingRequiredHeaderError::new(HEADER_INPUT_TOKENS))?;
    let priority =
        parse_optional_numeric_header(request_headers, HEADER_PRIORITY)?.unwrap_or_default();
    Ok(RequiredTunnelHeaders {
        request_id,
        routing_key,
        model_id,
        priority,
        input_tokens,
        accepted_at: Instant::now(),
    })
}

fn parse_optional_numeric_header<T>(
    headers: &HeaderMap,
    name: &'static str,
) -> Result<Option<T>, MissingRequiredHeaderError>
where
    T: std::str::FromStr,
{
    let Some(value) = headers.get(name) else {
        return Ok(None);
    };
    let value = value
        .to_str()
        .map_err(|_| MissingRequiredHeaderError::invalid(name))?
        .trim();
    if value.is_empty() {
        return Err(MissingRequiredHeaderError::invalid(name));
    }
    value
        .parse()
        .map(Some)
        .map_err(|_| MissingRequiredHeaderError::invalid(name))
}

fn get_optional_header(headers: &HeaderMap, name: &'static str) -> Option<String> {
    headers
        .get(name)
        .and_then(|value| value.to_str().ok())
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(ToOwned::to_owned)
}
