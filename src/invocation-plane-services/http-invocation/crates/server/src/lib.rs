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

#![warn(clippy::all)]
#![cfg_attr(not(feature = "jemalloc"), forbid(unsafe_code))]
#![cfg_attr(feature = "jemalloc", deny(unsafe_code))]
#![deny(warnings)]
#![deny(unused_crate_dependencies)]
#![deny(unused_extern_crates)]

// Silences unused crate error
#[cfg(test)]
use rstest as _;
use rustls as _;
#[cfg(test)]
use testcontainers_modules as _;
// Silence unused crate error when mimalloc is enabled but jemalloc takes precedence
#[cfg(all(feature = "mimalloc", feature = "jemalloc"))]
use mimalloc as _;

// Global allocator configuration based on features
// jemalloc takes precedence over mimalloc if both are enabled
#[cfg(feature = "jemalloc")]
#[global_allocator]
static GLOBAL: tikv_jemallocator::Jemalloc = tikv_jemallocator::Jemalloc;

#[cfg(all(feature = "mimalloc", not(feature = "jemalloc")))]
#[global_allocator]
static GLOBAL: mimalloc::MiMalloc = mimalloc::MiMalloc;

// Configure jemalloc profiling when both jemalloc and profiling features are enabled
#[cfg(all(feature = "jemalloc", feature = "profiling"))]
#[allow(non_upper_case_globals)]
#[allow(unsafe_code)] // Required for jemalloc malloc_conf configuration only
#[no_mangle]
pub static malloc_conf: &[u8] = b"prof:true,prof_active:true,lg_prof_sample:19\0";

// Configure jemalloc without profiling when jemalloc is enabled but profiling is not
#[cfg(all(feature = "jemalloc", not(feature = "profiling")))]
#[allow(non_upper_case_globals)]
#[allow(unsafe_code)] // Required for jemalloc malloc_conf configuration only
#[no_mangle]
pub static malloc_conf: &[u8] = b"prof:false\0";

pub mod app;
pub mod metrics;
pub mod middleware;
pub mod nats;
pub mod nvcf_api;
pub mod profiling;
pub mod rate_limit;
pub mod request_id;
pub mod routes;
pub mod s3;
pub mod secrets;
pub mod settings;
pub mod telemetry;
pub mod worker_streams;
