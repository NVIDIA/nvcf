/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

//! Shared GET /info response schema for the Rust services, matching what the
//! Go services serve from `src/libraries/go/lib/pkg/version` and the Java
//! services serve from `nv-boot-starter-core`.

use serde::Serialize;

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct InfoResponse {
    pub service: &'static str,
    pub version: &'static str,
    pub commit: &'static str,
}

/// Builds an [`InfoResponse`] for the named service.
///
/// A macro rather than a function so `env!` expands in the calling crate and
/// reads that crate's stamped values instead of this one's. The service name
/// is passed in because under Bazel `CARGO_PKG_NAME` resolves to the target
/// name, not the package name.
#[macro_export]
macro_rules! info_response {
    ($service:expr) => {
        $crate::InfoResponse {
            service: $service,
            version: env!("CARGO_PKG_VERSION"),
            commit: option_env!("NVCF_GIT_COMMIT").unwrap_or("unknown"),
        }
    };
}

#[cfg(test)]
mod tests {
    #[test]
    fn info_response_uses_the_given_service_name_and_this_crate_version() {
        let info = info_response!("test-service");
        assert_eq!(info.service, "test-service");
        assert_eq!(info.version, env!("CARGO_PKG_VERSION"));
        assert_eq!(info.commit, "unknown");
    }

    #[test]
    fn info_response_serializes_to_the_shared_json_shape() {
        let info = info_response!("test-service");
        let json = serde_json::to_value(&info).expect("serializes");
        assert_eq!(json["service"], "test-service");
        assert!(json["version"].is_string());
        assert!(json["commit"].is_string());
    }
}
