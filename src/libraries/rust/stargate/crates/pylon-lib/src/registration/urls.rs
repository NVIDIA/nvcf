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

pub(super) fn normalize_addr(addr: &str) -> String {
    if addr.starts_with("http://") || addr.starts_with("https://") {
        addr.to_string()
    } else {
        format!("http://{addr}")
    }
}

pub(super) fn infer_upstream_http_base_url(inference_server_url: &str) -> Option<String> {
    let url = url::Url::parse(inference_server_url).ok()?;
    matches!(url.scheme(), "http" | "https")
        .then(|| inference_server_url.trim_end_matches('/').to_string())
}

pub(super) fn is_direct_inference_server_url(inference_server_url: &str) -> bool {
    url::Url::parse(inference_server_url).is_ok_and(|url| url.scheme() == "quic")
}
