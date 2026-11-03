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

//! The fetch side of `nvstor-peer` over RDMA: a [`SourceSet`] of
//! `serve` nodes named by `HOST:PORT`.
//!
//! - Every name is resolved on each selection round, to every address it
//!   has, so a DNS name of ready replicas (a Kubernetes headless Service)
//!   yields one source per replica, and replicas that became ready since
//!   the last round are included.
//! - The first connection to a source asks it for its rails and stays
//!   open (the "anchor") until the fetch stops using that source. It holds
//!   the fetch's session on the source's fetcher cap while workers
//!   connect, reconnect and fail over, so the place is never briefly
//!   empty and given to another fetcher. Releasing the source closes it.
//! - A source that answers BUSY in the bootstrap is reported as
//!   [`OpenError::Busy`], one that refuses the credentials as
//!   [`OpenError::Refused`]; any other failure as `Failed`.

use std::collections::HashMap;
use std::net::ToSocketAddrs;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;

use nvstor_rdma::bootstrap;
use nvstor_rdma::hydrate::HydrateError;
use nvstor_rdma::object::GetError;
use nvstor_rdma::rail::{RailInfo, RailPeer, Rails};
use nvstor_rdma::s3_rdma::{connect_listing_rails, RdmaSource};
use parking_lot::Mutex;
use tracing::debug;

use crate::fetch::{OpenError, SourceSet};
use crate::manifest;

/// A source in use: its rails' bootstrap addresses and the connection
/// that holds the fetch's place on it.
struct Anchor {
    rail_addrs: Vec<String>,
    /// GIDs of the source's rails, for pairing local NICs by plane.
    rail_gids: Vec<[u8; 16]>,
    /// Which anchor of this name it is (`SourceSet::anchor_generation`).
    generation: u64,
    _peer: RailPeer,
}

pub struct RdmaSources {
    rails: Arc<Rails>,
    specs: Vec<String>,
    manifest_key: String,
    anchors: Mutex<HashMap<String, Arc<Anchor>>>,
    /// Server identity per source name, from its RAILS answer (kept after
    /// the anchor is released): two names of one server (IPv4 and IPv6)
    /// share it; a new server at a reused address has another.
    identities: Mutex<HashMap<String, String>>,
    next: AtomicUsize,
    /// Generations of anchors made (starts at 1: 0 means none).
    next_generation: std::sync::atomic::AtomicU64,
}

/// A server's identity, which does not depend on the address it was
/// reached at: its process instance id when it sends one (distinct for
/// two serve processes on one host, and for a replacement pod at the
/// same address); from a server that does not, its rails (GID and port
/// each, sorted). `None` when it sends neither.
fn server_identity(rails: &[RailInfo], instance: Option<[u8; 16]>) -> Option<String> {
    let hex = |b: &[u8]| b.iter().map(|x| format!("{x:02x}")).collect::<String>();
    if let Some(id) = instance {
        return Some(format!("instance:{}", hex(&id)));
    }
    if rails.is_empty() {
        return None;
    }
    let mut each: Vec<String> = rails
        .iter()
        .map(|r| format!("{}@{}", hex(&r.gid), r.port))
        .collect();
    each.sort();
    Some(format!("rails:{}", each.join(",")))
}

/// Split `HOST:PORT` (`[v6]:PORT` for IPv6 literals) and check the port.
pub fn check_spec(spec: &str) -> Result<(), String> {
    let (host, port) = spec
        .rsplit_once(':')
        .ok_or_else(|| format!("{spec:?}: expected HOST:PORT"))?;
    if host.is_empty() || port.parse::<u16>().is_err() {
        return Err(format!("{spec:?}: expected HOST:PORT"));
    }
    Ok(())
}

/// Every address `spec` resolves to, as `ip:port` / `[ip6]:port`.
pub fn resolve(spec: &str) -> Vec<String> {
    match spec.to_socket_addrs() {
        Ok(addrs) => addrs.map(|a| a.to_string()).collect(),
        Err(e) => {
            debug!(source = spec, error = %e, "does not resolve (yet)");
            Vec::new()
        }
    }
}

