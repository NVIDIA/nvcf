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

//! Multi-NIC ("rail") RDMA: one `RdmaTransport` per ACTIVE NIC.
//!
//! A memory region belongs to one protection domain on one device, so a
//! pool cannot be shared across NICs without per-device keys on every
//! buffer. Instead each NIC is its own transport with its own pool, and
//! bandwidth scales by spreading connections across them.
//!
//! The server advertises one [`RailInfo`] (TCP bootstrap port + GID) per
//! NIC through the S3-over-RDMA `Rails` request. A client pairs each
//! connection with a local NIC by **reachability**, not by device index:
//! `mlx5_k` names follow PCIe enumeration, not cabling, so index k on one
//! host is often wired to a different fabric than index k on another.
//! For each server rail the client grades every NIC with a [`Reach`],
//! best first:
//!
//! 1. `SameSubnet`: the NIC has the server rail's GID, a non-default,
//!    gateway-less route on the NIC's netdev covers the server rail's
//!    IPv4 address (on-link), or, for two non-IPv4 GIDs (InfiniBand,
//!    IPv6), the 64-bit subnet prefixes match;
//! 2. `Routed`: the most specific route in the kernel's IPv4 main table
//!    for the address leaves through the NIC's netdev and is not a
//!    default route. Default routes are never evidence: a NIC carrying
//!    the default route (a front-end NIC with RoCE) would otherwise
//!    outrank every rail;
//! 3. `SamePlane`: routing says nothing, and the NIC's address is on the
//!    server rail's plane. The planes come from the server's own rail
//!    addresses (the `Rails` reply), clustered by shared leading bits
//!    within one address family ([`planes`]); a NIC sharing at least the
//!    plane prefix with the server rail is on its plane. Multi-plane rail
//!    fabrics number each plane by subnet:
//!    - IPv6 (an SR-IOV fabric): one /48 per rail, e.g. `fd00:1:3900::/48`
//!      is rail 0 on every node, with a /64 per node within it, and every
//!      rail's default route is equal;
//!    - IPv4 (RoCE NICs allocated to a pod through Kubernetes DRA): rail k
//!      is e.g. `192.168.(32·k).0/19`, but each pod interface has a /32 and
//!      the pod has no rail routes, so the routing table answers nothing;
//! 4. `SharedPrefix(bits)`: no plane information yet (the first
//!    connection, before the `Rails` reply, or a server with fewer than
//!    two rails of the family): the raw number of leading bits shared;
//! 5. `Unknown`: none of the above, including rails whose addresses do
//!    not split into planes clearly, link-local GIDs and a family mismatch.
//!
//! NICs are then ordered by (a NIC whose path to this server rail already
//! failed comes last, reach, fewest connections already on the NIC, the
//! NIC that connected to this rail before, position equal to the server
//! rail's position, position). When the best NIC is `Unknown` a warning
//! is logged once per server rail. Whatever inference says, each new
//! connection is probed, and a path failure (QP not reaching RTR, retries
//! exhausted, server setup failure) moves it to the next NIC and is
//! remembered (`Pairing`).

use std::net::Ipv4Addr;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use tracing::{info, warn};

use crate::device::{Access, RdmaDevice};
use crate::pool::MemoryPool;
use crate::transport::RdmaTransport;

/// One server NIC as advertised on the wire: where to dial its TCP
/// bootstrap and the GID its QPs use.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct RailInfo {
    pub port: u16,
    pub gid: [u8; 16],
}

/// How a local NIC can reach a remote GID. Ordered worst → best.
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum Reach {
    Unknown,
    /// No plane information (the server's rails are not known yet, or it
    /// has one): the two addresses share this many leading bits; more is
    /// better.
    SharedPrefix(u8),
    /// On the server rail's plane by address (see [`planes`]). Several
    /// NICs on one plane tie.
    SamePlane,
    /// A route other than the default sends the address out of the NIC.
    Routed,
    SameSubnet,
}

/// One row of the kernel's IPv4 main routing table. Addresses are in
/// host order (`u32::from(Ipv4Addr)`).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Route {
    pub iface: String,
    pub dest: u32,
    pub mask: u32,
    pub gateway: u32,
    pub metric: u32,
}

/// The IPv4 address inside an IPv4-mapped GID (`::ffff:a.b.c.d`), which
/// is how RoCE v2 GIDs carry IPv4.
pub fn gid_ipv4(gid: &[u8; 16]) -> Option<Ipv4Addr> {
    (gid[..10].iter().all(|&b| b == 0) && gid[10] == 0xff && gid[11] == 0xff)
        .then(|| Ipv4Addr::new(gid[12], gid[13], gid[14], gid[15]))
}

/// Parse `/proc/net/route`. Its address columns are the kernel's
/// network-order `__be32` printed as a host integer, so on the
/// little-endian hosts this runs on (x86_64, aarch64) the bytes of the
/// parsed value, low first, are the address octets in order. Routes not
/// flagged UP are skipped; malformed rows are ignored.
pub fn parse_routes(text: &str) -> Vec<Route> {
    const RTF_UP: u32 = 0x1;
    let hex = |s: &str| u32::from_str_radix(s, 16).ok();
    let addr = |v: u32| u32::from(Ipv4Addr::from(v.to_le_bytes()));
    text.lines()
        .skip(1)
        .filter_map(|line| {
            let f: Vec<&str> = line.split_whitespace().collect();
            if f.len() < 8 {
                return None;
            }
            let flags = hex(f[3])?;
            if flags & RTF_UP == 0 {
                return None;
            }
            Some(Route {
                iface: f[0].to_string(),
                dest: addr(hex(f[1])?),
                gateway: addr(hex(f[2])?),
                metric: f[6].parse().ok()?,
                mask: addr(hex(f[7])?),
            })
        })
        .collect()
}

/// The main-table route the kernel would use for `ip`: longest prefix,
/// then lowest metric.
pub fn best_route(routes: &[Route], ip: Ipv4Addr) -> Option<&Route> {
    let ip = u32::from(ip);
    routes
        .iter()
        .filter(|r| ip & r.mask == r.dest)
        .min_by_key(|r| (std::cmp::Reverse(r.mask.count_ones()), r.metric))
}

/// What a server's rail addresses say about its planes, for one of its
/// rails. See [`planes`].
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Planes {
    /// The server's rails are not known (the first connection, before its
    /// RAILS reply) or it has fewer than two of this address family.
    NoInfo,
    /// Its rails do not split into planes clearly (numbered within one
    /// subnet, say): addresses cannot tell planes apart.
    Unclear,
    /// A local NIC sharing at least this many leading bits with the
    /// server rail is on its plane.
    Prefix(u8),
}

/// Host bits a plane must leave (a plane holds many nodes): a plane
/// prefix longer than the family's bits minus this is not believed.
const MIN_HOST_BITS: i32 = 8;
/// Leading bits by which rails within a plane must be closer than rails
/// of different planes for the split to be believed.
const MIN_PLANE_GAP: i32 = 4;

/// Split a server's rails into planes and say which prefix length marks
/// the plane of rail `target`.
///
/// The rails of `target`'s address family are clustered by single
/// linkage on shared leading bits: for each candidate `b` (a pairwise
/// shared length), rails sharing more than `b` bits are one plane. The
/// candidate kept is the one with the widest gap between the weakest
/// link inside a plane and `b` (with every rail its own plane, between
/// `b` and the family's bits minus [`MIN_HOST_BITS`]). A gap under
/// [`MIN_PLANE_GAP`] means planes cannot be told apart.
pub fn planes(target: &[u8; 16], server_rails: &[[u8; 16]]) -> Planes {
    let mut same: Vec<[u8; 16]> = server_rails
        .iter()
        .copied()
        .filter(|g| family_shared_bits(g, target).is_some())
        .collect();
    same.sort();
    same.dedup();
    if same.len() < 2 {
        return Planes::NoInfo;
    }
    let bits: i32 = if gid_ipv4(target).is_some() { 32 } else { 128 };
    let mut edges = Vec::new();
    for i in 0..same.len() {
        for j in i + 1..same.len() {
            if let Some(shared) = family_shared_bits(&same[i], &same[j]) {
                edges.push((i, j, shared));
            }
        }
    }
    let mut values: Vec<u8> = edges.iter().map(|&(_, _, v)| v).collect();
    values.sort_unstable();
    values.dedup();
    let mut best: Option<(i32, u8)> = None;
    for (k, &b) in values.iter().enumerate() {
        if components(
            same.len(),
            edges.iter().filter(|e| e.2 > b).map(|e| (e.0, e.1)),
        ) < 2
        {
            continue;
        }
        let gap = match values.get(k + 1) {
            Some(&within) => i32::from(within) - i32::from(b),
            None => bits - MIN_HOST_BITS - i32::from(b),
        };
        if best.is_none_or(|(g, _)| gap > g) {
            best = Some((gap, b));
        }
    }
    match best {
        Some((gap, b)) if gap >= MIN_PLANE_GAP => Planes::Prefix(b.saturating_add(1)),
        _ => Planes::Unclear,
    }
}

/// Number of connected components of `n` nodes joined by `edges`.
fn components(n: usize, edges: impl Iterator<Item = (usize, usize)>) -> usize {
    let mut parent: Vec<usize> = (0..n).collect();
    fn root(p: &mut [usize], mut x: usize) -> usize {
        while p[x] != x {
            p[x] = p[p[x]];
            x = p[x];
        }
        x
    }
    let mut count = n;
    for (a, b) in edges {
        let (ra, rb) = (root(&mut parent, a), root(&mut parent, b));
        if ra != rb {
            parent[ra] = rb;
            count -= 1;
        }
    }
    count
}

/// How a NIC with `local_gid` (whose RoCE netdev is `netdev`) reaches
/// `remote_gid`, given what the server's rails say about planes.
///
/// Default routes are no evidence: a NIC that carries the default route
/// (a front-end NIC with RoCE) would otherwise outrank every rail.
pub fn reach(
    local_gid: &[u8; 16],
    netdev: Option<&str>,
    remote_gid: &[u8; 16],
    routes: &[Route],
    planes: Planes,
) -> Reach {
    if local_gid == remote_gid {
        return Reach::SameSubnet;
    }
    let by_plane = |shared: Option<u8>| match (shared, planes) {
        (None, _) | (Some(_), Planes::Unclear) => Reach::Unknown,
        (Some(bits), Planes::NoInfo) => Reach::SharedPrefix(bits),
        (Some(bits), Planes::Prefix(len)) if bits >= len => Reach::SamePlane,
        (Some(_), Planes::Prefix(_)) => Reach::Unknown,
    };
    match (gid_ipv4(local_gid), gid_ipv4(remote_gid)) {
        (Some(_), Some(remote)) => {
            let rip = u32::from(remote);
            let on_link = netdev.is_some_and(|netdev| {
                routes.iter().any(|r| {
                    r.iface == netdev && r.gateway == 0 && r.mask != 0 && rip & r.mask == r.dest
                })
            });
            let routed = netdev.is_some_and(|netdev| {
                best_route(routes, remote).is_some_and(|r| r.iface == netdev && r.mask != 0)
            });
            if on_link {
                Reach::SameSubnet
            } else if routed {
                Reach::Routed
            } else {
                // Routing says nothing for this NIC (a /32 per rail, no
                // rail routes): the plane, by address.
                by_plane(family_shared_bits(local_gid, remote_gid))
            }
        }
        (None, None) if local_gid[..8] == remote_gid[..8] => Reach::SameSubnet,
        _ => by_plane(family_shared_bits(local_gid, remote_gid)),
    }
}

