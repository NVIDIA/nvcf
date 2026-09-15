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

#![warn(clippy::all)]
#![forbid(unsafe_code)]
#![deny(warnings)]
#![deny(unused_crate_dependencies)]
#![deny(unused_extern_crates)]
#[allow(dead_code)]
#[allow(unused_variables)]
// Suppress unused crate dependency warnings
use axum_server as _;
use gethostname as _;
use rand as _;

pub use tracing;

pub mod cassandra;
pub mod health;
pub mod metrics;
pub mod models;
pub mod nvcf_api;
pub mod routes;
pub mod scaling;
pub mod secrets;
pub mod settings;
pub mod startup;
pub mod timeseries_db;
pub mod tracing_init;
pub mod work;
