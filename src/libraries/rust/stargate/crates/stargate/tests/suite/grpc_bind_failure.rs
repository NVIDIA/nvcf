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

use crate::common::{base_config, bind_ephemeral, init_crypto};
use stargate::runtime::BoundStargateListeners;

/// When the gRPC port is already occupied, listener preparation must fail
/// before a runtime with unusable roots can be constructed.
#[test]
fn listener_binding_fails_when_grpc_port_is_occupied() {
    init_crypto();

    let (grpc_addr, _grpc_blocker) = bind_ephemeral();
    let mut config = base_config("test-occupied", grpc_addr, "127.0.0.1:0".parse().unwrap());
    let result = BoundStargateListeners::bind(&mut config);
    assert!(
        result.is_err(),
        "listener binding must fail when gRPC port is occupied"
    );
}

/// When the model-discovery port is already occupied, listener preparation
/// must fail rather than serving `ListModels` on the control-plane port.
#[test]
fn listener_binding_fails_when_model_discovery_port_is_occupied() {
    init_crypto();

    let (model_discovery_addr, _model_discovery_blocker) = bind_ephemeral();
    let mut config = base_config(
        "test-model-discovery-occupied",
        "127.0.0.1:0".parse().unwrap(),
        "127.0.0.1:0".parse().unwrap(),
    );
    config.model_discovery_listen_addr = model_discovery_addr;
    let result = BoundStargateListeners::bind(&mut config);
    assert!(
        result.is_err(),
        "listener binding must fail when model-discovery port is occupied"
    );
}