/// Leading bits two addresses share within their address family (32 for
/// IPv4, 128 for routable IPv6); `None` across families, or for
/// link-local and InfiniBand GIDs.
fn family_shared_bits(a: &[u8; 16], b: &[u8; 16]) -> Option<u8> {
    match (gid_ipv4(a), gid_ipv4(b)) {
        (Some(x), Some(y)) => Some((u32::from(x) ^ u32::from(y)).leading_zeros() as u8),
        (None, None) if !is_link_local(a) && !is_link_local(b) => Some(shared_prefix_bits(a, b)),
        _ => None,
    }
}

/// `fe80::/10`: link-local IPv6, and the default InfiniBand subnet prefix.
fn is_link_local(gid: &[u8; 16]) -> bool {
    gid[0] == 0xfe && gid[1] & 0xc0 == 0x80
}

/// Number of leading bits two GIDs have in common.
fn shared_prefix_bits(a: &[u8; 16], b: &[u8; 16]) -> u8 {
    let mut bits = 0u8;
    for (x, y) in a.iter().zip(b) {
        let diff = x ^ y;
        if diff != 0 {
            return bits + diff.leading_zeros() as u8;
        }
        bits += 8;
    }
    bits
}

/// Rank a candidate NIC for one server rail; smaller is better.
fn rank(
    reach: Reach,
    load: usize,
    position: usize,
    server_rail: usize,
) -> (std::cmp::Reverse<Reach>, usize, bool, usize) {
    (
        std::cmp::Reverse(reach),
        load,
        position != server_rail,
        position,
    )
}

/// The rails among `rails` (netdev, GID) with an IPv4 GID whose
/// reverse-path filtering is strict: `rp_filter` 1, where the kernel
/// uses the larger of `conf/all` and `conf/<netdev>` (2 is loose, 0 off).
/// `read` returns the value for "all" or a netdev; unreadable counts as 0.
pub fn strict_rp_filter(
    rails: &[(String, [u8; 16])],
    read: impl Fn(&str) -> Option<u32>,
) -> Vec<String> {
    let all = read("all").unwrap_or(0);
    rails
        .iter()
        .filter(|(_, gid)| gid_ipv4(gid).is_some())
        .filter(|(dev, _)| all.max(read(dev).unwrap_or(0)) == 1)
        .map(|(dev, _)| dev.clone())
        .collect()
}

/// Warn, once per process, when a rail of this host filters reverse
/// paths strictly (see [`strict_rp_filter`]). A peer rail whose address
/// this host routes through another interface (a /32 per rail and no rail
/// routes) then has its ARP requests and packets dropped here, and its
/// QPs time out; see `docs/peer-copy.md`, "Rails, pairing and routes".
pub fn warn_strict_rp_filter(rails: &[(String, [u8; 16])]) {
    static WARNED: std::sync::atomic::AtomicBool = std::sync::atomic::AtomicBool::new(false);
    let read = |conf: &str| {
        std::fs::read_to_string(format!("/proc/sys/net/ipv4/conf/{conf}/rp_filter"))
            .ok()?
            .trim()
            .parse()
            .ok()
    };
    let strict = strict_rp_filter(rails, read);
    if !strict.is_empty() && !WARNED.swap(true, Ordering::Relaxed) {
        warn!(netdevs = ?strict,
              "strict reverse-path filtering (rp_filter=1) on RDMA rails: peers whose rail addresses this host \
               does not route through the same rail will have their traffic dropped here; set rp_filter to 2 \
               (loose) on the rails or add rail routes (docs/peer-copy.md, \"Rails, pairing and routes\")");
    }
}

/// Pick the local NIC for `server_rail`, given each NIC's reachability
/// and current connection count. `None` only when `reaches` is empty.
pub fn pick(reaches: &[Reach], loads: &[usize], server_rail: usize) -> Option<usize> {
    (0..reaches.len()).min_by_key(|&i| rank(reaches[i], loads[i], i, server_rail))
}

struct Nic {
    transport: Arc<RdmaTransport>,
    /// The NIC's GID and its netdev, as last read. Re-read while the GID
    /// is not routable (see [`needs_refresh`]).
    addr: parking_lot::Mutex<NicAddr>,
    /// Live connections; drives load balancing.
    connections: AtomicUsize,
    /// Connections ever opened, for reporting where traffic went.
    opened: AtomicUsize,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct NicAddr {
    gid: [u8; 16],
    netdev: Option<String>,
}

impl NicAddr {
    fn read(transport: &RdmaTransport) -> Self {
        let dev = transport.device();
        Self {
            gid: transport.local_gid(),
            netdev: dev.gid_netdev(dev.roce_gid_index()),
        }
    }
}

/// Whether a NIC's cached GID must be re-read before pairing: a port
/// that has only zero or link-local GIDs has not finished address
/// configuration (a pod started before SLAAC). A link-local GID pairs
/// with nothing, which silently degrades every connection to index
/// matching, possibly across planes.
fn needs_refresh(gid: &[u8; 16]) -> bool {
    !crate::device::gid_is_routable(gid)
}

/// The client's local NICs, one transport each.
pub struct Rails {
    nics: Vec<Nic>,
    /// Makes pick + reserve atomic. Workers connect concurrently, and the
    /// handshake takes milliseconds; counting a connection only once it is
    /// up would let every concurrent worker see the same loads and pile
    /// onto one NIC.
    placement: parking_lot::Mutex<()>,
    /// Which local NIC connected to which server rail, and which failed.
    pairing: Pairing,
}

/// Which RDMA devices to use.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum DeviceSelect {
    /// Every device with an ACTIVE port.
    AllActive,
    /// These devices, by name (`mlx5_3`), in this order. Each must exist
    /// and be ACTIVE.
    Named(Vec<String>),
    /// ACTIVE RoCE devices that belong to the caller's network namespace
    /// (see [`membership`]): their GID is an address of an interface here,
    /// or their netdev is an interface here (same name and MAC). In a pod
    /// that holds SR-IOV VFs this is exactly the pod's VFs, even though a
    /// privileged pod can open every device on the node. A VF still
    /// waiting for its routable GID (SLAAC) is waited for, then kept
    /// (see [`RailWait`]). Selects nothing on InfiniBand, whose GIDs are
    /// not IP addresses and whose netdevs have no Ethernet MAC.
    ThisNetns,
}

impl std::str::FromStr for DeviceSelect {
    type Err = String;

    /// `all`, `netns`, or a comma-separated list of device names.
    fn from_str(s: &str) -> Result<Self, String> {
        match s {
            "all" => Ok(Self::AllActive),
            "netns" => Ok(Self::ThisNetns),
            "" => Err("empty device list".into()),
            names => Ok(Self::Named(
                names.split(',').map(|n| n.trim().to_string()).collect(),
            )),
        }
    }
}

/// Environment variable that overrides [`RailWait::timeout`], in whole
/// seconds (`0` = do not wait).
pub const RAIL_WAIT_ENV: &str = "NVSTOR_RDMA_RAIL_WAIT_SECS";

/// How long [`open_devices`] waits, for [`DeviceSelect::ThisNetns`], for
/// this namespace's NICs to get a routable GID.
///
/// A pod's rail VFs are ACTIVE as soon as their link is up, but their
/// routable IPv6 GID appears only when SLAAC finishes, often seconds
/// after the pod starts. Such a VF is recognised as this namespace's by
/// its netdev (see [`membership`]) and kept; the wait only gives SLAAC
/// time to finish before the first connection.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct RailWait {
    /// Longest wait at startup. A NIC still without a routable GID after
    /// it is kept as pending, with a warning naming it: its GID is read
    /// again on every connection (`Rails::connect`, `RdmaTransport`
    /// HELLO), so it carries traffic once its address appears.
    pub timeout: Duration,
    /// How often the GID tables and interface addresses are re-read.
    pub poll: Duration,
}

impl Default for RailWait {
    fn default() -> Self {
        Self {
            timeout: Duration::from_secs(30),
            poll: Duration::from_millis(250),
        }
    }
}

impl RailWait {
    /// The default, with `timeout` taken from [`RAIL_WAIT_ENV`] when set.
    pub fn from_env() -> Result<Self, String> {
        Self::from_env_value(std::env::var(RAIL_WAIT_ENV).ok().as_deref())
    }

    fn from_env_value(v: Option<&str>) -> Result<Self, String> {
        let mut w = Self::default();
        if let Some(v) = v {
            let secs: u64 = v
                .trim()
                .parse()
                .map_err(|_| format!("{RAIL_WAIT_ENV}: not a whole number of seconds: {v:?}"))?;
            w.timeout = Duration::from_secs(secs);
        }
        Ok(w)
    }
}

/// Open the devices `select` names. For [`DeviceSelect::ThisNetns`],
/// waits as [`RailWait::from_env`] says for this namespace's NICs to get
/// a routable GID.
pub fn open_devices(select: &DeviceSelect) -> Result<Vec<RdmaDevice>, String> {
    open_devices_with(select, &RailWait::from_env()?)
}

/// [`open_devices`] with an explicit startup wait.
pub fn open_devices_with(
    select: &DeviceSelect,
    wait: &RailWait,
) -> Result<Vec<RdmaDevice>, String> {
    let active = RdmaDevice::open_all();
    match select {
        DeviceSelect::AllActive => Ok(active),
        DeviceSelect::Named(names) => {
            let mut by_name: std::collections::HashMap<String, RdmaDevice> =
                active.into_iter().map(|d| (d.name.clone(), d)).collect();
            names
                .iter()
                .map(|n| {
                    by_name.remove(n).ok_or_else(|| {
                        format!("RDMA device {n}: not found, not ACTIVE, or listed twice")
                    })
                })
                .collect()
        }
        DeviceSelect::ThisNetns => {
            let selected = select_netns(active, NetnsView::read, wait, std::thread::sleep)?;
            Ok(selected.into_iter().map(|(d, _)| d).collect())
        }
    }
}

/// What rail selection reads about one NIC. A trait so tests can fake
/// the GID table and netdevs.
pub trait NicView {
    fn name(&self) -> &str;
    /// The GID its QPs would use now (the best entry of the GID table).
    fn best_gid(&self) -> [u8; 16];
    /// Every non-zero entry of its GID table, link-local ones included.
    fn gids(&self) -> Vec<[u8; 16]>;
    /// Its Ethernet netdevs: name and MAC (None when not 6 bytes or
    /// unreadable).
    fn netdevs(&self) -> Vec<(String, Option<[u8; 6]>)>;
}

impl NicView for RdmaDevice {
    fn name(&self) -> &str {
        &self.name
    }

    fn best_gid(&self) -> [u8; 16] {
        // SAFETY: every ibv_gid variant is 16 plain bytes.
        self.gid(i32::from(self.roce_gid_index()))
            .map(|g| unsafe { g.raw })
            .unwrap_or_default()
    }