/// The TCP bootstrap address of each rail a source advertises: the
/// source's own host (the address the fetch was given) at each rail's
/// port. Never the rail's GID address: bootstrap traffic follows the
/// host's normal routes and needs no route to the rail subnets
/// (`docs/peer-copy.md`, "Rails, pairing and routes"). A source that advertises no
/// rails is its own single rail.
pub fn rail_addrs(source: &str, advertised: &[RailInfo]) -> Result<Vec<String>, String> {
    if advertised.is_empty() {
        return Ok(vec![source.to_string()]);
    }
    let (host, _) = source
        .rsplit_once(':')
        .ok_or_else(|| format!("{source}: expected host:port"))?;
    Ok(advertised
        .iter()
        .map(|r| format!("{host}:{}", r.port))
        .collect())
}

fn classify(e: HydrateError) -> OpenError {
    match &e {
        HydrateError::Connect(m) if bootstrap::is_busy(m) => OpenError::Busy(m.clone()),
        HydrateError::Connect(m) if bootstrap::is_rejected(m) => OpenError::Refused(m.clone()),
        _ => OpenError::Failed(e),
    }
}

/// A failed RAILS request of `source`. NO_SLOT (`GetError::Busy`: every
/// worker of the source is taken) is a busy source, skipped and tried
/// again after a pause, never counted as a failure that could mark it
/// dead.
fn classify_rails(source: &str, error: GetError) -> OpenError {
    match error {
        GetError::Busy => OpenError::Busy(format!("{source}: RAILS answered busy (NO_SLOT)")),
        error => OpenError::Failed(HydrateError::Get { offset: 0, error }),
    }
}

/// The bootstrap addresses of `source`'s rails from its RAILS answer;
/// a busy answer makes the source busy (see [`classify_rails`]).
fn rail_addrs_for(
    source: &str,
    advertised: Result<Vec<RailInfo>, GetError>,
) -> Result<Vec<String>, OpenError> {
    let advertised = advertised.map_err(|error| classify_rails(source, error))?;
    rail_addrs(source, &advertised).map_err(|e| OpenError::Failed(HydrateError::Invalid(e)))
}

impl RdmaSources {
    /// Sources named by `specs` (`HOST:PORT` each), fetching `prefix`.
    pub fn new(rails: Arc<Rails>, specs: Vec<String>, prefix: &str) -> Result<Self, String> {
        if specs.is_empty() {
            return Err("at least one --source is required".into());
        }
        for s in &specs {
            check_spec(s)?;
        }
        Ok(Self {
            rails,
            specs,
            manifest_key: manifest::key(prefix.trim_end_matches('/')),
            anchors: Mutex::new(HashMap::new()),
            identities: Mutex::new(HashMap::new()),
            next_generation: std::sync::atomic::AtomicU64::new(1),
            next: AtomicUsize::new(0),
        })
    }

    /// Close every anchor (the fetch is over).
    pub fn close(&self) {
        self.anchors.lock().clear();
    }

    fn anchor(&self, source: &str) -> Result<Arc<Anchor>, OpenError> {
        let mut anchors = self.anchors.lock();
        if let Some(a) = anchors.get(source) {
            return Ok(Arc::clone(a));
        }
        // The RAILS request also checks the path: a local NIC that cannot
        // reach the source's rail 0 is passed over for the next.
        let (peer, advertised) =
            connect_listing_rails(&self.rails, source, 0, &[], &self.manifest_key)
                .map_err(|e| classify(HydrateError::Connect(format!("{source}: {e}"))))?;
        // The probe's RAILS answer: busy makes the source busy (skipped
        // and tried again, never counted toward marking it dead).
        let (advertised, instance) = advertised.map_err(|error| classify_rails(source, error))?;
        if let Some(id) = server_identity(&advertised, instance) {
            self.identities.lock().insert(source.to_string(), id);
        }
        let rail_addrs = rail_addrs_for(source, Ok(advertised.clone()))?;
        let rail_gids = advertised.iter().map(|r| r.gid).collect();
        let generation = self.next_generation.fetch_add(1, Ordering::Relaxed);
        let a = Arc::new(Anchor {
            rail_addrs,
            rail_gids,
            generation,
            _peer: peer,
        });
        anchors.insert(source.to_string(), Arc::clone(&a));
        Ok(a)
    }
}

impl SourceSet for RdmaSources {
    type Source = RdmaSource;

    fn candidates(&self) -> Vec<String> {
        self.specs.iter().flat_map(|s| resolve(s)).collect()
    }

