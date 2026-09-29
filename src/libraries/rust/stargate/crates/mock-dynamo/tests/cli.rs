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
fn explains_profile_without_starting_server() {
    let output = Command::new(env!("CARGO_BIN_EXE_mock-dynamo"))
        .args(["--explain-profile", "h100-llama-3.1-8b"])
        .output()
        .expect("mock-dynamo process should start");

    assert!(output.status.success());
    let stdout = String::from_utf8(output.stdout).expect("profile explanation should be UTF-8");
    assert!(stdout.contains("context length: 131072 input + output tokens"));
    assert!(stdout.contains("deterministic gaussian distribution over 128..=8192"));
    assert!(stdout.contains("per active request: 164.0 tokens/s average"));
    assert!(stdout.contains("1,000 input tokens: about 162.9 ms average"));
}

#[test]
fn invalid_http_listen_addr_exits_nonzero() {
    let status = Command::new(env!("CARGO_BIN_EXE_mock-dynamo"))
        .args(["--http-listen-addr", "not-a-socket-addr"])
        .status()
        .expect("mock-dynamo process should start");

    assert!(
        !status.success(),
        "mock-dynamo should reject invalid runtime listen addresses"
    );
}