    fn gids(&self) -> Vec<[u8; 16]> {
        self.gid_table()
    }

    /// From `device/net/` (the PCI function's netdevs) and the GID
    /// table's `ndevs`, with each MAC from sysfs.
    fn netdevs(&self) -> Vec<(String, Option<[u8; 6]>)> {
        let base = format!("/sys/class/infiniband/{}", self.name);
        let mut names: Vec<String> = std::fs::read_dir(format!("{base}/device/net"))
            .map(|d| {
                d.filter_map(|e| e.ok()?.file_name().into_string().ok())
                    .collect()
            })
            .unwrap_or_default();
        for i in 0..self.gid_table_len().clamp(1, 256) {
            if let Some(n) = self.gid_netdev(i as u8) {
                if !names.contains(&n) {
                    names.push(n);
                }
            }
        }
        names
            .into_iter()
            .map(|n| {
                let mac = std::fs::read_to_string(format!("{base}/device/net/{n}/address"))
                    .or_else(|_| std::fs::read_to_string(format!("/sys/class/net/{n}/address")))
                    .ok()
                    .and_then(|t| parse_mac(&t));
                (n, mac)
            })
            .collect()
    }
}

/// `aa:bb:cc:dd:ee:ff` → bytes. Anything else (an IPoIB 20-byte address
/// included) → None.
pub fn parse_mac(text: &str) -> Option<[u8; 6]> {
    let mut mac = [0u8; 6];
    let mut parts = text.trim().split(':');
    for b in &mut mac {
        *b = u8::from_str_radix(parts.next()?, 16).ok()?;
    }
    parts.next().is_none().then_some(mac)
}

/// This network namespace's interfaces: their addresses in GID form
/// (IPv4 as `::ffff:a.b.c.d`) and their (name, MAC) links.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct NetnsView {
    pub addrs: Vec<[u8; 16]>,
    pub links: Vec<(String, Option<[u8; 6]>)>,
}

impl NetnsView {
    /// Read with getifaddrs.
    pub fn read() -> std::io::Result<Self> {
        let mut head: *mut libc::ifaddrs = std::ptr::null_mut();
        // SAFETY: getifaddrs fills `head` with a list freed by freeifaddrs.
        if unsafe { libc::getifaddrs(&mut head) } != 0 {
            return Err(std::io::Error::last_os_error());
        }
        let mut view = Self::default();
        let mut cur = head;
        while !cur.is_null() {
            // SAFETY: `cur` is a node of the list getifaddrs returned.
            let ifa = unsafe { &*cur };
            if !ifa.ifa_addr.is_null() {
                // SAFETY: ifa_addr points at a sockaddr of the family it
                // names, and ifa_name at a NUL-terminated name.
                match i32::from(unsafe { (*ifa.ifa_addr).sa_family }) {
                    libc::AF_INET => {
                        let sin = unsafe { &*(ifa.ifa_addr as *const libc::sockaddr_in) };
                        view.addrs.push(
                            Ipv4Addr::from(u32::from_be(sin.sin_addr.s_addr))
                                .to_ipv6_mapped()
                                .octets(),
                        );
                    }
                    libc::AF_INET6 => {
                        let sin6 = unsafe { &*(ifa.ifa_addr as *const libc::sockaddr_in6) };
                        view.addrs.push(sin6.sin6_addr.s6_addr);
                    }
                    libc::AF_PACKET if !ifa.ifa_name.is_null() => {
                        let sll = unsafe { &*(ifa.ifa_addr as *const libc::sockaddr_ll) };
                        let name = unsafe { std::ffi::CStr::from_ptr(ifa.ifa_name) }
                            .to_string_lossy()
                            .into_owned();
                        let mac = (sll.sll_halen == 6).then(|| {
                            let mut m = [0u8; 6];
                            m.copy_from_slice(&sll.sll_addr[..6]);
                            m
                        });
                        view.links.push((name, mac));
                    }
                    _ => {}
                }
            }
            cur = ifa.ifa_next;
        }
        // SAFETY: `head` came from getifaddrs and is freed once.
        unsafe { libc::freeifaddrs(head) };
        Ok(view)
    }
}

/// Addresses of this network namespace's interfaces in GID form (IPv4 as
/// `::ffff:a.b.c.d`).
pub fn interface_gids() -> std::io::Result<Vec<[u8; 16]>> {
    NetnsView::read().map(|v| v.addrs)
}

/// Whether a NIC belongs to this network namespace, and whether it is
/// ready to carry connections.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Membership {
    /// Not one of this namespace's NICs (a PF, another pod's VF, an
    /// InfiniBand port).
    Elsewhere,
    /// This namespace's NIC, without a routable GID yet.
    Pending,
    /// This namespace's NIC with a routable GID.
    Ready,
}

/// Classify `nic` against this namespace. It is this namespace's when
///
/// 1. its routable GID is an address of an interface here (the only test
///    that alone would drop every VF still waiting for SLAAC), or
/// 2. any entry of its GID table, link-local ones included, is such an
///    address, or
/// 3. one of its netdevs is an interface here with the same name and
///    MAC. This holds from the moment the VF is moved into the namespace,
///    whatever addresses it has; the MAC keeps a same-named interface in
///    another namespace from matching, and IPoIB netdevs (20-byte
///    addresses) never match, so InfiniBand ports stay excluded.
pub fn membership(nic: &impl NicView, ns: &NetnsView) -> Membership {
    let best = nic.best_gid();
    let routable = crate::device::gid_is_routable(&best);
    if routable && ns.addrs.contains(&best) {
        return Membership::Ready;
    }
    let by_gid = nic
        .gids()
        .iter()
        .any(|g| g.iter().any(|&b| b != 0) && ns.addrs.contains(g));
    let by_netdev = nic
        .netdevs()
        .iter()
        .any(|(name, mac)| mac.is_some() && ns.links.iter().any(|(n, m)| n == name && m == mac));
    match (by_gid || by_netdev, routable) {
        (false, _) => Membership::Elsewhere,
        (true, true) => Membership::Ready,
        (true, false) => Membership::Pending,
    }
}

/// The NICs of `nics` that belong to this namespace, waiting up to
/// `wait.timeout` for every one of them to be [`Membership::Ready`].
/// Pending NICs left after the wait are kept, with a warning naming each.
/// `read_ns` and `sleep` are injected for tests.
pub fn select_netns<N: NicView>(
    nics: Vec<N>,
    mut read_ns: impl FnMut() -> std::io::Result<NetnsView>,
    wait: &RailWait,
    mut sleep: impl FnMut(Duration),
) -> Result<Vec<(N, Membership)>, String> {
    let started = Instant::now();
    let mut announced = false;
    loop {
        let ns = read_ns().map_err(|e| format!("listing interface addresses: {e}"))?;
        let classes: Vec<Membership> = nics.iter().map(|n| membership(n, &ns)).collect();
        let pending: Vec<&str> = nics
            .iter()
            .zip(&classes)
            .filter(|(_, &c)| c == Membership::Pending)
            .map(|(n, _)| n.name())
            .collect();
        let waited = started.elapsed();
        if pending.is_empty() || waited >= wait.timeout {
            for name in &pending {
                warn!(device = %name, waited_ms = waited.as_millis() as u64,
                      "RDMA rail in this network namespace has no routable GID yet; keeping it, \
                       its GID is re-read on every connection");
            }
            if announced && pending.is_empty() {
                info!(
                    waited_ms = waited.as_millis() as u64,
                    "every RDMA rail in this network namespace has a routable GID"
                );
            }
            return Ok(nics
                .into_iter()
                .zip(classes)
                .filter(|(_, c)| *c != Membership::Elsewhere)
                .collect());
        }
        if !announced {
            info!(devices = ?pending, timeout_s = wait.timeout.as_secs(),
                  "waiting for RDMA rails in this network namespace to get a routable GID");
            announced = true;
        }
        sleep(wait.poll.min(wait.timeout.saturating_sub(waited)));
    }
}

impl Rails {
    /// One transport per ACTIVE NIC, each with a pool of `slots_per_rail`
    /// registered `slot_size` buffers, authenticating and signing with
    /// `creds`. The pools are GET destinations only: servers may
    /// RDMA-WRITE into them and nothing else.
    pub fn open(
        slot_size: usize,
        slots_per_rail: usize,
        creds: &crate::auth::Credentials,
    ) -> Result<Arc<Self>, String> {
        Self::open_selected(&DeviceSelect::AllActive, slot_size, slots_per_rail, creds)
    }

    /// `open`, over the devices `select` names.
    pub fn open_selected(
        select: &DeviceSelect,
        slot_size: usize,
        slots_per_rail: usize,
        creds: &crate::auth::Credentials,
    ) -> Result<Arc<Self>, String> {
        let mut transports = Vec::new();
        for dev in open_devices(select)? {
            let dev = Arc::new(dev);
            let pool =
                MemoryPool::with_access(&dev, slot_size, slots_per_rail, Access::REMOTE_WRITE)
                    .map_err(|e| format!("{}: pool: {e}", dev.name))?;
            let t = Arc::new(RdmaTransport::new(dev, Arc::new(pool)));
            t.set_credentials(creds.clone());
            transports.push(t);
        }
        if transports.is_empty() {
            return Err(format!("no RDMA device selected ({select:?})"));
        }
        Ok(Self::from_transports(transports))
    }

    /// Rails over already-built transports (one per NIC).
    pub fn from_transports(transports: Vec<Arc<RdmaTransport>>) -> Arc<Self> {
        let nics = transports.into_iter()
            .map(|transport| {
                let addr = NicAddr::read(&transport);
                info!(device = %transport.device().name, netdev = addr.netdev.as_deref().unwrap_or("-"),
                      gid = %std::net::Ipv6Addr::from(addr.gid), "RDMA rail");
                if needs_refresh(&addr.gid) {
                    warn!(device = %transport.device().name,
                          "RDMA rail has no routable GID yet; re-read on each connection until it does");
                }
                Nic { transport, addr: parking_lot::Mutex::new(addr),
                      connections: AtomicUsize::new(0), opened: AtomicUsize::new(0) }
            })
            .collect();
        Arc::new(Self {
            nics,
            placement: parking_lot::Mutex::new(()),
            pairing: Pairing::default(),
        })
    }

    /// Each NIC's current (GID, netdev), re-reading any whose cached GID
    /// is not yet routable.
    fn addrs(&self) -> Vec<NicAddr> {
        self.nics.iter()
            .map(|n| {
                let mut a = n.addr.lock();
                if needs_refresh(&a.gid) {
                    let fresh = NicAddr::read(&n.transport);
                    if fresh != *a {
                        info!(device = %n.transport.device().name,
                              gid = %std::net::Ipv6Addr::from(fresh.gid), "RDMA rail address changed");
                        *a = fresh;
                    }
                }
                a.clone()
            })
            .collect()
    }

    pub fn len(&self) -> usize {
        self.nics.len()
    }

    /// (netdev, GID) of each rail whose netdev is known.
    pub fn netdevs(&self) -> Vec<(String, [u8; 16])> {
        self.addrs()
            .into_iter()
            .filter_map(|a| Some((a.netdev?, a.gid)))
            .collect()
    }

    pub fn is_empty(&self) -> bool {
        self.nics.is_empty()
    }

