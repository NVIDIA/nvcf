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

//! Compiles the vendored GDS proto contract under `proto/`.
//!
//! The files are a byte-identical copy of
//! `src/control-plane-services/global-discovery/api/proto`; Bazel diff tests
//! keep them in sync and `hack/sync-gds-proto.sh` in that module copies them.

const PROTOS: &[&str] = &[
    "proto/nvidia/gds/v1/common.proto",
    "proto/nvidia/gds/v1/cluster.proto",
    "proto/nvidia/gds/v1/mcs.proto",
    "proto/nvidia/gds/v1/pool.proto",
    "proto/nvidia/gds/v1/server.proto",
];

fn main() -> Result<(), Box<dyn std::error::Error>> {
    // A protoc from a release archive or a package manager finds the
    // well-known types next to its binary. The hermetic protoc Bazel passes
    // in does not, so Bazel names their directory in PROTOC_WKT_DIR.
    let mut includes = vec!["proto".to_string()];
    if let Ok(wkt) = std::env::var("PROTOC_WKT_DIR") {
        includes.push(wkt);
    }
    let includes: Vec<&str> = includes.iter().map(String::as_str).collect();

    tonic_prost_build::configure()
        .build_client(true)
        .build_server(true)
        // Fakes and test servers implement only the RPCs they need.
        .generate_default_stubs(true)
        .compile_protos(PROTOS, &includes)?;

    println!("cargo:rerun-if-changed=proto");
    println!("cargo:rerun-if-env-changed=PROTOC_WKT_DIR");
    Ok(())
}
