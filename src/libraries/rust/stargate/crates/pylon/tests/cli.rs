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
fn invalid_metrics_addr_exits_nonzero() {
    let status = Command::new(env!("CARGO_BIN_EXE_pylon"))
        .args([
            "--upstream-http-base-url",
            "http://127.0.0.1:1",
            "--metrics-host",
            "not-a-socket-host",
        ])
        .status()
        .expect("pylon process should start");

    assert!(
        !status.success(),
        "pylon should reject invalid runtime metrics listen addresses"
    );
}
