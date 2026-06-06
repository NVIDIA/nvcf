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

pub mod gateway_pb {
    tonic::include_proto!("llm_gateway");
}

pub const REGISTRATION_HEARTBEAT_MS_METADATA: &str = "x-stargate-registration-heartbeat-ms";

pub mod pb {
    tonic::include_proto!("stargate");

    pub mod serde_stargates_set {
        use std::collections::BTreeMap;

        use serde::{Deserialize, Deserializer, Serialize, Serializer};

        use super::StargateInfo;

        fn key_for(info: &StargateInfo) -> String {
            if !info.stargate_id.is_empty() {
                info.stargate_id.clone()
            } else {
                info.advertise_addr.clone()
            }
        }

        pub fn serialize<S>(value: &[StargateInfo], serializer: S) -> Result<S::Ok, S::Error>
        where
            S: Serializer,
        {
            let mut deduped: BTreeMap<String, StargateInfo> = BTreeMap::new();
            for info in value {
                deduped.insert(key_for(info), info.clone());
            }
            let normalized: Vec<StargateInfo> = deduped.into_values().collect();
            normalized.serialize(serializer)
        }

        pub fn deserialize<'de, D>(deserializer: D) -> Result<Vec<StargateInfo>, D::Error>
        where
            D: Deserializer<'de>,
        {
            let values = Vec::<StargateInfo>::deserialize(deserializer)?;
            let mut deduped: BTreeMap<String, StargateInfo> = BTreeMap::new();
            for info in values {
                deduped.insert(key_for(&info), info);
            }
            Ok(deduped.into_values().collect())
        }
    }

    pub mod serde_string_set {
        use std::collections::BTreeSet;

        use serde::{Deserialize, Deserializer, Serialize, Serializer};

        pub fn serialize<S>(value: &[String], serializer: S) -> Result<S::Ok, S::Error>
        where
            S: Serializer,
        {
            let normalized: Vec<String> = value
                .iter()
                .filter(|value| !value.is_empty())
                .cloned()
                .collect::<BTreeSet<_>>()
                .into_iter()
                .collect();
            normalized.serialize(serializer)
        }

        pub fn deserialize<'de, D>(deserializer: D) -> Result<Vec<String>, D::Error>
        where
            D: Deserializer<'de>,
        {
            let values = Vec::<String>::deserialize(deserializer)?;
            Ok(values
                .into_iter()
                .filter(|value| !value.is_empty())
                .collect::<BTreeSet<_>>()
                .into_iter()
                .collect())
        }
    }
}

#[cfg(test)]
mod build_plan;

#[cfg(test)]
#[path = "../build.rs"]
mod build_script;

#[cfg(test)]
mod tests {
    use crate::build_plan::{PROTO_SERDE_DERIVE, proto_compile_plans};
    use crate::pb::{StargateInfo, WatchStargatesResponse};

    #[test]
    fn proto_build_plan_covers_stargate_and_gateway_generation() {
        let plans = proto_compile_plans();

        assert_eq!(plans.len(), 2);
        assert_eq!(plans[0].protos, ["proto/stargate.proto"]);
        assert_eq!(plans[0].includes, ["proto"]);
        assert!(plans[0].build_server);
        assert_eq!(plans[0].type_attributes, [(".", PROTO_SERDE_DERIVE)]);
        assert_eq!(
            plans[0].field_attributes,
            [
                (
                    "stargate.WatchStargatesResponse.stargates",
                    "#[serde(default, serialize_with = \"crate::pb::serde_stargates_set::serialize\", deserialize_with = \"crate::pb::serde_stargates_set::deserialize\")]",
                ),
                (
                    "stargate.WatchStargatesResponse.watch_stargate_urls",
                    "#[serde(default, serialize_with = \"crate::pb::serde_string_set::serialize\", deserialize_with = \"crate::pb::serde_string_set::deserialize\")]",
                ),
            ]
        );
        assert_eq!(plans[1].protos, ["proto/llm_gateway.proto"]);
        assert_eq!(plans[1].includes, ["proto"]);
        assert!(!plans[1].build_server);
        assert!(plans[1].type_attributes.is_empty());
        assert!(plans[1].field_attributes.is_empty());
        assert_eq!(crate::build_script::planned_proto_compile_count(), 2);
    }

    #[test]
    fn watch_stargates_response_json_serde_normalizes_set_fields() {
        let response = WatchStargatesResponse {
            stargates: vec![
                StargateInfo {
                    stargate_id: "stargate-1".to_string(),
                    advertise_addr: "10.0.0.2:50071".to_string(),
                    http_advertise_addr: String::new(),
                    grpc_pylon_dial_addr: String::new(),
                },
                StargateInfo {
                    stargate_id: "stargate-0".to_string(),
                    advertise_addr: "10.0.0.1:50071".to_string(),
                    http_advertise_addr: String::new(),
                    grpc_pylon_dial_addr: String::new(),
                },
                StargateInfo {
                    stargate_id: "stargate-1".to_string(),
                    advertise_addr: "duplicate-should-be-dropped:50071".to_string(),
                    http_advertise_addr: String::new(),
                    grpc_pylon_dial_addr: String::new(),
                },
            ],
            watch_stargate_urls: vec![
                "remote-b:50071".to_string(),
                String::new(),
                "remote-a:50071".to_string(),
                "remote-b:50071".to_string(),
            ],
        };

        let json = serde_json::to_string(&response).expect("response should serialize");
        let decoded: WatchStargatesResponse =
            serde_json::from_str(&json).expect("response should deserialize");

        let ids: Vec<&str> = decoded
            .stargates
            .iter()
            .map(|info| info.stargate_id.as_str())
            .collect();
        assert_eq!(ids, vec!["stargate-0", "stargate-1"]);
        assert_eq!(
            decoded.watch_stargate_urls,
            vec!["remote-a:50071", "remote-b:50071"]
        );
    }
}
