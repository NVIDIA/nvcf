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

pub(super) fn embedding_items_from_request_body(body_bytes: &[u8]) -> Option<u64> {
    let value = serde_json::from_slice::<serde_json::Value>(body_bytes).ok()?;
    let input = value.get("input")?;
    match input {
        serde_json::Value::String(_) => Some(1),
        serde_json::Value::Array(items) => {
            if items.is_empty() {
                return Some(0);
            }
            if items.iter().all(serde_json::Value::is_number) {
                Some(1)
            } else {
                u64::try_from(items.len()).ok()
            }
        }
        _ => None,
    }
}
