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

pub const HEADER_REQUEST_ID: &str = "x-request-id";
pub const HEADER_MODEL: &str = "x-model";
pub const HEADER_ROUTING_KEY: &str = "x-routing-key";
pub const HEADER_INPUT_TOKENS: &str = "x-input-tokens";
pub const HEADER_PRIORITY: &str = "x-priority";

pub const HEADER_STARGATE_EXPECTED_QUEUE_MS: &str = "x-stargate-expected-queue-ms";
pub const HEADER_STARGATE_UPSTREAM_RETRYABLE: &str = "x-stargate-upstream-retryable";
pub const HEADER_STARGATE_RETRYABLE: &str = "x-stargate-retryable";
pub const HEADER_STARGATE_RETRY_REASON: &str = "x-stargate-retry-reason";
pub const HEADER_STARGATE_RETRY_AFTER_MS: &str = "x-stargate-retry-after-ms";

pub const HEADER_INFERENCE_SERVER_ID: &str = "x-inference-server-id";
pub const HEADER_REVERSE_AUTH_TOKEN: &str = "x-stargate-auth-token";

pub const WEBTRANSPORT_TUNNEL_PATH: &str = "/_stargate/webtransport";

pub const REQUIRED_REQUEST_HEADERS: [&str; 3] =
    [HEADER_REQUEST_ID, HEADER_MODEL, HEADER_INPUT_TOKENS];

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn required_tunnel_headers_are_canonical_lowercase_http_names() {
        assert_eq!(
            REQUIRED_REQUEST_HEADERS,
            ["x-request-id", "x-model", "x-input-tokens"]
        );
        assert_eq!(HEADER_ROUTING_KEY, "x-routing-key");
        assert_eq!(HEADER_PRIORITY, "x-priority");
    }

    #[test]
    fn retry_and_reverse_tunnel_contract_names_are_owned_here() {
        assert_eq!(
            HEADER_STARGATE_EXPECTED_QUEUE_MS,
            "x-stargate-expected-queue-ms"
        );
        assert_eq!(
            HEADER_STARGATE_UPSTREAM_RETRYABLE,
            "x-stargate-upstream-retryable"
        );
        assert_eq!(HEADER_STARGATE_RETRYABLE, "x-stargate-retryable");
        assert_eq!(HEADER_STARGATE_RETRY_REASON, "x-stargate-retry-reason");
        assert_eq!(HEADER_STARGATE_RETRY_AFTER_MS, "x-stargate-retry-after-ms");
        assert_eq!(HEADER_INFERENCE_SERVER_ID, "x-inference-server-id");
        assert_eq!(HEADER_REVERSE_AUTH_TOKEN, "x-stargate-auth-token");
        assert_eq!(WEBTRANSPORT_TUNNEL_PATH, "/_stargate/webtransport");
    }
}
