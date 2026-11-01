/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let nvcf_proto = "proto/nvcf.proto";

    // Prefer an externally-provided PROTOC (Bazel sets this via
    // cargo_build_script's build_script_env to the hermetic
    // @protobuf//:protoc). Fall back to protoc_bin_vendored for plain
    // cargo builds, which extracts a protoc binary bundled in the
    // crate. Under crate_universe the vendored binary path is not
    // preserved on disk and the lookup panics, so the env override is
    // the supported path for Bazel.
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

    // Under cargo, protoc_bin_vendored ships its own copy of the
    // well-known type .proto files at <protoc>/../include/, and
    // tonic-build adds that path automatically. Under Bazel we
    // override PROTOC with a hermetic protoc that does NOT bundle
    // includes, so the well-known types have to be wired in
    // explicitly via PROTOC_WKT_DIR. Bazel's cargo_build_script sets
    // it to the directory containing google/protobuf/*.proto from
    // @protobuf//:well_known_type_protos.
    // Include the local proto/ dir so `nvcf.proto` resolves -- tonic-build
    // normally adds the parent of each proto file automatically when
    // includes is empty, but we override the empty case below by adding
    // PROTOC_WKT_DIR.
    let mut includes: Vec<String> = vec!["proto".to_string()];
    if let Ok(wkt) = std::env::var("PROTOC_WKT_DIR") {
        includes.push(wkt);
    }
    let include_refs: Vec<&str> = includes.iter().map(String::as_str).collect();

    // Compile nvcf.proto (NVCF service definitions including Autoscaler service)
    tonic_build::configure()
        .build_server(false) // We're only the client
        .build_client(true)
        .compile_protos(&[nvcf_proto], &include_refs)?;

    // Tell cargo to rerun if proto file changes
    println!("cargo:rerun-if-changed={}", nvcf_proto);

    Ok(())
}