    pub fn transport(&self, rail: usize) -> Option<&Arc<RdmaTransport>> {
        self.nics.get(rail).map(|n| &n.transport)
    }

    /// (device name, connections ever opened on it), per local NIC.
    pub fn opened(&self) -> Vec<(String, usize)> {
        self.nics
            .iter()
            .map(|n| {
                (
                    n.transport.device().name.clone(),
                    n.opened.load(Ordering::Relaxed),
                )
            })
            .collect()
    }

    /// Shut down every rail's transport.
    pub fn shutdown(&self) {
        for n in &self.nics {
            n.transport.shutdown();
        }
    }

    /// Connect to the server rail listening at `addr` (position
    /// `server_rail` in its advertised list) on the best local NIC for
    /// it. The connection counts against that NIC until dropped.
    pub fn connect(self: &Arc<Self>, addr: &str, server_rail: usize) -> Result<RailPeer, String> {
        self.connect_rail(addr, server_rail, &[])
    }

    /// [`Self::connect`], knowing the GIDs of all the server's rails
    /// (its RAILS reply): they tell which local NIC is on the server
    /// rail's plane when routing does not (see [`planes`]).
    pub fn connect_rail(
        self: &Arc<Self>,
        addr: &str,
        server_rail: usize,
        server_rails: &[[u8; 16]],
    ) -> Result<RailPeer, String> {
        self.connect_rail_probed(addr, server_rail, server_rails, &mut |_, _| Ok(()))
    }

    /// [`Self::connect_rail`], with `probe` run on each new connection
    /// before it is accepted (one control round trip, say): a connection
    /// whose path does not work fails there, and the next local NIC is
    /// tried. See [`Pairing`].
    pub fn connect_rail_probed(
        self: &Arc<Self>,
        addr: &str,
        server_rail: usize,
        server_rails: &[[u8; 16]],
        probe: &mut ConnProbe<'_>,
    ) -> Result<RailPeer, String> {
        // Routes and addresses are read now, not at startup: a pod's rail
        // interfaces may get their addresses and routes after we start.
        let routes = std::fs::read_to_string("/proc/net/route")
            .map(|t| parse_routes(&t))
            .unwrap_or_default();
        let nics: Vec<NicAddr> = self.addrs();
        let loads: Vec<&AtomicUsize> = self.nics.iter().map(|n| &n.connections).collect();
        let names: Vec<String> = self
            .nics
            .iter()
            .map(|n| n.transport.device().name.clone())
            .collect();
        let mut link = RdmaLink {
            rails: self,
            addr,
            dialed: None,
            probe,
        };
        let target = Target {
            addr,
            server_rail,
            server_rails,
            names: &names,
        };
        let (rail, peer) =
            self.pairing
                .connect(&mut link, &nics, &loads, &self.placement, &routes, &target)?;
        self.nics[rail].opened.fetch_add(1, Ordering::Relaxed);
        Ok(RailPeer {
            rails: Arc::clone(self),
            rail,
            peer,
        })
    }
}

/// A check run on a new connection (transport, peer id) before it is
/// accepted; an error that [`is_path_error`] recognises moves the
/// connection to the next local NIC.
pub type ConnProbe<'a> = dyn FnMut(&Arc<RdmaTransport>, u64) -> Result<(), String> + 'a;

/// One server rail to connect to, as [`Pairing::connect`] sees it.
pub(crate) struct Target<'a> {
    addr: &'a str,
    server_rail: usize,
    server_rails: &'a [[u8; 16]],
    /// Local NIC names, for logs.
    names: &'a [String],
}

/// The steps of one connection attempt to a server rail, faked in tests.
pub(crate) trait RailLink {
    type Conn;
    /// Open the TCP bootstrap; returns the server rail's GID.
    fn dial(&mut self) -> Result<[u8; 16], String>;
    /// Finish the connection on local NIC `nic` and check its path.
    fn connect(&mut self, nic: usize) -> Result<Self::Conn, String>;
}

/// The real link: TCP bootstrap, QPs on the chosen NIC, then `probe`.
struct RdmaLink<'a> {
    rails: &'a Rails,
    addr: &'a str,
    /// The bootstrap opened by `dial`, used by the first attempt; later
    /// attempts dial again.
    dialed: Option<crate::transport::Dialed>,
    probe: &'a mut ConnProbe<'a>,
}

impl RailLink for RdmaLink<'_> {
    type Conn = u64;

    fn dial(&mut self) -> Result<[u8; 16], String> {
        let d = RdmaTransport::dial(self.addr)?;
        let gid = d.server_gid();
        self.dialed = Some(d);
        Ok(gid)
    }

    fn connect(&mut self, nic: usize) -> Result<u64, String> {
        let dialed = match self.dialed.take() {
            Some(d) => d,
            None => RdmaTransport::dial(self.addr)?,
        };
        let transport = &self.rails.nics[nic].transport;
        let peer = transport.connect_dialed(dialed, self.addr)?;
        if let Err(e) = (self.probe)(transport, peer) {
            transport.disconnect(peer);
            return Err(e);
        }
        Ok(peer)
    }
}

/// Whether a failed connection attempt points at the path between this
/// local NIC and the server rail (wrong plane, no neighbour), so another
/// local NIC may work: the QP could not reach RTR (no route, no ARP/ND
/// answer), its first message was never acknowledged, or the server
/// could not set up its side (its own RTR toward us failed).
pub fn is_path_error(message: &str) -> bool {
    let rtr = message
        .split("QP to RTR failed: ")
        .nth(1)
        .and_then(|rest| rest.split(|c: char| !c.is_ascii_digit()).next())
        .and_then(|n| n.parse::<i32>().ok())
        .is_some_and(|errno| {
            matches!(
                errno,
                libc::ENETUNREACH
                    | libc::EHOSTUNREACH
                    | libc::ETIMEDOUT
                    | libc::ENODATA
                    | libc::EADDRNOTAVAIL
            )
        });
    rtr || message.contains("RETRY_EXC_ERR") && !message.contains("RNR_RETRY_EXC_ERR")
        || message.contains("poll timeout")
        || message.contains("server could not set up the connection")
}

/// Pairing memory: per server rail GID, the local NIC that connected
/// and the ones whose path failed. Kept for the life of the process.
#[derive(Default)]
pub(crate) struct Pairing {
    memory: parking_lot::Mutex<PairMemory>,
    /// Server rails already warned about (no NIC known to reach them).
    warned: parking_lot::Mutex<std::collections::HashSet<[u8; 16]>>,
}

#[derive(Default)]
struct PairMemory {
    good: std::collections::HashMap<[u8; 16], usize>,
    bad: std::collections::HashSet<([u8; 16], usize)>,
}

impl Pairing {
    /// Connect to one server rail. Local NICs are ranked by (NICs whose
    /// path to this rail failed before come last, reachability, load, the
    /// NIC that connected to it before, position); each is tried at most
    /// once, and a failure that points at the path ([`is_path_error`])
    /// moves on to the next. Whatever address inference says, a
    /// connection never stays on a NIC that cannot reach the rail, and a
    /// pair that failed is not tried first again.
    pub(crate) fn connect<L: RailLink>(
        &self,
        link: &mut L,
        nics: &[NicAddr],
        loads: &[&AtomicUsize],
        placement: &parking_lot::Mutex<()>,
        routes: &[Route],
        target: &Target<'_>,
    ) -> Result<(usize, L::Conn), String> {
        let remote_gid = link.dial()?;
        let planes = planes(&remote_gid, target.server_rails);
        let reaches: Vec<Reach> = nics
            .iter()
            .map(|a| reach(&a.gid, a.netdev.as_deref(), &remote_gid, routes, planes))
            .collect();
        let remote = std::net::Ipv6Addr::from(remote_gid);
        let mut tried = std::collections::HashSet::new();
        let mut failures = Vec::new();
        loop {
            let nic = {
                let _placing = placement.lock();
                let memory = self.memory.lock();
                let good = memory.good.get(&remote_gid).copied();
                let Some(nic) = (0..nics.len())
                    .filter(|i| !tried.contains(i))
                    .min_by_key(|&i| {
                        let load = loads[i].load(Ordering::Relaxed);
                        (
                            memory.bad.contains(&(remote_gid, i)),
                            std::cmp::Reverse(reaches[i]),
                            load,
                            good != Some(i),
                            i != target.server_rail,
                            i,
                        )
                    })
                else {
                    break;
                };
                loads[nic].fetch_add(1, Ordering::Relaxed);
                nic
            };
            // The first connection to a server, before its rail list is
            // known, has no plane information: not worth a warning.
            if tried.is_empty()
                && reaches[nic] == Reach::Unknown
                && !target.server_rails.is_empty()
                && self.warned.lock().insert(remote_gid)
            {
                warn!(addr = target.addr, %remote, ?planes,
                      "no local RDMA NIC is on-link, routed (not by default) or on the same plane as this server \
                       rail by address (planes cannot be told apart); trying NICs by load. Warned once per \
                       server rail");
            }
            tried.insert(nic);
            let name = target.names.get(nic).map(String::as_str).unwrap_or("?");
            match link.connect(nic) {
                Ok(conn) => {
                    let mut memory = self.memory.lock();
                    memory.bad.remove(&(remote_gid, nic));
                    if memory.good.insert(remote_gid, nic) != Some(nic) {
                        info!(addr = target.addr, %remote, device = name, attempts = tried.len(),
                              "RDMA rail paired: this local NIC reaches this server rail");
                    }
                    return Ok((nic, conn));
                }
                Err(e) => {
                    loads[nic].fetch_sub(1, Ordering::Relaxed);
                    if !is_path_error(&e) {
                        return Err(e);
                    }
                    warn!(addr = target.addr, %remote, device = name, error = %e,
                          "this local NIC cannot reach the server rail; trying the next one");
                    let mut memory = self.memory.lock();
                    memory.bad.insert((remote_gid, nic));
                    if memory.good.get(&remote_gid) == Some(&nic) {
                        memory.good.remove(&remote_gid);
                    }
                    failures.push(format!("{name}: {e}"));
                }
            }
        }
        Err(format!(
            "no local RDMA NIC could connect to server rail {} ({remote}): {}",
            target.addr,
            failures.join("; ")
        ))
    }
}

/// A peer connection on one local rail. Dropping it disconnects the
/// peer and releases the rail's connection count.
pub struct RailPeer {
    rails: Arc<Rails>,
    rail: usize,
    peer: u64,
}

impl RailPeer {
    pub fn rail(&self) -> usize {
        self.rail
    }

    pub fn peer_id(&self) -> u64 {
        self.peer
    }

    pub fn transport(&self) -> &Arc<RdmaTransport> {
        &self.rails.nics[self.rail].transport
    }
}

