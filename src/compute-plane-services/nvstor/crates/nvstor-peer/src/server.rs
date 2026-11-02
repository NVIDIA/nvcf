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

//! `nvstor-peer serve`: export a directory read-only on every selected
//! RDMA NIC, to at most `max_fetchers` fetch sessions at once.

use std::path::PathBuf;
use std::sync::Arc;

use nvstor_rdma::admission::Admission;
use nvstor_rdma::device::Access;
use nvstor_rdma::object::ReadFn;
use nvstor_rdma::pool::MemoryPool;
use nvstor_rdma::rail::{open_devices, DeviceSelect, RailInfo};
use nvstor_rdma::s3_rdma::S3RdmaServer;
use nvstor_rdma::transport::RdmaTransport;

use crate::export::DirExport;
use crate::sessions::{self, FetcherCap};

/// Default cap on concurrently served fetchers; see `docs/peer-copy.md`
/// for why 8.
pub const DEFAULT_MAX_FETCHERS: usize = 4;

#[derive(Clone, Debug)]
pub struct ServeOptions {
    /// Directory to export.
    pub root: PathBuf,
    /// Serve it under this key prefix ("" for none); see
    /// [`DirExport::open_as`].
    pub export_as: String,
    /// Bootstrap port of rail 0; rail k listens on port + k (0 = any).
    pub port: u16,
    /// Address the bootstrap listeners bind: `::` (the default) for every
    /// IPv4 and IPv6 address, so a fetcher may name this node by either;
    /// a specific address to restrict them.
    pub listen: std::net::IpAddr,
    pub devices: DeviceSelect,
    /// Bytes per registered slot: the largest range served.
    pub slot_size: usize,
    pub slots: usize,
    /// Requests in progress across all rails.
    pub max_in_flight: usize,
    /// Fetch sessions served at once; more are told BUSY.
    pub max_fetchers: usize,
}

/// A running export. Serving stops when it is dropped.
pub struct Server {
    pub export: Arc<DirExport>,
    pub cap: Arc<FetcherCap>,
    /// (device name, advertised rail) per NIC served.
    pub rails: Vec<(String, RailInfo)>,
    transports: Vec<Arc<RdmaTransport>>,
    /// The request workers of every rail; closed on drop so they exit.
    admission: Arc<Admission>,
}

impl Drop for Server {
    fn drop(&mut self) {
        self.admission.close();
        for t in &self.transports {
            t.shutdown();
        }
    }
}

/// Start serving. Only holders of `secret` get a QP at all: the
/// bootstrap refuses everyone else before any verbs resource exists, and
/// fetchers over `max_fetchers` are told BUSY at the same point.
pub fn start(opts: &ServeOptions, secret: &str) -> Result<Server, String> {
    if opts.max_fetchers == 0 {
        return Err("--max-fetchers must be at least 1".into());
    }
    let export = DirExport::open_as(&opts.root, &opts.export_as)
        .map_err(|e| format!("{}: {e}", opts.root.display()))?;
    let devices = open_devices(&opts.devices)?;
    if devices.is_empty() {
        return Err(format!("no RDMA device selected ({:?})", opts.devices));
    }
    let read_fn: ReadFn = {
        let export = Arc::clone(&export);
        Arc::new(move |key, offset, buf| export.read(key, offset, buf))
    };
    let verifier = sessions::verifier(secret);
    let auth = sessions::request_auth(secret);
    // One cap across every rail: a fetcher counts once however many NICs
    // and connections it uses.
    let cap = FetcherCap::new(opts.max_fetchers);
    let admitter = cap.admitter();

    let mut transports = Vec::new();
    let mut rails = Vec::new();
    for (k, dev) in devices.into_iter().enumerate() {
        let dev = Arc::new(dev);
        // The server only RDMA-WRITEs out of its pool: no peer may read or
        // write it.
        let pool = MemoryPool::with_access(&dev, opts.slot_size, opts.slots, Access::LOCAL)
            .map_err(|e| format!("{}: pool: {e}", dev.name))?;
        let t = Arc::new(RdmaTransport::new(Arc::clone(&dev), Arc::new(pool)));
        t.set_verifier(Arc::clone(&verifier));
        t.set_session_admitter(Arc::clone(&admitter));
        let want = if opts.port == 0 {
            0
        } else {
            opts.port
                .checked_add(k as u16)
                .ok_or_else(|| format!("port {} + rail {k} overflows", opts.port))?
        };
        let bound = t
            .listen_on(opts.listen, want)
            .map_err(|e| format!("{}: listen on {want}: {e}", dev.name))?;
        rails.push((
            dev.name.clone(),
            RailInfo {
                port: bound.port(),
                gid: t.local_gid(),
            },
        ));
        transports.push(t);
    }
    let netdevs: Vec<(String, [u8; 16])> = transports
        .iter()
        .filter_map(|t| {
            Some((
                t.device().gid_netdev(t.device().roce_gid_index())?,
                t.local_gid(),
            ))
        })
        .collect();
    nvstor_rdma::rail::warn_strict_rp_filter(&netdevs);
    let advertised: Vec<RailInfo> = rails.iter().map(|(_, r)| *r).collect();
    let admission = Admission::new(opts.max_in_flight);
    for t in &transports {
        // Read-only: ALLOC / PERSIST / DELETE are refused before they can
        // reserve a pool slot. The process-wide replay guard covers
        // every rail.
        S3RdmaServer::read_only(Arc::clone(&read_fn), Arc::clone(t), Arc::clone(&admission))
            .with_auth(Arc::clone(&auth))
            .with_rails(advertised.clone())
            .start();
    }
    Ok(Server {
        export,
        cap,
        rails,
        transports,
        admission,
    })
}
