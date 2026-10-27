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

//! Client for the Global Discovery Service (GDS).
//!
//! - [`endpoints`] and [`channel`]: an ordered list of GDS URLs with
//!   failover, DNS re-resolution on every reconnect, GDS-advertised fallback
//!   endpoints and a timed return to the primary.
//! - [`client`]: typed calls with deadlines and retries on UNAVAILABLE and
//!   DEADLINE_EXCEEDED.
//! - [`cache`] and [`routers`]: TTL-cached, watch-driven pool and router
//!   views, and the seed URLs pylons and Stargates dial.
//! - [`register`]: pool registration with heartbeats and a graceful
//!   drain-then-deregister shutdown.
//! - [`derived`] and [`labels`]: derived Service names and the well-known
//!   labels shared with the Go module.
//!
//! The crate re-exports `tonic`, `prost` and `prost_types`, so dependents
//! need not pin their versions.

pub mod cache;
pub mod channel;
pub mod client;
pub mod derived;
pub mod endpoints;
pub mod error;
pub mod labels;
pub mod metrics;
pub mod proto;
pub mod register;
pub mod routers;
#[cfg(feature = "stargate")]
pub mod stargate;

mod task;
mod watch;

pub use prost;
pub use prost_types;
pub use tonic;

pub use cache::{PoolCache, PoolCacheConfig, PoolState};
pub use channel::{ChannelManager, Connection, ConnectionState};
pub use client::{GdsClient, GdsClientConfig, WatchStream};
pub use derived::derived_service_name;
pub use endpoints::{Backoff, ConnectPolicy, EndpointSet, GdsUrl, TlsOptions};
pub use error::Error;
#[cfg(feature = "metrics")]
pub use metrics::GdsClientMetrics;
pub use proto::v1;
pub use register::{
    DesiredState, PoolRegistration, PoolSpec, PoolUpdate, RegistrationState, next_generation,
};
pub use routers::{
    RouterCache, RouterCacheConfig, RouterState, derived_service_url, pylon_seed_urls,
    remote_watch_urls, router_seed_urls,
};