impl Drop for RailPeer {
    fn drop(&mut self) {
        let nic = &self.rails.nics[self.rail];
        nic.transport.disconnect(self.peer);
        nic.connections.fetch_sub(1, Ordering::Relaxed);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn v4(a: [u8; 4]) -> [u8; 16] {
        let mut g = [0u8; 16];
        g[10] = 0xff;
        g[11] = 0xff;
        g[12..].copy_from_slice(&a);
        g
    }

    const ROUTE_TABLE: &str = "\
Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT
eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0
eth0\t0001A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0
rail0\t00000A0A\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0
rail1\t00000B0A\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0
rail1\t00000C0A\t01000B0A\t0003\t0\t0\t0\t0000FFFF\t0\t0\t0
down0\t00000D0A\t00000000\t0000\t0\t0\t0\t0000FFFF\t0\t0\t0
";

    #[test]
    fn parses_proc_net_route() {
        let r = parse_routes(ROUTE_TABLE);
        assert_eq!(r.len(), 5, "header and non-UP rows skipped");
        assert_eq!(
            r[0],
            Route {
                iface: "eth0".into(),
                dest: 0,
                mask: 0,
                gateway: u32::from(Ipv4Addr::new(192, 168, 1, 1)),
                metric: 100
            }
        );
        assert_eq!(r[2].dest, u32::from(Ipv4Addr::new(10, 10, 0, 0)));
        assert_eq!(r[2].mask, u32::from(Ipv4Addr::new(255, 255, 0, 0)));
        assert!(parse_routes("header only\n").is_empty());
        assert!(parse_routes("h\nshort row\n").is_empty());
    }

    #[test]
    fn best_route_is_longest_prefix() {
        let r = parse_routes(ROUTE_TABLE);
        assert_eq!(
            best_route(&r, Ipv4Addr::new(10, 11, 3, 4)).map(|r| r.iface.as_str()),
            Some("rail1")
        );
        assert_eq!(
            best_route(&r, Ipv4Addr::new(8, 8, 8, 8)).map(|r| r.iface.as_str()),
            Some("eth0")
        );
    }

    #[test]
    fn gid_ipv4_only_for_mapped_gids() {
        assert_eq!(
            gid_ipv4(&v4([10, 1, 2, 3])),
            Some(Ipv4Addr::new(10, 1, 2, 3))
        );
        let mut link_local = [0u8; 16];
        link_local[0] = 0xfe;
        link_local[1] = 0x80;
        assert_eq!(gid_ipv4(&link_local), None);
    }

    #[test]
    fn reach_prefers_on_link_then_routed() {
        let r = parse_routes(ROUTE_TABLE);
        let rail0 = v4([10, 10, 0, 5]);
        let rail1 = v4([10, 11, 0, 5]);
        // Server NIC on 10.11/16: rail1 is on-link, rail0 is not.
        let server = v4([10, 11, 0, 9]);
        // The server's rails on 10.10/16 and 10.11/16: planes are /16.
        let plane = planes(
            &v4([10, 11, 0, 9]),
            &[v4([10, 10, 0, 9]), v4([10, 11, 0, 9])],
        );
        assert_eq!(plane, Planes::Prefix(16));
        assert_eq!(
            reach(&rail1, Some("rail1"), &server, &r, plane),
            Reach::SameSubnet
        );
        // Not routed through rail0, and on another plane.
        assert_eq!(
            reach(&rail0, Some("rail0"), &server, &r, plane),
            Reach::Unknown
        );
        // 10.12/16 is reachable only via a gateway on rail1.
        let far = v4([10, 12, 0, 9]);
        assert_eq!(reach(&rail1, Some("rail1"), &far, &r, plane), Reach::Routed);
        assert_eq!(
            reach(&rail0, Some("rail0"), &far, &r, plane),
            Reach::Unknown
        );
        // No netdev known (sysfs missing): routing cannot judge, the
        // plane still can; without plane information, the raw prefix;
        // with unclear planes, nothing.
        assert_eq!(reach(&rail1, None, &server, &r, plane), Reach::SamePlane);
        assert_eq!(
            reach(&rail1, None, &server, &r, Planes::NoInfo),
            Reach::SharedPrefix(28)
        );
        assert_eq!(
            reach(&rail1, None, &server, &r, Planes::Unclear),
            Reach::Unknown
        );
        assert!(
            Reach::Unknown < Reach::SharedPrefix(0)
                && Reach::SharedPrefix(32) < Reach::SamePlane
                && Reach::SamePlane < Reach::Routed
        );
        // Loopback: same GID.
        assert_eq!(
            reach(&rail0, None, &rail0, &[], Planes::NoInfo),
            Reach::SameSubnet
        );
    }

    // A default route through an RDMA NIC (a front-end NIC with RoCE) is
    // no plane evidence: it must not outrank the NIC on the server rail's
    // plane, or every connection piles onto the front-end NIC.
    #[test]
    fn a_default_route_through_a_nic_is_not_evidence() {
        let routes = parse_routes(
            "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n\
            fe0\t00000000\t0101000A\t0003\t0\t0\t0\t00000000\t0\t0\t0\n",
        );
        let server_rails = [v4([192, 168, 0, 9]), v4([192, 168, 32, 9])];
        let plane = planes(&server_rails[1], &server_rails);
        let front_end = v4([10, 0, 0, 5]);
        let rail1 = v4([192, 168, 32, 5]);
        assert_eq!(
            reach(&front_end, Some("fe0"), &server_rails[1], &routes, plane),
            Reach::Unknown
        );
        assert_eq!(
            reach(&rail1, Some("ens1"), &server_rails[1], &routes, plane),
            Reach::SamePlane
        );
        assert_eq!(
            pick(&[Reach::Unknown, Reach::SamePlane], &[0, 9], 0),
            Some(1)
        );
        // A specific (non-default) route through a NIC still counts.
        let mut specific = routes.clone();
        specific.push(Route {
            iface: "fe0".into(),
            dest: u32::from(Ipv4Addr::new(192, 168, 32, 0)),
            mask: 0xffff_ff00,
            gateway: u32::from(Ipv4Addr::new(10, 0, 0, 1)),
            metric: 0,
        });
        assert_eq!(
            reach(&front_end, Some("fe0"), &server_rails[1], &specific, plane),
            Reach::Routed
        );
    }

    #[test]
    fn reach_for_infiniband_gids_compares_subnet_prefix() {
        let mut a = [0u8; 16];
        a[0] = 0xfe;
        a[1] = 0x80;
        a[15] = 1;
        let mut b = a;
        b[15] = 2;
        assert_eq!(reach(&a, None, &b, &[], Planes::NoInfo), Reach::SameSubnet);
        b[7] = 9;
        assert_eq!(reach(&a, None, &b, &[], Planes::Prefix(1)), Reach::Unknown);
        // Mixed IPv4-mapped and IB never match.
        assert_eq!(
            reach(&v4([10, 0, 0, 1]), Some("x"), &a, &[], Planes::Prefix(1)),
            Reach::Unknown
        );
    }

    fn v6(text: &str) -> [u8; 16] {
        text.parse::<std::net::Ipv6Addr>().expect("ipv6").octets()
    }

    /// SR-IOV GPU rails: one plane prefix per rail, one /64 per node in
    /// it, and the client's VFs enumerated in a different order than the
    /// server's. Every server rail must land on the client VF of its plane.
    #[test]
    fn ipv6_rails_pair_by_plane_prefix() {
        let client = [
            v6("fd00:1:3a00:10::1"),
            v6("fd00:1:3c00:10::1"),
            v6("fd00:1:3900:10::1"),
            v6("fd00:1:3b00:10::1"),
        ];
        let server = [
            v6("fd00:1:3900:20::2"),
            v6("fd00:1:3a00:20::2"),
            v6("fd00:1:3b00:20::2"),
            v6("fd00:1:3c00:20::2"),
        ];
        let expected = [2usize, 0, 3, 1];
        let mut loads = [0usize; 4];
        for conn in 0..8 {
            let k = conn % 4;
            let plane = planes(&server[k], &server);
            assert_eq!(
                plane,
                Planes::Prefix(40),
                "the four plane prefixes differ within their /40"
            );
            let reaches: Vec<Reach> = client
                .iter()
                .map(|c| reach(c, Some("net1"), &server[k], &[], plane))
                .collect();
            let nic = pick(&reaches, &loads, k).expect("nic");
            assert_eq!(nic, expected[k], "server rail {k}: {reaches:?}");
            loads[nic] += 1;
        }
        assert_eq!(loads, [2; 4], "load never moves a connection off its plane");
    }

    // A NIC whose GID was read before SLAAC finished (zero or
    // fe80::) is re-read on the next connection; a routable one is not.
    #[test]
    fn unroutable_cached_gids_are_refreshed() {
        assert!(needs_refresh(&[0; 16]));
        assert!(needs_refresh(&v6("fe80::1")));
        assert!(!needs_refresh(&v6("fd00:1:3a00:10::1")));
        assert!(!needs_refresh(&v4([10, 0, 0, 1])));
        // What a stale fe80 GID costs: no reachability to any rail, so
        // pairing would fall back to load and index.
        let server = v6("fd00:1:3900:20::2");
        let plane = planes(&server, &[server, v6("fd00:1:3a00:20::1")]);
        assert_eq!(
            reach(&v6("fe80::1"), Some("net1"), &server, &[], plane),
            Reach::Unknown
        );
        assert!(
            reach(&v6("fd00:1:3900:10::1"), Some("net1"), &server, &[], plane) > Reach::Unknown
        );
    }

    /// Connections a client makes to a server with one rail per plane:
    /// an anchor on server rail 0, then `n` round robin over the rails.
    /// Returns how many landed on a local NIC of another plane.
    fn cross_plane(
        locals: &[([u8; 16], String)],
        remotes: &[[u8; 16]],
        plane_of: impl Fn(usize) -> usize,
        routes: &[Route],
        n: usize,
    ) -> usize {
        connect_all(locals, remotes, routes, n)
            .0
            .iter()
            .filter(|&&(server_rail, nic)| plane_of(nic) != server_rail)
            .count()
    }

    /// A server rail that `works(nic)` decides who reaches; records tries.
    struct FakeLink<'a> {
        remote: [u8; 16],
        works: &'a dyn Fn(usize) -> bool,
        error: &'a str,
        attempts: Vec<usize>,
    }

