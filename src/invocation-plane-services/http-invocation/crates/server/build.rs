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
    // When CICD_NEXT_VERSION is set (by Docker build-arg in CI), override
    // CARGO_PKG_VERSION so env!("CARGO_PKG_VERSION") returns the release version.
    // Locally (and in CI check/test jobs), Cargo.toml's "0.0.0-dev" placeholder is used.
    // Only the Docker build stage passes CICD_NEXT_VERSION via --build-arg.
    if let Ok(version) = std::env::var("CICD_NEXT_VERSION") {
        if !version.is_empty() {
            println!("cargo:rustc-env=CARGO_PKG_VERSION={}", version);
        }
    }

    // Display a warning that the build script is running
    println!("cargo:warning=Running build.rs");

    // Prefer an externally-provided PROTOC (e.g. Bazel sets this via
    // cargo_build_script's build_script_env to a hermetic @protobuf//:protoc).
    // Fall back to protoc_bin_vendored for plain cargo builds, which extracts
    // a protoc binary bundled in the crate. Under crate_universe the vendored
    // binary path is not preserved on disk and the lookup panics, so the env
    // override is the supported path for Bazel.
    if std::env::var_os("PROTOC").is_none() {
        // SAFETY: build scripts run single-threaded before any cargo
        // parallelism kicks in. std::env::set_var was made unsafe in
        // Rust 1.80 because env mutation can race with libc readers in
        // multi-threaded contexts; in a build script the precondition
        // holds.
        unsafe {
            std::env::set_var("PROTOC", protoc_bin_vendored::protoc_bin_path()?);
        }
    }
    // alternatively, compile from source https://crates.io/crates/protobuf-src
    // std::env::set_var("PROTOC", protobuf_src::protoc());

    let project_root = concat!(env!("CARGO_MANIFEST_DIR"));
    println!("cargo:warning=Project root is {}", project_root);

    // Under cargo, protoc_bin_vendored ships its own copy of the well-known
    // type .proto files at <protoc>/../include/, and tonic-prost-build adds
    // that path automatically. Under Bazel we override PROTOC with a hermetic
    // protoc that does NOT bundle includes, so the well-known types have to
    // be wired in explicitly via PROTOC_WKT_DIR. Bazel's cargo_build_script
    // sets it to the directory containing google/protobuf/*.proto from
    // @protobuf//:well_known_type_protos.
    let mut includes = vec!["proto".to_string()];
    if let Ok(wkt) = std::env::var("PROTOC_WKT_DIR") {
        includes.push(wkt);
    }
    let include_refs: Vec<&str> = includes.iter().map(String::as_str).collect();
    tonic_prost_build::configure()
        .bytes(".")
        .compile_protos(&["proto/nvcf.proto", "proto/ratelimiter.proto"], &include_refs)?;

    Ok(())
}
