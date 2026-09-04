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

use axum::Json;

/// GET /info: service/version/commit, via the shared nvcf-info crate (same
/// schema as NVCF's Go and Java /info endpoints). version and commit come
/// from CARGO_PKG_VERSION/NVCF_GIT_COMMIT, stamped at Bazel --stamp build
/// time by crates/server/BUILD.bazel's version_env expand_template (see
/// tools/workspace_status.sh for the underlying git values); unstamped/local
/// builds fall back to "0.0.0-dev"/"unknown".
pub async fn get_info() -> Json<nvcf_info::InfoResponse> {
    Json(nvcf_info::info_response!("nvcf-invocation-service"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn info_reports_service_version_and_commit() {
        let Json(info) = get_info().await;
        assert_eq!(info.service, "nvcf-invocation-service");
        // Stamped only on Bazel --stamp builds; under cargo these are the
        // unstamped fallbacks. Assert they are populated, not their literals,
        // so the test does not break on every release bump.
        assert!(!info.version.is_empty());
        assert!(!info.commit.is_empty());
    }
}
