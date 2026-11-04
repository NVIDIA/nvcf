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

fn main() {
    // Link against libibverbs (rdma-core).
    // Must have libibverbs-dev installed: apt install libibverbs-dev
    // (or point RDMA_CORE_PREFIX at an unpacked rdma-core `usr` tree).
    println!("cargo:rustc-link-lib=ibverbs");
    println!("cargo:rerun-if-changed=src/cm.c");
    println!("cargo:rerun-if-env-changed=RDMA_CORE_PREFIX");
    let prefix = std::env::var_os("RDMA_CORE_PREFIX").map(std::path::PathBuf::from);
    // The prefix's libraries are searched with or without RDMA-CM:
    // libibverbs is always linked.
    if let Some(prefix) = &prefix {
        // Debian multiarch directory of the target, e.g. lib/aarch64-linux-gnu.
        let arch = std::env::var("CARGO_CFG_TARGET_ARCH").unwrap_or_default();
        println!(
            "cargo:rustc-link-search=native={}",
            prefix.join(format!("lib/{arch}-linux-gnu")).display()
        );
        println!(
            "cargo:rustc-link-search=native={}",
            prefix.join("lib").display()
        );
    }
    if std::env::var_os("CARGO_FEATURE_RDMA_CM").is_some() {
        let mut build = cc::Build::new();
        build.file("src/cm.c").flag_if_supported("-std=c11");
        if let Some(prefix) = &prefix {
            build.include(prefix.join("include"));
        }
        build.compile("nvstor_rdma_cm");
        println!("cargo:rustc-link-lib=rdmacm");
    }
}
