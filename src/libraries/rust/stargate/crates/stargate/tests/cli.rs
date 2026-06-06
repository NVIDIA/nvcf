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

use std::process::Command;

#[test]
fn invalid_stargate_runtime_listen_addr_exits_nonzero() {
    let status = Command::new(env!("CARGO_BIN_EXE_stargate"))
        .args([
            "--stargate-id",
            "test-stargate",
            "--advertise-addr",
            "127.0.0.1:50071",
            "--stargate-discovery-dns-name",
            "stargate-headless",
            "--listen-addr",
            "not-a-socket-addr",
        ])
        .status()
        .expect("stargate process should start");

    assert!(
        !status.success(),
        "stargate should reject invalid runtime listen addresses"
    );
}

#[test]
fn list_models_probe_invalid_endpoint_exits_nonzero() {
    let status = Command::new(env!("CARGO_BIN_EXE_stargate-list-models-probe"))
        .args([
            "--addr",
            "http://[",
            "--expect",
            "model-a",
            "--attempts",
            "1",
            "--interval-ms",
            "0",
        ])
        .status()
        .expect("stargate-list-models-probe process should start");

    assert!(
        !status.success(),
        "list-models probe should reject invalid discovery endpoints"
    );
}
