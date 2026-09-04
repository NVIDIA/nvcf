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

//! Shared GET /info response schema for NVCF's Rust services, matching the
//! service/version/commit shape the Go services serve from
//! src/libraries/go/lib/pkg/version and the Java services serve from
//! nv-boot-starter-core.
//!
//! Rust has no equivalent to Go's linker `-X` stamping of an imported
//! package's variables, so this crate cannot read a caller's
//! CARGO_PKG_VERSION/NVCF_GIT_COMMIT itself: `env!`/`option_env!` resolve
//! against the environment of whichever crate's compilation they textually
//! appear in. [`info_response!`] is a macro, not a function, so that expansion
//! happens inside the calling crate and reads that crate's own Bazel-stamped
//! values (see each service's `crates/server/BUILD.bazel` `version_env`
//! `expand_template`).

use serde::Serialize;

/// Response body for GET /info. Field names and JSON shape match the Go
/// `version.Info` struct so a single client can parse every NVCF service.
#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct InfoResponse {
    pub service: &'static str,
    pub version: &'static str,
    pub commit: &'static str,
}

/// Builds an [`InfoResponse`] for the named service from the calling crate's
/// own `CARGO_PKG_VERSION` and `NVCF_GIT_COMMIT`.
///
/// The service name is passed in rather than derived from `CARGO_PKG_NAME`
/// for two reasons. It matches how the Go services set `version.Service`, an
/// explicit deployed service name in each `BUILD.bazel` `x_defs` (for example
/// `nvcf-grpc-proxy`), rather than the Go package name. And under Bazel,
/// `CARGO_PKG_NAME` resolves to the rules_rust target name, not the Cargo
/// package name, so deriving it would report an internal build label such as
/// `rs_autoscaler_lib`.
///
/// `commit` falls back to `"unknown"` when `NVCF_GIT_COMMIT` is unset, which
/// is the case under plain `cargo build`/`cargo test` rather than a Bazel
/// `--stamp` build.
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
        // Exercises the macro from within nvcf-info itself. The cross-crate
        // behavior, that env!() resolves to the *caller's* stamped values,
        // is covered by each service's own routes tests.
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
