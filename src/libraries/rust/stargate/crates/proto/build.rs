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

fn main() -> Result<(), Box<dyn std::error::Error>> {
    tonic_prost_build::configure()
        .type_attribute(".", "#[derive(serde::Serialize, serde::Deserialize)]")
        .field_attribute(
            "stargate.WatchStargatesResponse.stargates",
            "#[serde(default, serialize_with = \"crate::pb::serde_stargates_set::serialize\", deserialize_with = \"crate::pb::serde_stargates_set::deserialize\")]",
        )
        .field_attribute(
            "stargate.WatchStargatesResponse.watch_stargate_urls",
            "#[serde(default, serialize_with = \"crate::pb::serde_string_set::serialize\", deserialize_with = \"crate::pb::serde_string_set::deserialize\")]",
        )
        .compile_protos(&["proto/stargate.proto"], &["proto"])?;
    tonic_prost_build::configure().compile_protos(&["proto/llm_gateway.proto"], &["proto"])?;
    Ok(())
}
