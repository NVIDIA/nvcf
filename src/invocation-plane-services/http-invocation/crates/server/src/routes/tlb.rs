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

use crate::nats::NatsService;
use crate::nvcf_api::NVCFService;
use crate::rate_limit::RateLimitService;
use crate::routes::app_error::AppError;
use crate::routes::input_asset_header::InputAssetHeader;
use crate::routes::pexec;
use crate::routes::post_pexec::FunctionRouting;
use crate::s3::S3Service;
use crate::settings::AppConfig;
use crate::worker_streams::WorkerStreamService;
use axum::extract::{Path, Request, State};
use axum::response::Response;
use axum::Extension;
use axum_extra::TypedHeader;
use headers::authorization::Bearer;
use headers::Authorization;
use http::{HeaderMap, HeaderValue};
use std::sync::Arc;
use uuid::Uuid;

#[derive(Copy, Clone, Default, Debug, Eq, PartialEq)]
pub struct FunctionId(pub Uuid);
#[derive(Copy, Clone, Default, Debug, Eq, PartialEq)]
pub struct FunctionVersionId(pub Uuid);

#[allow(clippy::too_many_arguments)]
pub async fn tlb_handler(
    State(nvcf_service): State<Arc<NVCFService>>,
    State(nats_service): State<Arc<NatsService>>,
    State(s3_service): State<Arc<S3Service>>,
    State(rate_limit_service): State<Arc<RateLimitService>>,
    State(worker_stream_service): State<Arc<WorkerStreamService>>,
    State(app_config): State<Arc<AppConfig>>,
    Extension(Authorization(bearer)): Extension<Authorization<Bearer>>,
    Extension(FunctionId(function_id)): Extension<FunctionId>,
    function_version_id: Option<Extension<FunctionVersionId>>,
    req: Request,
) -> Result<Response, AppError> {
    pexec(
        Path(FunctionRouting {
            function_id,
            function_version_id: function_version_id.map(|fv| fv.0 .0),
        }),
        State(nvcf_service),
        State(nats_service),
        State(s3_service),
        State(rate_limit_service),
        State(worker_stream_service),
        State(app_config),
        Extension(Authorization(bearer)),
        TypedHeader(InputAssetHeader(Vec::default())),
        req,
    )
    .await
}

pub fn split_hostname(hostname: &str) -> Option<(Option<FunctionId>, Option<FunctionVersionId>)> {
    const ID_LEN: usize = "00000000-0000-0000-0000-000000000000".len();
    if let Some((first_id, remainder)) = hostname.split_at_checked(ID_LEN) {
        if let Ok(first_id) = Uuid::try_parse(first_id) {
            // first id is good, try checking for a second id
            if let Some((second_id, _)) = remainder.split_at_checked(ID_LEN + 1) {
                // second_id will hold a dot at the start
                let (_, second_id) = second_id.split_at(1); // strip off first char
                if let Ok(second_id) = Uuid::try_parse(second_id) {
                    return Some((
                        Some(FunctionId(second_id)),
                        Some(FunctionVersionId(first_id)),
                    ));
                }
            }
            return Some((Some(FunctionId(first_id)), None));
        }
    }
    None
}

pub fn function_id_headers(
    headers: &HeaderMap<HeaderValue>,
) -> Option<(Option<FunctionId>, Option<FunctionVersionId>)> {
    if let Some(function_id) = headers.get("function-id") {
        if let Ok(function_id) = Uuid::try_parse_ascii(function_id.as_bytes()) {
            if let Some(function_version_id) = headers.get("function-version-id") {
                if let Ok(function_version_id) =
                    Uuid::try_parse_ascii(function_version_id.as_bytes())
                {
                    return Some((
                        Some(FunctionId(function_id)),
                        Some(FunctionVersionId(function_version_id)),
                    ));
                }
            }
            return Some((Some(FunctionId(function_id)), None));
        }
    }
    None
}

#[cfg(test)]
mod test {
    use super::*;
    use uuid::uuid;

    #[test]
    fn test_split_hostname_no_ids() {
        // Test with no IDs in hostname
        let hostname = "example.com";
        assert!(split_hostname(hostname).is_none());
    }

    #[test]
    fn test_split_hostname_invalid_id() {
        // Test with invalid ID in hostname
        let hostname = "invalid-id.example.com";
        assert!(split_hostname(hostname).is_none());
    }

    #[test]
    fn test_split_hostname_invalid_second_id() {
        // Test with invalid ID at start of hostname
        let uuid_str = uuid!("00000000-0000-0000-0000-000000000000");
        let hostname = format!("invalid-id.{uuid_str}.example.com");
        assert!(split_hostname(&hostname).is_none());
    }

    #[test]
    fn test_split_hostname_single_id() {
        // Test with single valid ID in hostname
        let uuid_str = uuid!("00000000-0000-0000-0000-000000000000");
        let hostname = format!("{uuid_str}.example.com");
        let expected_id = FunctionId(uuid_str);
        assert_eq!(split_hostname(&hostname), Some((Some(expected_id), None)));
    }

    #[test]
    fn test_split_hostname_double_id() {
        // Test with two valid IDs in hostname
        let uuid_str1 = uuid!("00000000-0000-0000-0000-000000000000");
        let uuid_str2 = uuid!("11111111-1111-1111-1111-111111111111");
        let hostname = format!("{uuid_str1}.{uuid_str2}.example.com");
        let expected_func_id = FunctionId(uuid_str2);
        let expected_version_id = FunctionVersionId(uuid_str1);
        assert_eq!(
            split_hostname(&hostname),
            Some((Some(expected_func_id), Some(expected_version_id)))
        );
    }
}
