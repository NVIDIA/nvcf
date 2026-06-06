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

pub(crate) const PROTO_SERDE_DERIVE: &str = "#[derive(serde::Serialize, serde::Deserialize)]";

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct ProtoCompilePlan {
    pub protos: &'static [&'static str],
    pub includes: &'static [&'static str],
    pub build_server: bool,
    pub type_attributes: &'static [(&'static str, &'static str)],
    pub field_attributes: &'static [(&'static str, &'static str)],
}

pub(crate) fn proto_compile_plans() -> [ProtoCompilePlan; 2] {
    [
        ProtoCompilePlan {
            protos: &["proto/stargate.proto"],
            includes: &["proto"],
            build_server: true,
            type_attributes: &[(".", PROTO_SERDE_DERIVE)],
            field_attributes: &[
                (
                    "stargate.WatchStargatesResponse.stargates",
                    "#[serde(default, serialize_with = \"crate::pb::serde_stargates_set::serialize\", deserialize_with = \"crate::pb::serde_stargates_set::deserialize\")]",
                ),
                (
                    "stargate.WatchStargatesResponse.watch_stargate_urls",
                    "#[serde(default, serialize_with = \"crate::pb::serde_string_set::serialize\", deserialize_with = \"crate::pb::serde_string_set::deserialize\")]",
                ),
            ],
        },
        ProtoCompilePlan {
            protos: &["proto/llm_gateway.proto"],
            includes: &["proto"],
            build_server: false,
            type_attributes: &[],
            field_attributes: &[],
        },
    ]
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn stargate_plan_carries_watch_response_serde_attributes() {
        let [stargate_plan, _] = proto_compile_plans();

        assert_eq!(stargate_plan.protos, ["proto/stargate.proto"]);
        assert!(stargate_plan.build_server);
        assert_eq!(stargate_plan.type_attributes, [(".", PROTO_SERDE_DERIVE)]);
        assert_eq!(stargate_plan.field_attributes.len(), 2);
    }

    #[test]
    fn gateway_plan_builds_client_only_proto() {
        let [_, gateway_plan] = proto_compile_plans();

        assert_eq!(gateway_plan.protos, ["proto/llm_gateway.proto"]);
        assert_eq!(gateway_plan.includes, ["proto"]);
        assert!(!gateway_plan.build_server);
        assert!(gateway_plan.type_attributes.is_empty());
        assert!(gateway_plan.field_attributes.is_empty());
    }
}