    fn open(&self, source: &str) -> Result<RdmaSource, OpenError> {
        let anchor = self.anchor(source)?;
        // Spread workers round-robin over the source's rails.
        let k = self.next.fetch_add(1, Ordering::Relaxed) % anchor.rail_addrs.len();
        RdmaSource::open_rail(&self.rails, &anchor.rail_addrs[k], k, &anchor.rail_gids)
            .map_err(classify)
    }

    fn release(&self, source: &str) {
        self.anchors.lock().remove(source);
    }

    fn identity(&self, source: &str) -> Option<String> {
        self.identities.lock().get(source).cloned()
    }

    fn anchor_generation(&self, source: &str) -> u64 {
        self.anchors.lock().get(source).map_or(0, |a| a.generation)
    }

    fn release_generation(&self, source: &str, generation: u64) {
        let mut anchors = self.anchors.lock();
        if anchors
            .get(source)
            .is_some_and(|a| a.generation == generation)
        {
            anchors.remove(source);
        }
    }

    fn reachable(&self, source: &str, timeout: std::time::Duration) -> bool {
        // `source` is an address (`ip:port`) from `candidates`; a plain TCP
        // connect to its bootstrap port, no RDMA handshake.
        source
            .to_socket_addrs()
            .ok()
            .and_then(|mut a| a.next())
            .is_some_and(|addr| std::net::TcpStream::connect_timeout(&addr, timeout).is_ok())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn specs_and_resolution() {
        for ok in ["10.0.0.1:9890", "[fd00::1]:9890", "localhost:1"] {
            assert!(check_spec(ok).is_ok(), "{ok}");
        }
        for bad in ["10.0.0.1", ":9890", "host:", "host:99999", "host:x"] {
            assert!(check_spec(bad).is_err(), "{bad}");
        }
        assert_eq!(resolve("127.0.0.1:9890"), ["127.0.0.1:9890"]);
        assert_eq!(resolve("[::1]:9890"), ["[::1]:9890"]);
        assert!(resolve("does-not-exist.invalid:9890").is_empty());
    }

    // The bootstrap of every rail goes to the source's own host, whatever
    // GID the rail advertises: no TCP connection to a rail address, so
    // no route to the rail subnets is needed.
    #[test]
    fn rail_bootstraps_go_to_the_source_host_not_the_rail_gid() {
        let gid = |a: [u8; 4]| std::net::Ipv4Addr::from(a).to_ipv6_mapped().octets();
        let rails = [
            RailInfo {
                port: 9890,
                gid: gid([192, 168, 0, 5]),
            },
            RailInfo {
                port: 9891,
                gid: gid([192, 168, 32, 5]),
            },
        ];
        assert_eq!(
            rail_addrs("10.0.0.7:9890", &rails).expect("addrs"),
            ["10.0.0.7:9890", "10.0.0.7:9891"]
        );
        assert_eq!(
            rail_addrs("[fd00::7]:9890", &rails).expect("addrs"),
            ["[fd00::7]:9890", "[fd00::7]:9891"]
        );
        assert_eq!(
            rail_addrs("src.example:9890", &rails).expect("addrs"),
            ["src.example:9890", "src.example:9891"]
        );
        assert_eq!(
            rail_addrs("10.0.0.7:9890", &[]).expect("addrs"),
            ["10.0.0.7:9890"]
        );
        assert!(rail_addrs("nohostport", &rails).is_err());
    }

    // A source too busy to answer RAILS is busy, not
    // failed (a failure counts toward marking it dead).
    #[test]
    fn a_busy_rails_answer_is_busy_not_failure() {
        assert!(matches!(
            rail_addrs_for("10.0.0.1:9890", Err(GetError::Busy)),
            Err(OpenError::Busy(_))
        ));
        assert!(matches!(
            rail_addrs_for("10.0.0.1:9890", Err(GetError::Server(2))),
            Err(OpenError::Failed(_))
        ));
        let rails = vec![
            RailInfo {
                port: 9890,
                gid: [0; 16],
            },
            RailInfo {
                port: 9891,
                gid: [0; 16],
            },
        ];
        assert_eq!(
            rail_addrs_for("10.0.0.1:9890", Ok(rails)).ok(),
            Some(vec![
                "10.0.0.1:9890".to_string(),
                "10.0.0.1:9891".to_string()
            ])
        );
        assert_eq!(
            rail_addrs_for("10.0.0.1:9890", Ok(vec![])).ok(),
            Some(vec!["10.0.0.1:9890".to_string()])
        );
        assert!(matches!(
            classify_rails("x:1", GetError::Busy),
            OpenError::Busy(_)
        ));
        assert!(matches!(
            classify_rails("x:1", GetError::Server(2)),
            OpenError::Failed(_)
        ));
        assert!(matches!(
            classify_rails("x:1", GetError::Transport("reset".into())),
            OpenError::Failed(_)
        ));
    }

    // A server's identity is its process instance id when
    // it sends one; two serve processes on one host (same rail GIDs,
    // other ports, other instance) are two servers, and so is a
    // replacement at the same address. Without an instance id (an older
    // server): its rails, ports included, in any order.
    #[test]
    fn server_identity_tells_two_servers_on_one_host_apart() {
        let r = |g: u8, port| RailInfo { port, gid: [g; 16] };
        let host = [r(1, 9890), r(2, 9891)];
        let copy = [r(1, 9990), r(2, 9991)];
        assert_ne!(
            server_identity(&host, Some([1; 16])),
            server_identity(&copy, Some([2; 16])),
            "two processes on one host"
        );
        assert_ne!(
            server_identity(&host, Some([1; 16])),
            server_identity(&host, Some([3; 16])),
            "a replacement at the same addresses"
        );
        assert_eq!(
            server_identity(&host, Some([1; 16])),
            server_identity(&[r(9, 1)], Some([1; 16])),
            "one process, whatever rail answered"
        );
        assert_ne!(
            server_identity(&host, None),
            server_identity(&copy, None),
            "older servers: ports count"
        );
        assert_eq!(
            server_identity(&host, None),
            server_identity(&[r(2, 9891), r(1, 9890)], None)
        );
        assert_ne!(
            server_identity(&[r(0, 9890)], None),
            server_identity(&[r(0, 9990)], None),
            "zero GIDs (unreadable) on two ports are two servers"
        );
        assert_eq!(server_identity(&[], None), None);
    }

    // The instance id a server sends, decoded from a real
    // RAILS reply, is what the identity is made of.
    #[test]
    fn identity_comes_from_the_decoded_rails_reply() {
        use nvstor_rdma::s3_rdma_proto::{Response, CONTROL_BUF_SIZE};
        let rails = vec![RailInfo {
            port: 9890,
            gid: [4; 16],
        }];
        let mut buf = vec![0u8; CONTROL_BUF_SIZE];
        let n = Response::Rails {
            req_id: 1,
            rails: rails.clone(),
            instance: Some([0xab; 16]),
        }
        .encode_into(&mut buf)
        .expect("encode");
        let Response::Rails {
            rails: got,
            instance,
            ..
        } = Response::decode(&buf[..n]).expect("decode")
        else {
            panic!("not a RAILS reply");
        };
        assert_eq!(
            server_identity(&got, instance),
            Some(format!("instance:{}", "ab".repeat(16)))
        );
        assert_ne!(
            server_identity(&got, instance),
            server_identity(&rails, None)
        );
    }

    #[test]
    fn busy_is_told_apart_from_failure() {
        let busy = HydrateError::Connect(format!("x:1: {}", bootstrap::BootstrapError::Busy));
        assert!(matches!(classify(busy), OpenError::Busy(_)));
        let rejected =
            |r| HydrateError::Connect(format!("x:1: {}", bootstrap::BootstrapError::Rejected(r)));
        assert!(matches!(
            classify(rejected(bootstrap::RejectReason::Credentials)),
            OpenError::Refused(_)
        ));
        // An older server that gives no reason: a credential refusal, as before.
        assert!(matches!(
            classify(rejected(bootstrap::RejectReason::Unspecified)),
            OpenError::Refused(_)
        ));
        // A server that could not set the connection up: retryable, not auth.
        assert!(matches!(
            classify(rejected(bootstrap::RejectReason::Setup)),
            OpenError::Failed(_)
        ));
        assert!(matches!(
            classify(rejected(bootstrap::RejectReason::Malformed)),
            OpenError::Failed(_)
        ));
        let unreachable = HydrateError::Connect(
            "x:1: TCP connect to x:1: Connection refused (os error 111)".into(),
        );
        assert!(
            matches!(classify(unreachable), OpenError::Failed(_)),
            "a TCP refusal is not a credential refusal"
        );
    }
}
