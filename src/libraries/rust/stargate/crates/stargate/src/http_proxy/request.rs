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

use axum::http::{HeaderMap, StatusCode};
use stargate_protocol::tunnel_contract::{
    HEADER_INPUT_TOKENS, HEADER_MODEL, HEADER_PRIORITY, HEADER_REQUEST_ID, HEADER_ROUTING_KEY,
};
use tracing::{Span, warn};

use crate::load_balancer::{
    LoadBalancerAlgorithmConfig, LoadBalancerAlgorithmOverride, LoadBalancerRoutingAlgorithmError,
};
use crate::routing_state::RoutingTargetKey;

use super::{
    HEADER_CACHE_AFFINITY_KEY, HEADER_MAX_WAIT_MS, HEADER_REQUEST_SLO_MS, HEADER_ROUTING_METHOD,
};

#[derive(Clone, Debug, PartialEq, Eq)]
pub(super) struct ProxyRequestInputs {
    pub(super) target: RoutingTargetKey,
    pub(super) input_tokens: u64,
    pub(super) priority: u32,
    pub(super) max_wait_ms: Option<u64>,
    pub(super) request_slo_ms: Option<u64>,
    pub(super) cache_affinity_key: Option<String>,
    pub(super) routing_algorithm_override: Option<LoadBalancerAlgorithmOverride>,
}

pub(super) fn parse_proxy_request_inputs(
    headers: &HeaderMap,
) -> Result<ProxyRequestInputs, StatusCode> {
    get_optional_header(headers, HEADER_REQUEST_ID).ok_or(StatusCode::BAD_REQUEST)?;
    let input_tokens = parse_optional_numeric_header(headers, HEADER_INPUT_TOKENS)?
        .ok_or(StatusCode::BAD_REQUEST)?;
    let target = RoutingTargetKey::new(
        get_optional_header(headers, HEADER_ROUTING_KEY),
        get_optional_header(headers, HEADER_MODEL).ok_or(StatusCode::BAD_REQUEST)?,
    );
    let routing_algorithm_override = parse_routing_algorithm_override(headers, &target)?;
    Ok(ProxyRequestInputs {
        target,
        input_tokens,
        priority: parse_optional_numeric_header(headers, HEADER_PRIORITY)?.unwrap_or(0),
        max_wait_ms: parse_optional_numeric_header(headers, HEADER_MAX_WAIT_MS)?,
        request_slo_ms: parse_optional_numeric_header(headers, HEADER_REQUEST_SLO_MS)?,
        cache_affinity_key: get_optional_header(headers, HEADER_CACHE_AFFINITY_KEY),
        routing_algorithm_override,
    })
}

fn parse_routing_algorithm_override(
    headers: &HeaderMap,
    target: &RoutingTargetKey,
) -> Result<Option<LoadBalancerAlgorithmOverride>, StatusCode> {
    let Some(value) = headers.get(HEADER_ROUTING_METHOD) else {
        return Ok(None);
    };
    let raw = value.to_str().map_err(|_| {
        reject_invalid_routing_algorithm(
            target,
            &LoadBalancerRoutingAlgorithmError::Unknown {
                raw: "<invalid-utf8>".to_string(),
            },
        )
    })?;
    raw.parse::<LoadBalancerAlgorithmOverride>()
        .map(Some)
        .map_err(|error| reject_invalid_routing_algorithm(target, &error))
}

pub(super) fn validate_load_balancer_request_requirements(
    lb_config: &LoadBalancerAlgorithmConfig,
    request_inputs: &ProxyRequestInputs,
) -> Result<(), StatusCode> {
    let target = &request_inputs.target;
    let model_id = target.model_id.as_str();
    if lb_config.requires_cache_affinity_key() && request_inputs.cache_affinity_key.is_none() {
        warn!(
            routing_key = ?target.routing_key,
            model_id = %model_id,
            "missing cache affinity key for load-balanced proxy request"
        );
        return Err(StatusCode::BAD_REQUEST);
    }
    Ok(())
}

pub(super) fn reject_invalid_routing_algorithm(
    target: &RoutingTargetKey,
    error: &LoadBalancerRoutingAlgorithmError,
) -> StatusCode {
    let requested_algorithm = error.requested_algorithm();
    Span::current().record("routing.requested_algorithm", requested_algorithm);
    Span::current().record("routing.invalid_requested_algorithm", requested_algorithm);
    warn!(
        routing_key = ?target.routing_key,
        model_id = %target.model_id,
        requested_algorithm = %requested_algorithm,
        rejection_reason = %error.reason(),
        "invalid routing algorithm header"
    );
    StatusCode::BAD_REQUEST
}

