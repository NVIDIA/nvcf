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

//! Conversions to `stargate-proto` types (feature `stargate`).
//!
//! These are for diagnostics, for example listing GDS routers next to the
//! `WatchStargates` view. They are not registration targets: a pylon reaches
//! a router cluster through its seed URL and learns the per-replica
//! identities from `WatchStargates` on that seed.

use stargate_proto::pb::StargateInfo;

use crate::derived::derived_service_host;
use crate::labels::DIAL_SCHEME_HTTP;
use crate::proto::v1::Router;
use crate::routers::{DEFAULT_DIAL_SCHEME, RouterState, router_key, seed_url};

/// Describes `router` as a [`StargateInfo`]: `stargate_id` is
/// `gds:<cluster>/<namespace>/<name>`, `advertise_addr` the seed URL and
/// `http_advertise_addr` the derived Service URL of the HTTP port. Returns
/// `None` when the router has no seed URL.
pub fn router_to_stargate_info(router: &Router) -> Option<StargateInfo> {
    let advertise_addr = seed_url(router)?;
    let http_advertise_addr = u16::try_from(router.http_port)
        .ok()
        .filter(|port| *port != 0)
        .map(|port| {
            let scheme = match router.dial_scheme.trim().to_ascii_lowercase().as_str() {
                DIAL_SCHEME_HTTP => DIAL_SCHEME_HTTP,
                _ => DEFAULT_DIAL_SCHEME,
            };
            format!(
                "{scheme}://{}:{port}",
                derived_service_host(&router.namespace, &router.name)
            )
        })
        .unwrap_or_default();
    Some(StargateInfo {
        stargate_id: format!("gds:{}", router_key(router)),
        advertise_addr,
        http_advertise_addr,
        grpc_pylon_dial_addr: String::new(),
    })
}

/// [`router_to_stargate_info`] for every router of a state, in key order.
pub fn routers_to_stargate_infos(state: &RouterState) -> Vec<StargateInfo> {
    state
        .iter()
        .filter_map(|router| router_to_stargate_info(router))
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn router_converts_to_stargate_info() {
        let router = Router {
            cluster: "a".to_string(),
            namespace: "llm".to_string(),
            name: "llm-request-router-backend".to_string(),
            grpc_port: 50071,
            http_port: 8080,
            dial_scheme: "https".to_string(),
            ..Router::default()
        };
        let info = router_to_stargate_info(&router).expect("info");
        assert_eq!(info.stargate_id, "gds:a/llm/llm-request-router-backend");
        assert_eq!(
            info.advertise_addr,
            "https://derived-b756q8b74k.llm.svc.cluster.local:50071"
        );
        assert_eq!(
            info.http_advertise_addr,
            "https://derived-b756q8b74k.llm.svc.cluster.local:8080"
        );
        assert!(info.grpc_pylon_dial_addr.is_empty());
    }

    #[test]
    fn router_without_grpc_port_has_no_info() {
        let router = Router {
            namespace: "llm".to_string(),
            name: "r".to_string(),
            ..Router::default()
        };
        assert!(router_to_stargate_info(&router).is_none());
    }
}
