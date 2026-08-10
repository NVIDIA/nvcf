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

use std::time::{Duration, Instant};

use axum::http::{HeaderMap, HeaderName, StatusCode};
use stargate_protocol::tunnel_contract::{HEADER_STARGATE_RETRY_REASON, HEADER_STARGATE_RETRYABLE};

mod replay;

pub(super) use replay::{ReplayReadiness, ReplayableRequestBody};

const RETRY_REASON_QUEUE_ESTIMATE_MISMATCH: &str = "queue_estimate_mismatch";
const RETRY_REASON_RETRYABLE_PROXY_ERROR: &str = "retryable_proxy_error";
const DEFAULT_RETRY_BUDGET_MS_HEADER: &str = "x-stargate-max-wait-ms";
const DEFAULT_MAX_REPLAY_BODY_BYTES: usize = 64 * 1024 * 1024;

#[derive(Clone, Debug)]
pub struct ProxyRetryConfig {
    pub max_connect_retries: u32,
    pub max_request_retries: u32,
    pub max_replay_body_bytes: usize,
    pub retryable_status_codes: Vec<StatusCode>,
    pub require_pylon_retry_signal: bool,
    pub request_retry_budget_ms_header: Option<HeaderName>,
}

impl Default for ProxyRetryConfig {
    fn default() -> Self {
        Self {
            max_connect_retries: 2,
            max_request_retries: 2,
            max_replay_body_bytes: DEFAULT_MAX_REPLAY_BODY_BYTES,
            retryable_status_codes: vec![
                StatusCode::TOO_MANY_REQUESTS,
                StatusCode::SERVICE_UNAVAILABLE,
            ],
            require_pylon_retry_signal: true,
            request_retry_budget_ms_header: Some(HeaderName::from_static(
                DEFAULT_RETRY_BUDGET_MS_HEADER,
            )),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(super) enum RetryDecision<T> {
    Final(FinalRetryDisposition),
    Retry(T),
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(super) enum FinalRetryDisposition {
    PassThrough,
    Exhausted(String),
    ReplayIncomplete(String),
    PayloadTooLarge(Option<String>),
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(super) enum UpstreamRetry {
    AlternateBackend(String),
    AlternateCluster(String),
}

pub(super) fn decide_upstream_response_retry(
    status: StatusCode,
    headers: &HeaderMap,
    retry: &ProxyRetryConfig,
    retry_budget_remaining: bool,
    request_retries: u32,
    replay_readiness: ReplayReadiness,
) -> RetryDecision<UpstreamRetry> {
    if !should_retry_upstream_response(status, headers, retry) {
        return RetryDecision::Final(FinalRetryDisposition::PassThrough);
    }

    let retry_reason = retry_reason_from_headers(headers);
    if !retry_budget_remaining {
        return retry_exhausted("retry_budget_exhausted");
    }
    if request_retries >= retry.max_request_retries {
        return retry_exhausted(retry_reason);
    }

    match replay_readiness {
        ReplayReadiness::Ready if retry_reason == RETRY_REASON_QUEUE_ESTIMATE_MISMATCH => {
            RetryDecision::Retry(UpstreamRetry::AlternateBackend(retry_reason))
        }
        ReplayReadiness::Ready => {
            RetryDecision::Retry(UpstreamRetry::AlternateCluster(retry_reason))
        }
        ReplayReadiness::Incomplete => {
            RetryDecision::Final(FinalRetryDisposition::ReplayIncomplete(retry_reason))
        }
        ReplayReadiness::PayloadTooLarge => {
            RetryDecision::Final(FinalRetryDisposition::PayloadTooLarge(Some(retry_reason)))
        }
    }
}

pub(super) fn decide_proxy_error_retry(
    status: StatusCode,
    retry: &ProxyRetryConfig,
    retry_budget_remaining: bool,
    connect_retries: u32,
    replay_readiness: ReplayReadiness,
) -> RetryDecision<()> {
    if !matches!(
        status,
        StatusCode::BAD_GATEWAY | StatusCode::GATEWAY_TIMEOUT | StatusCode::SERVICE_UNAVAILABLE
    ) {
        return RetryDecision::Final(FinalRetryDisposition::PassThrough);
    }
    if !retry_budget_remaining {
        return retry_exhausted("retry_budget_exhausted");
    }
    if connect_retries >= retry.max_connect_retries {
        return retry_exhausted("connect_retries_exhausted");
    }

    match replay_readiness {
        ReplayReadiness::Ready => RetryDecision::Retry(()),
        ReplayReadiness::Incomplete => RetryDecision::Final(
            FinalRetryDisposition::ReplayIncomplete(RETRY_REASON_RETRYABLE_PROXY_ERROR.to_string()),
        ),
        ReplayReadiness::PayloadTooLarge => {
            RetryDecision::Final(FinalRetryDisposition::PayloadTooLarge(None))
        }
    }
}

fn should_retry_upstream_response(
    status: StatusCode,
    headers: &HeaderMap,
    retry: &ProxyRetryConfig,
) -> bool {
    retry.retryable_status_codes.contains(&status)
        && match header_str(headers, HEADER_STARGATE_RETRYABLE) {
            Some(retryable) => retryable.eq_ignore_ascii_case("true"),
            None => !retry.require_pylon_retry_signal,
        }
}

pub(super) fn should_release_queue_mismatch_reservation(
    status: StatusCode,
    headers: &HeaderMap,
) -> bool {
    status == StatusCode::TOO_MANY_REQUESTS
        && header_str(headers, HEADER_STARGATE_RETRYABLE)
            .is_some_and(|value| value.eq_ignore_ascii_case("true"))
        && header_str(headers, HEADER_STARGATE_RETRY_REASON)
            == Some(RETRY_REASON_QUEUE_ESTIMATE_MISMATCH)
}

pub(super) fn retry_budget_deadline(
    headers: &HeaderMap,
    retry: &ProxyRetryConfig,
    request_start: Instant,
) -> Result<Option<Instant>, StatusCode> {
    let Some(header_value) = retry
        .request_retry_budget_ms_header
        .as_ref()
        .and_then(|header_name| headers.get(header_name))
    else {
        return Ok(None);
    };
    let budget_ms = header_value
        .to_str()
        .map_err(|_| StatusCode::BAD_REQUEST)?
        .trim()
        .parse::<u64>()
        .map_err(|_| StatusCode::BAD_REQUEST)?;
    request_start
        .checked_add(Duration::from_millis(budget_ms))
        .map(Some)
        .ok_or(StatusCode::BAD_REQUEST)
}

pub(super) fn retry_budget_has_remaining(deadline: Option<Instant>) -> bool {
    deadline.is_none_or(|deadline| Instant::now() < deadline)
}

fn retry_reason_from_headers(headers: &HeaderMap) -> String {
    header_str(headers, HEADER_STARGATE_RETRY_REASON)
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .unwrap_or("retryable_upstream_response")
        .to_owned()
}

fn header_str<'a>(headers: &'a HeaderMap, name: &'static str) -> Option<&'a str> {
    headers.get(name)?.to_str().ok()
}

fn retry_exhausted<T>(reason: impl Into<String>) -> RetryDecision<T> {
    RetryDecision::Final(FinalRetryDisposition::Exhausted(reason.into()))
}

#[cfg(test)]
mod tests {
    use super::*;

    use axum::http::HeaderValue;

    fn retry_headers(retryable: &'static str, reason: Option<&'static str>) -> HeaderMap {
        let mut headers = HeaderMap::new();
        headers.insert(
            HEADER_STARGATE_RETRYABLE,
            HeaderValue::from_static(retryable),
        );
        if let Some(reason) = reason {
            headers.insert(
                HEADER_STARGATE_RETRY_REASON,
                HeaderValue::from_static(reason),
            );
        }
        headers
    }

    fn budget_headers(value: &'static str) -> HeaderMap {
        [(
            HeaderName::from_static(DEFAULT_RETRY_BUDGET_MS_HEADER),
            HeaderValue::from_static(value),
        )]
        .into_iter()
        .collect()
    }

    #[test]
    fn retry_requires_explicit_pylon_signal_by_default() {
        let retry = ProxyRetryConfig::default();
        let bare_headers = HeaderMap::new();
        assert!(!should_retry_upstream_response(
            StatusCode::TOO_MANY_REQUESTS,
            &bare_headers,
            &retry
        ));

        let retryable_headers = retry_headers("true", None);
        assert!(should_retry_upstream_response(
            StatusCode::TOO_MANY_REQUESTS,
            &retryable_headers,
            &retry
        ));
    }

    #[test]
    fn retry_signal_is_ignored_for_non_retryable_status() {
        let retry = ProxyRetryConfig::default();
        let headers = retry_headers("true", None);

        assert!(!should_retry_upstream_response(
            StatusCode::BAD_REQUEST,
            &headers,
            &retry
        ));
    }

    #[test]
    fn only_explicit_queue_mismatch_rejection_releases_optimistic_reservation() {
        let mut headers = retry_headers("true", Some(RETRY_REASON_QUEUE_ESTIMATE_MISMATCH));

        assert!(should_release_queue_mismatch_reservation(
            StatusCode::TOO_MANY_REQUESTS,
            &headers
        ));
        assert!(!should_release_queue_mismatch_reservation(
            StatusCode::SERVICE_UNAVAILABLE,
            &headers
        ));

        headers.insert(
            HEADER_STARGATE_RETRY_REASON,
            HeaderValue::from_static("upstream_admission_rejected"),
        );
        assert!(!should_release_queue_mismatch_reservation(
            StatusCode::TOO_MANY_REQUESTS,
            &headers
        ));
    }

    #[test]
    fn explicit_non_retryable_signal_blocks_status_only_retry() {
        let retry = ProxyRetryConfig {
            require_pylon_retry_signal: false,
            ..ProxyRetryConfig::default()
        };
        let headers = retry_headers("false", None);

        assert!(!should_retry_upstream_response(
            StatusCode::SERVICE_UNAVAILABLE,
            &headers,
            &retry
        ));
    }

    #[test]
    fn upstream_response_retry_decision_retries_when_budget_limit_and_replay_allow() {
        let retry = ProxyRetryConfig::default();
        let headers = retry_headers("true", Some("upstream_overloaded"));

        assert_eq!(
            decide_upstream_response_retry(
                StatusCode::SERVICE_UNAVAILABLE,
                &headers,
                &retry,
                true,
                0,
                ReplayReadiness::Ready,
            ),
            RetryDecision::Retry(UpstreamRetry::AlternateCluster(
                "upstream_overloaded".to_string()
            ))
        );
    }

    #[test]
    fn queue_mismatch_retry_decision_retries_a_sibling_before_excluding_the_cluster() {
        let retry = ProxyRetryConfig::default();
        let headers = retry_headers("true", Some(RETRY_REASON_QUEUE_ESTIMATE_MISMATCH));

        assert_eq!(
            decide_upstream_response_retry(
                StatusCode::TOO_MANY_REQUESTS,
                &headers,
                &retry,
                true,
                0,
                ReplayReadiness::Ready,
            ),
            RetryDecision::Retry(UpstreamRetry::AlternateBackend(
                RETRY_REASON_QUEUE_ESTIMATE_MISMATCH.to_string()
            ))
        );
    }

    #[test]
    fn upstream_response_retry_decision_preserves_exhaustion_precedence() {
        let retry = ProxyRetryConfig::default();
        let headers = retry_headers("true", Some("upstream_overloaded"));

        assert_eq!(
            decide_upstream_response_retry(
                StatusCode::SERVICE_UNAVAILABLE,
                &headers,
                &retry,
                false,
                0,
                ReplayReadiness::Ready,
            ),
            RetryDecision::Final(FinalRetryDisposition::Exhausted(
                "retry_budget_exhausted".to_string()
            ))
        );
        assert_eq!(
            decide_upstream_response_retry(
                StatusCode::SERVICE_UNAVAILABLE,
                &headers,
                &retry,
                true,
                retry.max_request_retries,
                ReplayReadiness::PayloadTooLarge,
            ),
            RetryDecision::Final(FinalRetryDisposition::Exhausted(
                "upstream_overloaded".to_string()
            ))
        );
    }

    #[test]
    fn proxy_error_retry_decision_retries_when_budget_limit_and_replay_allow() {
        let retry = ProxyRetryConfig::default();

        assert_eq!(
            decide_proxy_error_retry(
                StatusCode::SERVICE_UNAVAILABLE,
                &retry,
                true,
                0,
                ReplayReadiness::Ready,
            ),
            RetryDecision::Retry(())
        );
    }

    #[test]
    fn proxy_error_retry_decision_preserves_exhaustion_and_status_precedence() {
        let retry = ProxyRetryConfig::default();

        assert_eq!(
            decide_proxy_error_retry(
                StatusCode::SERVICE_UNAVAILABLE,
                &retry,
                false,
                0,
                ReplayReadiness::Ready,
            ),
            RetryDecision::Final(FinalRetryDisposition::Exhausted(
                "retry_budget_exhausted".to_string()
            ))
        );
        assert_eq!(
            decide_proxy_error_retry(
                StatusCode::SERVICE_UNAVAILABLE,
                &retry,
                true,
                retry.max_connect_retries,
                ReplayReadiness::PayloadTooLarge,
            ),
            RetryDecision::Final(FinalRetryDisposition::Exhausted(
                "connect_retries_exhausted".to_string()
            ))
        );
        assert_eq!(
            decide_proxy_error_retry(
                StatusCode::BAD_REQUEST,
                &retry,
                true,
                0,
                ReplayReadiness::PayloadTooLarge,
            ),
            RetryDecision::Final(FinalRetryDisposition::PassThrough)
        );
    }

    #[test]
    fn proxy_error_retry_decision_reports_replay_incomplete_reason() {
        let retry = ProxyRetryConfig::default();

        assert_eq!(
            decide_proxy_error_retry(
                StatusCode::BAD_GATEWAY,
                &retry,
                true,
                0,
                ReplayReadiness::Incomplete,
            ),
            RetryDecision::Final(FinalRetryDisposition::ReplayIncomplete(
                "retryable_proxy_error".to_string()
            ))
        );
    }

    #[test]
    fn retry_budget_header_zero_blocks_retry() {
        let retry = ProxyRetryConfig::default();
        let headers = budget_headers("0");

        let deadline = retry_budget_deadline(&headers, &retry, Instant::now()).unwrap();
        assert!(!retry_budget_has_remaining(deadline));
    }

    #[test]
    fn retry_budget_header_absent_allows_retry() {
        let retry = ProxyRetryConfig::default();
        let headers = HeaderMap::new();

        let deadline = retry_budget_deadline(&headers, &retry, Instant::now()).unwrap();
        assert!(retry_budget_has_remaining(deadline));
    }

    #[test]
    fn retry_budget_header_rejects_invalid_values() {
        let retry = ProxyRetryConfig::default();
        let headers = budget_headers("not-a-number");

        assert_eq!(
            retry_budget_deadline(&headers, &retry, Instant::now()),
            Err(StatusCode::BAD_REQUEST)
        );
    }
}