    impl RailLink for FakeLink<'_> {
        type Conn = ();
        fn dial(&mut self) -> Result<[u8; 16], String> {
            Ok(self.remote)
        }
        fn connect(&mut self, nic: usize) -> Result<(), String> {
            self.attempts.push(nic);
            if (self.works)(nic) {
                Ok(())
            } else {
                Err(self.error.to_string())
            }
        }
    }

    /// A client's NICs as `Pairing::connect` sees them, with loads that
    /// stay counted (connections are never dropped here).
    struct FakeClient {
        nics: Vec<NicAddr>,
        names: Vec<String>,
        loads: Vec<AtomicUsize>,
        placement: parking_lot::Mutex<()>,
        pairing: Pairing,
    }

    impl FakeClient {
        fn new(locals: &[([u8; 16], String)]) -> Self {
            Self {
                nics: locals
                    .iter()
                    .map(|(gid, dev)| NicAddr {
                        gid: *gid,
                        netdev: Some(dev.clone()),
                    })
                    .collect(),
                names: locals.iter().map(|(_, dev)| dev.clone()).collect(),
                loads: locals.iter().map(|_| AtomicUsize::new(0)).collect(),
                placement: parking_lot::Mutex::new(()),
                pairing: Pairing::default(),
            }
        }

        /// One connection to `remote`, position `server_rail`, knowing the
        /// server's rails (`[]`: the anchor, before its RAILS reply).
        fn connect(
            &self,
            remote: [u8; 16],
            server_rail: usize,
            server_rails: &[[u8; 16]],
            routes: &[Route],
            works: &dyn Fn(usize) -> bool,
            error: &str,
        ) -> (Result<usize, String>, Vec<usize>) {
            let mut link = FakeLink {
                remote,
                works,
                error,
                attempts: Vec::new(),
            };
            let loads: Vec<&AtomicUsize> = self.loads.iter().collect();
            let target = Target {
                addr: "server:9890",
                server_rail,
                server_rails,
                names: &self.names,
            };
            let r = self.pairing.connect(
                &mut link,
                &self.nics,
                &loads,
                &self.placement,
                routes,
                &target,
            );
            (r.map(|(nic, ())| nic), link.attempts)
        }

        fn loads(&self) -> Vec<usize> {
            self.loads
                .iter()
                .map(|l| l.load(Ordering::Relaxed))
                .collect()
        }
    }

    const PATH_ERROR: &str = "QP to RTR failed: 110 (Connection timed out)";

    /// The production path: an anchor on server rail 0 (no rail list yet,
    /// as `RdmaSources` and `server_rails` connect), then `n` connections
    /// round robin knowing the server's rails, through `Pairing::connect`
    /// with every NIC able to connect. (server rail, local NIC) per
    /// connection, the final loads, and the chosen NIC's reach.
    fn connect_all(
        locals: &[([u8; 16], String)],
        remotes: &[[u8; 16]],
        routes: &[Route],
        n: usize,
    ) -> (Vec<(usize, usize)>, Vec<usize>, Vec<Reach>) {
        let client = FakeClient::new(locals);
        let mut placed = Vec::new();
        let mut best = Vec::new();
        for (i, server_rail) in std::iter::once(0)
            .chain((0..n).map(|i| i % remotes.len()))
            .enumerate()
        {
            let known: &[[u8; 16]] = if i == 0 { &[] } else { remotes };
            let (nic, attempts) = client.connect(
                remotes[server_rail],
                server_rail,
                known,
                routes,
                &|_| true,
                PATH_ERROR,
            );
            let nic = nic.expect("connects");
            assert_eq!(attempts, [nic], "every NIC works: one attempt");
            let p = planes(&remotes[server_rail], known);
            best.push(reach(
                &locals[nic].0,
                Some(&locals[nic].1),
                &remotes[server_rail],
                routes,
                p,
            ));
            placed.push((server_rail, nic));
        }
        (placed, client.loads(), best)
    }

    // DRA pods: eight rails, one /32 each in 192.168.(32k).0/19, only an
    // eth0 default. Without per-rail routes the routing table says nothing
    // about any NIC; the shared prefix alone pairs every connection by
    // plane, with or without the routes.
    #[test]
    fn dra_rails_pair_by_plane_with_or_without_rail_routes() {
        let no_loop = parse_routes(
            "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n\
            eth0\t00000000\t0101FEA9\t0003\t0\t0\t0\t00000000\t0\t0\t0\n\
            eth0\t0101FEA9\t00000000\t0005\t0\t0\t0\tFFFFFFFF\t0\t0\t0\n",
        );
        assert_eq!(no_loop.len(), 2);
        let mut with_loop = no_loop.clone();
        for k in 0..8u32 {
            with_loop.push(Route {
                iface: format!("ens{k}"),
                dest: u32::from(Ipv4Addr::new(192, 168, (32 * k) as u8, 0)),
                mask: 0xffff_e000,
                gateway: 0,
                metric: 0,
            });
        }
        let locals: Vec<([u8; 16], String)> = (0..8u8)
            .map(|k| (v4([192, 168, 32 * k, 5]), format!("ens{k}")))
            .collect();
        let remotes: Vec<[u8; 16]> = (0..8u8).map(|k| v4([192, 168, 32 * k, 9])).collect();
        for routes in [&with_loop, &no_loop] {
            assert_eq!(cross_plane(&locals, &remotes, |nic| nic, routes, 16), 0);
        }
        // Enumerated in another order on the client: still by plane.
        let order = [5usize, 2, 7, 0, 3, 6, 1, 4];
        let shuffled: Vec<([u8; 16], String)> = order.iter().map(|&k| locals[k].clone()).collect();
        assert_eq!(
            cross_plane(&shuffled, &remotes, |nic| order[nic], &no_loop, 64),
            0
        );
    }

    // IPv6 rails with one /64 per plane (both ends on it): on-link by
    // prefix, so pairing is by plane.
    #[test]
    fn ipv6_slash64_per_plane_pairs_by_plane() {
        let locals: Vec<([u8; 16], String)> = (0..4u16)
            .map(|k| (v6(&format!("fd00:0:0:{k}::5")), format!("net{k}")))
            .collect();
        let remotes: Vec<[u8; 16]> = (0..4u16).map(|k| v6(&format!("fd00:0:0:{k}::9"))).collect();
        assert_eq!(cross_plane(&locals, &remotes, |nic| nic, &[], 16), 0);
    }

    // A mixed host: two IPv4 rails and two IPv6 rails. An address of one
    // family never pairs with a NIC of the other while one of its own
    // family exists.
    #[test]
    fn mixed_family_host_pairs_within_each_family() {
        let locals: Vec<([u8; 16], String)> = vec![
            (v4([192, 168, 0, 5]), "ens0".into()),
            (v6("fd00:0:0:1::5"), "net1".into()),
            (v4([192, 168, 32, 5]), "ens2".into()),
            (v6("fd00:0:0:3::5"), "net3".into()),
        ];
        let remotes = [
            v4([192, 168, 0, 9]),
            v6("fd00:0:0:1::9"),
            v4([192, 168, 32, 9]),
            v6("fd00:0:0:3::9"),
        ];
        assert_eq!(cross_plane(&locals, &remotes, |nic| nic, &[], 32), 0);
        // Across families: Unknown, below any prefix match.
        let plane = planes(&remotes[0], &remotes);
        assert_eq!(
            plane,
            Planes::Prefix(19),
            "the two IPv4 rails decide the IPv4 plane length"
        );
        assert_eq!(
            reach(&locals[1].0, Some("net1"), &remotes[0], &[], plane),
            Reach::Unknown
        );
        assert_eq!(
            reach(&locals[2].0, Some("ens2"), &remotes[2], &[], plane),
            Reach::SamePlane
        );
    }

    // Two local NICs on one plane (one server rail per plane): they tie,
    // so connections spread over both instead of piling on the NIC whose
    // host part is numerically closest.
    #[test]
    fn nics_on_one_plane_share_its_connections() {
        let locals: Vec<([u8; 16], String)> = vec![
            (v4([192, 168, 0, 5]), "ens0".into()),
            (v4([192, 168, 0, 130]), "ens1".into()),
            (v4([192, 168, 32, 5]), "ens2".into()),
        ];
        let remotes = [v4([192, 168, 0, 9]), v4([192, 168, 32, 9])];
        let (placed, loads, best) = connect_all(&locals, &remotes, &[], 31);
        // 17 connections on server rail 0 (the anchor and 16 more), split
        // over the two NICs on its plane; 15 on rail 1, all on its NIC.
        assert_eq!(loads, [9, 8, 15]);
        assert!(
            placed
                .iter()
                .all(|&(server_rail, nic)| (nic == 2) == (server_rail == 1)),
            "{placed:?}"
        );
        // The anchor ranks by raw prefix (no rail list yet), the rest by plane.
        assert!(matches!(best[0], Reach::SharedPrefix(_)));
        assert!(best[1..].iter().all(|&r| r == Reach::SamePlane));
    }

    // A server whose rails are numbered within one subnet (planes not by
    // subnet): the addresses cannot tell planes apart, so no NIC is
    // preferred by address, connections spread by load, and the pairing
    // reports Unknown (Rails::connect_rail warns).
    #[test]
    fn interleaved_numbering_spreads_by_load() {
        let locals: Vec<([u8; 16], String)> = (0..4u8)
            .map(|k| (v4([10, 0, 0, 11 + k]), format!("ens{k}")))
            .collect();
        let remotes: Vec<[u8; 16]> = (0..4u8).map(|k| v4([10, 0, 0, 1 + k])).collect();
        let (_, loads, best) = connect_all(&locals, &remotes, &[], 15);
        assert_eq!(loads, [4; 4], "no pile-up on the numerically closest NIC");
        assert_eq!(planes(&remotes[0], &remotes), Planes::Unclear);
        assert!(best[1..].iter().all(|&r| r == Reach::Unknown));
    }

    #[test]
    fn planes_from_the_servers_rails() {
        let dra: Vec<[u8; 16]> = (0..8u8).map(|k| v4([192, 168, 32 * k, 9])).collect();
        assert_eq!(planes(&dra[3], &dra), Planes::Prefix(19));
        // One rail (or one per family): no plane information.
        assert_eq!(planes(&dra[0], &dra[..1]), Planes::NoInfo);
        assert_eq!(
            planes(&dra[0], &[dra[0], dra[0]]),
            Planes::NoInfo,
            "duplicates count once"
        );
        assert_eq!(planes(&dra[0], &[dra[0], v6("fd00::1")]), Planes::NoInfo);
        // Link-local and InfiniBand GIDs carry no plane.
        assert_eq!(
            planes(&v6("fe80::1"), &[v6("fe80::1"), v6("fe80::2")]),
            Planes::NoInfo
        );
        // IPv6: one /48 per plane.
        let v6rails = [v6("fd00:1:3900::1"), v6("fd00:1:3a00::1")];
        assert_eq!(planes(&v6rails[0], &v6rails), Planes::Prefix(39));
        // Two server NICs on one plane (a fan-out node): the close pair is
        // one plane, the split is still /19.
        let fanout = [
            v4([192, 168, 0, 5]),
            v4([192, 168, 0, 130]),
            v4([192, 168, 32, 5]),
        ];
        assert_eq!(planes(&fanout[0], &fanout), Planes::Prefix(19));
        // Rails numbered within one subnet: no clear planes.
        assert_eq!(
            planes(&v4([10, 0, 0, 9]), &[v4([10, 0, 0, 9]), v4([10, 0, 0, 10])]),
            Planes::Unclear
        );
        // A weak split: a close pair (/25) one bit apart from the third
        // rail (/24), all within one /24 subnet.
        let weak = [v4([10, 0, 0, 1]), v4([10, 0, 0, 65]), v4([10, 0, 0, 129])];
        assert_eq!(planes(&weak[0], &weak), Planes::Unclear);
        assert_eq!(components(3, [(0, 1)].into_iter()), 2);
    }

    // A server with two NICs on one plane and one on another (a --then-serve
    // fan-out node): connections pair by plane; the close pair must not
    // set the plane length.
    #[test]
    fn a_server_with_two_nics_on_one_plane_pairs_by_plane() {
        let locals: Vec<([u8; 16], String)> = vec![
            (v4([192, 168, 0, 7]), "ens0".into()),
            (v4([192, 168, 32, 7]), "ens1".into()),
        ];
        let remotes = [
            v4([192, 168, 0, 5]),
            v4([192, 168, 0, 130]),
            v4([192, 168, 32, 5]),
        ];
        let (placed, _, _) = connect_all(&locals, &remotes, &[], 31);
        assert!(
            placed
                .iter()
                .all(|&(rail, nic)| nic == usize::from(rail == 2)),
            "{placed:?}"
        );
    }

    // Rails numbered within one subnet, .9 on plane A and .10 on plane B:
    // addresses cannot tell, so pairing goes by load and the connection
    // backstop finds the NIC that works. No connection stays cross-plane.
    #[test]
    fn adjacent_numbering_is_rescued_by_the_connection_backstop() {
        let locals: Vec<([u8; 16], String)> = vec![
            (v4([10, 0, 0, 20]), "ensB".into()),
            (v4([10, 0, 0, 21]), "ensA".into()),
        ];
        let remotes = [v4([10, 0, 0, 9]), v4([10, 0, 0, 10])];
        assert_eq!(planes(&remotes[0], &remotes), Planes::Unclear);
        // Plane A: server rail 0 and local NIC 1; plane B: rail 1, NIC 0.
        let client = FakeClient::new(&locals);
        for i in 0..20 {
            let rail = i % 2;
            let known: &[[u8; 16]] = if i == 0 { &[] } else { &remotes };
            let works = move |nic: usize| nic == 1 - rail;
            let (nic, attempts) =
                client.connect(remotes[rail], rail, known, &[], &works, PATH_ERROR);
            assert_eq!(nic, Ok(1 - rail), "connection {i}");
            assert!(attempts.len() <= 2);
            if i >= 2 {
                assert_eq!(
                    attempts,
                    [1 - rail],
                    "a pair that failed is not tried first again"
                );
            }
        }
        assert!(
            client.pairing.warned.lock().contains(&remotes[0])
                || client.pairing.warned.lock().contains(&remotes[1]),
            "pairing by load is reported"
        );
    }

    // The anchor (no rail list yet) on the wrong NIC by address: the
    // backstop moves it to the one that connects, remembers the pair, and
    // the anchor does not use up the once-per-rail warning.
    #[test]
    fn the_anchor_falls_back_to_the_nic_that_connects() {
        let locals: Vec<([u8; 16], String)> = vec![
            (v4([10, 0, 0, 11]), "ens0".into()),
            (v4([10, 0, 0, 12]), "ens1".into()),
            (v6("fd00::5"), "net2".into()),
        ];
        let server = v4([10, 0, 0, 9]);
        let client = FakeClient::new(&locals);
        // .11 shares more bits with .9 than .12 does, but only ens1 works.
        let (nic, attempts) = client.connect(server, 0, &[], &[], &|nic| nic == 1, PATH_ERROR);
        assert_eq!((nic, attempts), (Ok(1), vec![0, 1]));
        let (nic, attempts) = client.connect(server, 0, &[], &[], &|nic| nic == 1, PATH_ERROR);
        assert_eq!(
            (nic, attempts),
            (Ok(1), vec![1]),
            "the working pair is remembered"
        );
        assert_eq!(
            client.loads(),
            [0, 2, 0],
            "a failed attempt releases its load"
        );
        assert!(
            client.pairing.warned.lock().is_empty(),
            "the anchor never warns"
        );
        // Not even when no NIC qualifies at all (only another family here).
        let v6_only = FakeClient::new(&[(v6("fd00::5"), "net0".into())]);
        let (nic, _) = v6_only.connect(server, 0, &[], &[], &|_| true, PATH_ERROR);
        assert_eq!(nic, Ok(0));
        assert!(
            v6_only.pairing.warned.lock().is_empty(),
            "the anchor never warns"
        );
        let (nic, _) = v6_only.connect(
            server,
            0,
            &[server, v4([10, 0, 32, 9])],
            &[],
            &|_| true,
            PATH_ERROR,
        );
        assert_eq!(nic, Ok(0));
        assert!(
            v6_only.pairing.warned.lock().contains(&server),
            "a later connection does"
        );
        // A remembered pair that stops working is dropped, and the next
        // NIC is found.
        // (ens0 failed before, so the untried IPv6 NIC comes first.)
        let (nic, attempts) = client.connect(server, 0, &[], &[], &|nic| nic == 0, PATH_ERROR);
        assert_eq!((nic, attempts), (Ok(0), vec![1, 2, 0]));
    }

    #[test]
    fn when_no_nic_connects_the_error_names_every_attempt() {
        let locals: Vec<([u8; 16], String)> = (0..3u8)
            .map(|k| (v4([10, 0, k, 5]), format!("ens{k}")))
            .collect();
        let client = FakeClient::new(&locals);
        let (r, attempts) = client.connect(v4([10, 0, 1, 9]), 1, &[], &[], &|_| false, PATH_ERROR);
        let e = r.expect_err("nothing connects");
        assert_eq!(attempts.len(), 3, "each NIC once: {attempts:?}");
        assert_eq!(attempts[0], 1, "best by address first");
        for name in ["ens0", "ens1", "ens2"] {
            assert!(e.contains(name), "{e}");
        }
        assert_eq!(client.loads(), [0, 0, 0]);
        // An error that is not about the path (credentials, BUSY): no
        // other NIC is tried.
        let (r, attempts) = client.connect(
            v4([10, 0, 1, 9]),
            1,
            &[],
            &[],
            &|_| false,
            "server refused this connector's credentials",
        );
        assert!(r.is_err());
        assert_eq!(attempts.len(), 1);
    }

    #[test]
    fn path_errors_are_told_apart() {
        for path in [
            "x: QP to RTR failed: 110 (Connection timed out)",
            "QP to RTR failed: 113 (No route to host)",
            "QP to RTR failed: 101",
            "send: completion RETRY_EXC_ERR",
            "poll timeout (5s wall, no CQE)",
            "10.0.0.1:9890: server could not set up the connection; retry, or try another server",
        ] {
            assert!(is_path_error(path), "{path}");
        }
        for other in [
            "QP to RTR failed: 22 (Invalid argument)",
            "RNR_RETRY_EXC_ERR",
            "server refused this connector's credentials [nvstor-rdma REJECTED]",
            "server is at its limit of concurrent sessions [nvstor-rdma BUSY]",
            "TCP connect: refused",
        ] {
            assert!(!is_path_error(other), "{other}");
        }
    }

    #[test]
    fn strict_rp_filter_is_the_larger_of_all_and_the_netdev() {
        let rails = vec![
            ("ens0".to_string(), v4([192, 168, 0, 5])),
            ("ens1".to_string(), v4([192, 168, 32, 5])),
            ("net2".to_string(), v6("fd00::5")),
        ];
        let table = |all: u32, per: &'static [(&'static str, u32)]| {
            move |c: &str| {
                if c == "all" {
                    Some(all)
                } else {
                    per.iter().find(|(d, _)| *d == c).map(|(_, v)| *v)
                }
            }
        };
        // conf/all = 1 makes every IPv4 rail strict; IPv6 rails have no rp_filter.
        assert_eq!(strict_rp_filter(&rails, table(1, &[])), ["ens0", "ens1"]);
        // Loose on a rail (2) beats strict all (1): the larger wins.
        assert_eq!(strict_rp_filter(&rails, table(1, &[("ens0", 2)])), ["ens1"]);
        // all = 0, one rail strict.
        assert_eq!(
            strict_rp_filter(&rails, table(0, &[("ens1", 1), ("net2", 1)])),
            ["ens1"]
        );
        // Loose all (2) with a strict rail: loose.
        assert_eq!(
            strict_rp_filter(&rails, table(2, &[("ens0", 1)])),
            Vec::<String>::new()
        );
        // Unreadable: not strict.
        assert!(strict_rp_filter(&rails, |_| None).is_empty());
    }

    #[test]
    fn device_select_parses() {
        assert_eq!("all".parse::<DeviceSelect>(), Ok(DeviceSelect::AllActive));
        assert_eq!("netns".parse::<DeviceSelect>(), Ok(DeviceSelect::ThisNetns));
        assert_eq!(
            "mlx5_0, mlx5_3".parse::<DeviceSelect>(),
            Ok(DeviceSelect::Named(vec!["mlx5_0".into(), "mlx5_3".into()]))
        );
        assert!("".parse::<DeviceSelect>().is_err());
    }

    #[test]
    fn interface_gids_include_loopback() {
        let gids = interface_gids().expect("getifaddrs");
        assert!(
            gids.contains(&Ipv4Addr::LOCALHOST.to_ipv6_mapped().octets()),
            "{gids:?}"
        );
        let view = NetnsView::read().expect("getifaddrs");
        assert!(
            view.links.iter().any(|(n, _)| n == "lo"),
            "{:?}",
            view.links
        );
    }

    /// A NIC whose GID table gains a routable entry after `ready_after`
    /// reads of its best GID (SLAAC finishing); `usize::MAX` = never.
    struct FakeNic {
        name: &'static str,
        link_local: Option<[u8; 16]>,
        routable: [u8; 16],
        ready_after: usize,
        reads: std::cell::Cell<usize>,
        netdev: Option<(&'static str, Option<[u8; 6]>)>,
    }

    impl FakeNic {
        fn new(name: &'static str, routable: [u8; 16], ready_after: usize) -> Self {
            Self {
                name,
                link_local: None,
                routable,
                ready_after,
                reads: 0.into(),
                netdev: None,
            }
        }
        fn with_netdev(mut self, n: &'static str, mac: Option<[u8; 6]>) -> Self {
            self.netdev = Some((n, mac));
            self
        }
        fn with_link_local(mut self, g: [u8; 16]) -> Self {
            self.link_local = Some(g);
            self
        }
        fn has_routable(&self) -> bool {
            self.reads.get() >= self.ready_after
        }
    }

    impl NicView for FakeNic {
        fn name(&self) -> &str {
            self.name
        }
        fn best_gid(&self) -> [u8; 16] {
            let ready = self.has_routable();
            self.reads.set(self.reads.get().saturating_add(1));
            if ready {
                self.routable
            } else {
                self.link_local.unwrap_or([0; 16])
            }
        }
        fn gids(&self) -> Vec<[u8; 16]> {
            let mut g: Vec<[u8; 16]> = self.link_local.into_iter().collect();
            if self.has_routable() {
                g.push(self.routable);
            }
            g
        }
        fn netdevs(&self) -> Vec<(String, Option<[u8; 6]>)> {
            self.netdev
                .iter()
                .map(|(n, m)| (n.to_string(), *m))
                .collect()
        }
    }

    const MAC_A: [u8; 6] = [0x02, 0, 0, 0, 0, 0xa];
    const MAC_B: [u8; 6] = [0x02, 0, 0, 0, 0, 0xb];

    /// A pod namespace with VF `net1` (MAC_A), its fe80 address and, once
    /// SLAAC finishes, its routable one.
    fn pod_ns(routable: Option<[u8; 16]>) -> NetnsView {
        let mut addrs = vec![v6("fe80::a"), v4([127, 0, 0, 1])];
        addrs.extend(routable);
        NetnsView {
            addrs,
            links: vec![("lo".into(), None), ("net1".into(), Some(MAC_A))],
        }
    }

    // A VF must count as this namespace's before its routable GID is an
    // address here, or a VF still in SLAAC would be dropped.
    #[test]
    fn membership_recognises_a_vf_before_its_routable_gid() {
        let gid = v6("fd00:1:3900:10::1");
        // Routable GID that is an address here: ready.
        let ready = FakeNic::new("mlx5_2", gid, 0);
        assert_eq!(membership(&ready, &pod_ns(Some(gid))), Membership::Ready);
        // No GID at all yet, netdev net1 with this namespace's MAC: pending.
        let by_netdev = FakeNic::new("mlx5_2", gid, usize::MAX).with_netdev("net1", Some(MAC_A));
        assert_eq!(membership(&by_netdev, &pod_ns(None)), Membership::Pending);
        // Link-local GID that is an address here: pending.
        let by_ll = FakeNic::new("mlx5_2", gid, usize::MAX).with_link_local(v6("fe80::a"));
        assert_eq!(membership(&by_ll, &pod_ns(None)), Membership::Pending);
        // Our netdev, routable GID present but not (yet) listed here: ready.
        let late_addr = FakeNic::new("mlx5_2", gid, 0).with_netdev("net1", Some(MAC_A));
        assert_eq!(membership(&late_addr, &pod_ns(None)), Membership::Ready);
    }

    #[test]
    fn membership_excludes_other_namespaces_nics() {
        let ns = pod_ns(None);
        // A host PF: its GID is a host address, its netdev has another
        // MAC even where the name collides.
        let pf = FakeNic::new("mlx5_0", v4([10, 0, 0, 7]), 0)
            .with_link_local(v6("fe80::7"))
            .with_netdev("net1", Some(MAC_B));
        assert_eq!(membership(&pf, &ns), Membership::Elsewhere);
        // An IPoIB netdev (no 6-byte MAC) never matches by name.
        let ib = FakeNic::new("mlx5_ib0", [0; 16], usize::MAX).with_netdev("net1", None);
        assert_eq!(
            membership(
                &ib,
                &NetnsView {
                    addrs: vec![],
                    links: vec![("net1".into(), None)]
                }
            ),
            Membership::Elsewhere
        );
        // No GID, no netdev.
        assert_eq!(
            membership(&FakeNic::new("x", [0; 16], usize::MAX), &ns),
            Membership::Elsewhere
        );
    }

    // A pending VF is waited for and comes back ready once its GID
    // appears; NICs of other namespaces are dropped.
    #[test]
    fn select_waits_for_slaac_then_keeps_the_rail() {
        let gid = v6("fd00:1:3900:10::1");
        let nics = vec![
            FakeNic::new("mlx5_0", v4([10, 0, 0, 7]), 0).with_netdev("ens1f0", Some(MAC_B)),
            FakeNic::new("mlx5_2", gid, 4).with_netdev("net1", Some(MAC_A)),
        ];
        let mut sleeps = Vec::new();
        let wait = RailWait {
            timeout: Duration::from_secs(30),
            poll: Duration::from_millis(250),
        };
        let got = select_netns(nics, || Ok(pod_ns(Some(gid))), &wait, |d| sleeps.push(d))
            .expect("select");
        assert_eq!(got.len(), 1);
        assert_eq!((got[0].0.name, got[0].1), ("mlx5_2", Membership::Ready));
        assert!(
            !sleeps.is_empty() && sleeps.iter().all(|&d| d == Duration::from_millis(250)),
            "{sleeps:?}"
        );
    }

    // A VF that never gets a routable GID within the wait is kept as
    // pending, not dropped, and the wait is bounded.
    #[test]
    fn select_keeps_a_pending_rail_after_the_wait() {
        let gid = v6("fd00:1:3900:10::1");
        let nics = vec![
            FakeNic::new("mlx5_2", gid, 0).with_netdev("net1", Some(MAC_A)),
            FakeNic::new("mlx5_3", v6("fd00:1:3a00:10::1"), usize::MAX)
                .with_netdev("net2", Some(MAC_B)),
        ];
        let mut ns = pod_ns(Some(gid));
        ns.links.push(("net2".into(), Some(MAC_B)));
        let wait = RailWait {
            timeout: Duration::from_millis(60),
            poll: Duration::from_millis(10),
        };
        let started = Instant::now();
        let got = select_netns(nics, || Ok(ns.clone()), &wait, std::thread::sleep).expect("select");
        let took = started.elapsed();
        assert!(
            took >= Duration::from_millis(60) && took < Duration::from_secs(5),
            "{took:?}"
        );
        let got: Vec<(&str, Membership)> = got.iter().map(|(n, m)| (n.name, *m)).collect();
        assert_eq!(
            got,
            [
                ("mlx5_2", Membership::Ready),
                ("mlx5_3", Membership::Pending)
            ]
        );
    }

    #[test]
    fn select_without_pending_rails_or_with_zero_wait_does_not_sleep() {
        let gid = v6("fd00:1:3900:10::1");
        let mut slept = false;
        let wait = RailWait::default();
        let got = select_netns(
            vec![FakeNic::new("mlx5_2", gid, 0)],
            || Ok(pod_ns(Some(gid))),
            &wait,
            |_| slept = true,
        )
        .expect("select");
        assert_eq!(got.len(), 1);
        // Nothing of ours at all: nothing to wait for.
        let got = select_netns(
            vec![FakeNic::new("mlx5_0", v4([10, 0, 0, 7]), 0)],
            || Ok(pod_ns(None)),
            &wait,
            |_| slept = true,
        )
        .expect("select");
        assert!(got.is_empty());
        // Timeout 0: a pending rail is kept at once.
        let zero = RailWait {
            timeout: Duration::ZERO,
            ..RailWait::default()
        };
        let got = select_netns(
            vec![FakeNic::new("mlx5_2", gid, usize::MAX).with_netdev("net1", Some(MAC_A))],
            || Ok(pod_ns(None)),
            &zero,
            |_| slept = true,
        )
        .expect("select");
        assert_eq!(got[0].1, Membership::Pending);
        assert!(!slept);
    }

    #[test]
    fn select_reports_an_unreadable_namespace() {
        let r = select_netns(
            vec![FakeNic::new("x", [0; 16], 0)],
            || Err(std::io::Error::other("no getifaddrs")),
            &RailWait::default(),
            |_| {},
        );
        assert!(r.is_err_and(|e| e.contains("no getifaddrs")));
    }

    #[test]
    fn rail_wait_reads_the_environment_value() {
        assert_eq!(RailWait::from_env_value(None), Ok(RailWait::default()));
        assert_eq!(RailWait::default().timeout, Duration::from_secs(30));
        assert_eq!(
            RailWait::from_env_value(Some("0")).map(|w| w.timeout),
            Ok(Duration::ZERO)
        );
        assert_eq!(
            RailWait::from_env_value(Some(" 90 ")).map(|w| w.timeout),
            Ok(Duration::from_secs(90))
        );
        assert!(RailWait::from_env_value(Some("30s")).is_err());
        assert!(RailWait::from_env_value(Some("-1")).is_err());
    }

    #[test]
    fn parses_macs() {
        assert_eq!(parse_mac("02:00:00:00:00:0a\n"), Some(MAC_A));
        assert_eq!(parse_mac("02:00:00:00:00"), None);
        assert_eq!(parse_mac("02:00:00:00:00:0a:0b"), None);
        assert_eq!(parse_mac("zz:00:00:00:00:0a"), None);
        // IPoIB hardware address: 20 bytes.
        assert_eq!(
            parse_mac("00:00:10:29:fe:80:00:00:00:00:00:00:02:00:00:ff:fe:00:00:01"),
            None
        );
    }

    #[test]
    fn shared_prefix_counts_leading_bits() {
        let a = v6("fd00:1:3900:20::1");
        assert_eq!(shared_prefix_bits(&a, &a), 128);
        assert_eq!(shared_prefix_bits(&a, &v6("fd00:1:3900:10::1")), 58);
        assert_eq!(shared_prefix_bits(&a, &v6("fd00:1:3a00:20::1")), 38);
        assert_eq!(shared_prefix_bits(&a, &v6("7dcd::1")), 0);
        // Same /64: on-link, ranked above any shared prefix.
        assert_eq!(
            reach(&a, None, &v6("fd00:1:3900:20::2"), &[], Planes::NoInfo),
            Reach::SameSubnet
        );
        // Link-local on either side: no prefix ranking.
        assert_eq!(
            reach(&a, None, &v6("fe80::1"), &[], Planes::Prefix(1)),
            Reach::Unknown
        );
    }

    /// The case that rules out index matching: the server's rail 0 sits
    /// on the fabric this client's second NIC is cabled to.
    #[test]
    fn pick_follows_reachability_not_index() {
        let reaches = [Reach::Unknown, Reach::SameSubnet];
        assert_eq!(pick(&reaches, &[0, 0], 0), Some(1));
        assert_eq!(
            pick(&reaches, &[0, 5], 0),
            Some(1),
            "load never beats reachability"
        );
        let reaches = [Reach::Routed, Reach::SameSubnet];
        assert_eq!(pick(&reaches, &[0, 0], 0), Some(1));
    }

    #[test]
    fn pick_spreads_by_load_then_index_tiebreak() {
        let all = [Reach::SameSubnet; 4];
        // Equal load: the NIC at the server rail's position wins.
        assert_eq!(pick(&all, &[0, 0, 0, 0], 2), Some(2));
        // Unequal load: least-loaded wins regardless of position.
        assert_eq!(pick(&all, &[1, 1, 0, 1], 0), Some(2));
        // Server rail position beyond our NIC count: lowest position.
        assert_eq!(pick(&all, &[0, 0, 0, 0], 7), Some(0));
        // Nothing reachable: still picks (least loaded), caller warns.
        assert_eq!(pick(&[Reach::Unknown; 2], &[3, 1], 0), Some(1));
        assert_eq!(pick(&[], &[], 0), None);
    }

    /// Simulated 8-rail host where each server rail is on-link to exactly
    /// one client NIC, wired in a non-identity order: 16 connections spread
    /// over the server rails round-robin land 2 per client NIC, each on
    /// the NIC that shares the server rail's subnet.
    #[test]
    fn eight_rails_pair_by_subnet() {
        let wiring = [3usize, 0, 6, 1, 7, 2, 5, 4]; // server rail k → client NIC wiring[k]
        let mut table = String::from(
            "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n",
        );
        for nic in 0..8u8 {
            let dest = u32::from_le_bytes([10, 100 + nic, 0, 0]);
            table += &format!("r{nic}\t{dest:08X}\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n");
        }
        let routes = parse_routes(&table);
        let local: Vec<[u8; 16]> = (0..8u8).map(|n| v4([10, 100 + n, 0, 1])).collect();
        let mut loads = [0usize; 8];
        for conn in 0..16 {
            let k = conn % 8;
            let server_gid = v4([10, 100 + wiring[k] as u8, 0, 2]);
            let reaches: Vec<Reach> = (0..8)
                .map(|n| {
                    reach(
                        &local[n],
                        Some(&format!("r{n}")),
                        &server_gid,
                        &routes,
                        Planes::NoInfo,
                    )
                })
                .collect();
            let nic = pick(&reaches, &loads, k).expect("nic");
            assert_eq!(nic, wiring[k], "server rail {k}");
            loads[nic] += 1;
        }
        assert_eq!(loads, [2; 8]);
    }
}
