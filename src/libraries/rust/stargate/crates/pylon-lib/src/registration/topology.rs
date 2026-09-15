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

use std::collections::BTreeSet;

use tokio::sync::watch;

use super::grpc_endpoint::StargateGrpcEndpoint;

#[derive(Debug, Default, Clone, PartialEq, Eq)]
pub(super) enum RegistrationRouterTopology {
    #[default]
    AwaitingInitial,
    Published(BTreeSet<StargateGrpcEndpoint>),
}

impl RegistrationRouterTopology {
    pub(super) fn published_routers(&self) -> Option<&BTreeSet<StargateGrpcEndpoint>> {
        match self {
            Self::AwaitingInitial => None,
            Self::Published(routers) => Some(routers),
        }
    }
}

pub(super) fn publish_registration_router_topology(
    topology_tx: &watch::Sender<RegistrationRouterTopology>,
    active_routers: &BTreeSet<StargateGrpcEndpoint>,
    initial_publish_ready: bool,
) -> bool {
    topology_tx.send_if_modified(|topology| {
        let changed = match topology {
            RegistrationRouterTopology::AwaitingInitial => {
                initial_publish_ready && !active_routers.is_empty()
            }
            RegistrationRouterTopology::Published(routers) => routers != active_routers,
        };
        if changed {
            *topology = RegistrationRouterTopology::Published(active_routers.clone());
        }
        changed
    })
}