fn get_optional_header(headers: &HeaderMap, name: &'static str) -> Option<String> {
    headers
        .get(name)
        .and_then(|value| value.to_str().ok())
        .map(|value| value.trim())
        .filter(|value| !value.is_empty())
        .map(ToOwned::to_owned)
}

fn parse_optional_numeric_header<T>(
    headers: &HeaderMap,
    name: &'static str,
) -> Result<Option<T>, StatusCode>
where
    T: std::str::FromStr,
{
    let Some(value) = headers.get(name) else {
        return Ok(None);
    };
    let value = value.to_str().map_err(|_| StatusCode::BAD_REQUEST)?.trim();
    if value.is_empty() {
        return Err(StatusCode::BAD_REQUEST);
    }
    value
        .parse::<T>()
        .map(Some)
        .map_err(|_| StatusCode::BAD_REQUEST)
}

#[cfg(test)]
mod tests {
    use std::collections::HashMap;

    use axum::http::{HeaderName, HeaderValue};

    use super::*;
    use crate::load_balancer::{
        LoadBalancerAlgorithm, LoadBalancerConfig, LoadBalancerModelConfig, LoadBalancerRouter,
    };

    fn proxy_headers() -> HeaderMap {
        [
            (HEADER_REQUEST_ID, "req-test"),
            (HEADER_MODEL, "model-a"),
            (HEADER_INPUT_TOKENS, "128"),
        ]
        .into_iter()
        .map(|(name, value)| {
            (
                HeaderName::from_static(name),
                HeaderValue::from_static(value),
            )
        })
        .collect()
    }

    fn set_header(headers: &mut HeaderMap, name: &'static str, value: &'static str) {
        headers.insert(
            HeaderName::from_static(name),
            HeaderValue::from_static(value),
        );
    }

    #[test]
    fn optional_u64_proxy_headers_reject_invalid_values() {
        for header in [
            HEADER_INPUT_TOKENS,
            HEADER_MAX_WAIT_MS,
            HEADER_REQUEST_SLO_MS,
        ] {
            let mut headers = HeaderMap::new();
            set_header(&mut headers, header, "bad");

            assert_eq!(
                parse_optional_numeric_header::<u64>(&headers, header),
                Err(StatusCode::BAD_REQUEST)
            );
        }
    }

    #[test]
    fn optional_u32_proxy_headers_reject_invalid_values() {
        let mut headers = HeaderMap::new();
        set_header(&mut headers, HEADER_PRIORITY, "bad");

        assert_eq!(
            parse_optional_numeric_header::<u32>(&headers, HEADER_PRIORITY),
            Err(StatusCode::BAD_REQUEST)
        );
    }

    #[test]
    fn optional_numeric_proxy_headers_parse_valid_or_absent_values() {
        let mut headers = HeaderMap::new();
        set_header(&mut headers, HEADER_INPUT_TOKENS, "42");
        set_header(&mut headers, HEADER_PRIORITY, "7");

        assert_eq!(
            parse_optional_numeric_header::<u64>(&headers, HEADER_INPUT_TOKENS),
            Ok(Some(42))
        );
        assert_eq!(
            parse_optional_numeric_header::<u32>(&headers, HEADER_PRIORITY),
            Ok(Some(7))
        );
        assert_eq!(
            parse_optional_numeric_header::<u64>(&headers, HEADER_MAX_WAIT_MS),
            Ok(None)
        );
    }

    #[test]
    fn proxy_request_inputs_parse_routing_and_control_headers() {
        let mut headers = proxy_headers();
        for (name, value) in [
            (HEADER_ROUTING_KEY, "tenant-a"),
            (HEADER_PRIORITY, "4"),
            (HEADER_MAX_WAIT_MS, "250"),
            (HEADER_REQUEST_SLO_MS, "900"),
            (HEADER_CACHE_AFFINITY_KEY, "cache-key-a"),
            (HEADER_ROUTING_METHOD, "round_robin"),
        ] {
            set_header(&mut headers, name, value);
        }

        let inputs = parse_proxy_request_inputs(&headers).expect("headers should parse");

        assert_eq!(inputs.target.routing_key.as_deref(), Some("tenant-a"));
        assert_eq!(inputs.target.model_id, "model-a");
        assert_eq!(inputs.input_tokens, 128);
        assert_eq!(inputs.priority, 4);
        assert_eq!(inputs.max_wait_ms, Some(250));
        assert_eq!(inputs.request_slo_ms, Some(900));
        assert_eq!(inputs.cache_affinity_key.as_deref(), Some("cache-key-a"));
        assert!(inputs.routing_algorithm_override.is_some());
    }

