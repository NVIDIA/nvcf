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

    // Use vendored protoc binary (includes well-known types)
    std::env::set_var("PROTOC", protoc_bin_vendored::protoc_bin_path()?);

    // Compile nvcf.proto (NVCF service definitions including Autoscaler service)
    let empty: [String; 0] = [];
    tonic_build::configure()
        .build_server(false) // We're only the client
        .build_client(true)
        .compile_protos(&[nvcf_proto], &empty)?;

    // Tell cargo to rerun if proto file changes
    println!("cargo:rerun-if-changed={}", nvcf_proto);

    Ok(())
}