    #[test]
    fn proxy_missing_routing_method_uses_configured_default_algorithm() {
        let lb_router = LoadBalancerRouter::from_config(&LoadBalancerConfig {
            default: LoadBalancerAlgorithm::RoundRobin,
            request_algorithms: HashMap::new(),
            models: HashMap::new(),
        })
        .expect("load balancer should initialize");
        let headers = proxy_headers();
        let inputs = parse_proxy_request_inputs(&headers).expect("headers should parse");

        let config = lb_router
            .resolve_algorithm_override(
                &inputs.target.model_id,
                inputs.routing_algorithm_override.as_ref(),
            )
            .expect("missing routing method should use configured default");

        assert_eq!(
            config.config().algorithm(),
            LoadBalancerAlgorithm::RoundRobin
        );
    }

    #[test]
    fn proxy_valid_configured_routing_method_uses_request_algorithm() {
        let lb_router = LoadBalancerRouter::from_config(&LoadBalancerConfig {
            default: LoadBalancerAlgorithm::PowerOfTwo,
            request_algorithms: HashMap::from([(
                LoadBalancerAlgorithm::RoundRobin,
                LoadBalancerModelConfig::Name(LoadBalancerAlgorithm::RoundRobin),
            )]),
            models: HashMap::new(),
        })
        .expect("load balancer should initialize");
        let mut headers = proxy_headers();
        set_header(&mut headers, HEADER_ROUTING_METHOD, "round_robin");
        let inputs = parse_proxy_request_inputs(&headers).expect("headers should parse");

        let config = lb_router
            .resolve_algorithm_override(
                &inputs.target.model_id,
                inputs.routing_algorithm_override.as_ref(),
            )
            .expect("configured routing method should be available");

        assert_eq!(
            config.config().algorithm(),
            LoadBalancerAlgorithm::RoundRobin
        );
    }

    #[test]
    fn proxy_unknown_routing_method_returns_bad_request() {
        let mut headers = proxy_headers();
        set_header(&mut headers, HEADER_ROUTING_METHOD, "sticky");

        assert_eq!(
            parse_proxy_request_inputs(&headers),
            Err(StatusCode::BAD_REQUEST)
        );
    }

    #[test]
    fn proxy_blank_routing_method_returns_bad_request() {
        let mut headers = proxy_headers();
        set_header(&mut headers, HEADER_ROUTING_METHOD, "   ");

        assert_eq!(
            parse_proxy_request_inputs(&headers),
            Err(StatusCode::BAD_REQUEST)
        );
    }

    #[test]
    fn proxy_known_unconfigured_routing_method_returns_bad_request() {
        let lb_router = LoadBalancerRouter::from_config(&LoadBalancerConfig::default())
            .expect("load balancer should initialize");
        let mut headers = proxy_headers();
        set_header(&mut headers, HEADER_ROUTING_METHOD, "round-robin");
        let inputs = parse_proxy_request_inputs(&headers).expect("headers should parse");
        let error = lb_router
            .resolve_algorithm_override(
                &inputs.target.model_id,
                inputs.routing_algorithm_override.as_ref(),
            )
            .expect_err("unconfigured routing method should fail");

        assert_eq!(
            reject_invalid_routing_algorithm(&inputs.target, &error),
            StatusCode::BAD_REQUEST
        );
        assert_eq!(error.reason(), "unavailable");
    }

    #[test]
    fn proxy_request_inputs_reject_missing_required_headers() {
        for missing_header in [HEADER_MODEL, HEADER_REQUEST_ID, HEADER_INPUT_TOKENS] {
            let mut headers = proxy_headers();
            headers.remove(missing_header);

            assert_eq!(
                parse_proxy_request_inputs(&headers),
                Err(StatusCode::BAD_REQUEST),
                "missing {missing_header} should be rejected"
            );
        }
    }

    #[test]
    fn load_balancer_request_requirements_reject_missing_cache_affinity_key() {
        let headers = proxy_headers();
        let inputs = parse_proxy_request_inputs(&headers).expect("headers should parse");
        let mut config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::Pulsar);
        config.request_policy_mut().require_cache_affinity_key = true;

        assert_eq!(
            validate_load_balancer_request_requirements(&config, &inputs),
            Err(StatusCode::BAD_REQUEST)
        );
    }

    #[test]
    fn load_balancer_request_requirements_accept_satisfied_controls() {
        let mut headers = proxy_headers();
        set_header(&mut headers, HEADER_CACHE_AFFINITY_KEY, "cache-key-a");
        let inputs = parse_proxy_request_inputs(&headers).expect("headers should parse");
        let mut config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::Pulsar);
        config.request_policy_mut().require_cache_affinity_key = true;
        config.request_policy_mut().require_input_tokens = true;

        assert_eq!(
            validate_load_balancer_request_requirements(&config, &inputs),
            Ok(())
        );
    }
}
