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

//! NFS v4 TCP server — accepts connections and dispatches RPC calls.
//!
//! Per RFC 7530: NFS v4 uses TCP (not UDP). Each client connection is a
//! single TCP stream with RPC record marking. COMPOUND requests are
//! dispatched to the CompoundProcessor backed by a persistent VFS.

use std::collections::HashMap;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::Instant;

use tokio::io::{BufReader, BufWriter};
use tokio::net::{TcpListener, TcpStream};
use tokio::sync::mpsc;
use tracing::{debug, info, warn};

use crate::metrics::NfsMetrics;
use crate::nfs4::{CompoundProcessor, ExportPolicy};
use crate::rpc;

#[cfg(feature = "rdma")]
#[path = "rdma_server.rs"]
mod rdma_server;
#[cfg(feature = "rdma")]
pub use nvstor_rdma::cm::Listener as RdmaListener;
#[cfg(feature = "rdma")]
pub use rdma_server::{bind_rdma_listener, DEFAULT_NFS_RDMA_PORT};

/// Maximum number of COMPOUNDs in flight per connection.
///
/// The dispatch loop is concurrent: the reader pulls frames as
/// fast as they arrive and spawns a worker per record. Without a bound,
/// a misbehaving client could allocate one tokio task + one blocking-pool
/// slot per RPC frame and OOM us. 256 in-flight per connection is plenty
/// to saturate the storage layer (kernel NFS clients negotiate ca_maxops
/// in the low double digits per slot, with ~32 slots typical) without
/// being a memory bomb. Backpressure works via the bounded reply channel:
/// when full, the reader's `tx.send(...)` awaits — which transparently
/// throttles the read side without dropping frames.
const MAX_INFLIGHT_PER_CONN: usize = 256;

/// NFS export entry — controls who can mount and with what permissions.
#[derive(Debug, Clone)]
pub struct NfsExport {
    /// Export path (e.g., "/").
    pub path: String,
    /// Allowed client IP patterns (e.g., "10.0.0.0/8", "192.168.1.0/24",
    /// or "*" for every client). Empty = loopback clients only: an export
    /// is reachable from other hosts only when it names them.
    pub allowed_clients: Vec<String>,
    /// Read-only export.
    pub read_only: bool,
    /// Enable root squashing (uid 0 and gid 0 → `anon_uid` / `anon_gid`).
    pub root_squash: bool,
    /// Uid squashed root and AUTH_NONE callers run as (default 65534).
    pub anon_uid: u32,
    /// Gid squashed root and AUTH_NONE callers run as (default 65534).
    pub anon_gid: u32,
    /// Allowed security flavors.
    pub sec: Vec<String>,
}

impl Default for NfsExport {
    fn default() -> Self {
        Self {
            path: "/".into(),
            allowed_clients: vec![], // empty = loopback clients only
            read_only: false,
            root_squash: true,
            anon_uid: crate::nfs4::ANON_ID,
            anon_gid: crate::nfs4::ANON_ID,
            sec: vec!["sys".into()],
        }
    }
}

/// NFS server configuration.
#[derive(Debug, Clone)]
pub struct NfsServerConfig {
    /// Maximum concurrent client connections.
    pub max_connections: usize,
    /// Idle timeout — close a connection that goes this long with no traffic.
    ///
    /// Guards against slowloris clients that open a socket and send nothing.
    /// Real NFS clients send RENEW (or SEQUENCE in v4.1+) well inside the
    /// lease period, so 300 s is a loose upper bound.
    pub idle_timeout: std::time::Duration,
    /// Write timeout — how long we'll wait for a reply to drain to the client.
    ///
    /// If the client stops reading, we must not block the tokio task forever.
    pub write_timeout: std::time::Duration,
    /// Export table. If empty, a default export of "/" is used.
    pub exports: Vec<NfsExport>,
    /// Registered memory per RDMA device for zero-copy READ and WRITE data,
    /// shared by all NFS/RDMA connections on the device (1 MiB slots). When
    /// it runs out, transfers register their own memory instead.
    pub rdma_pool_bytes: usize,
}

/// Default NFS/RDMA buffer pool per device: 256 in-flight 1 MiB transfers.
pub const DEFAULT_RDMA_POOL_BYTES: usize = 256 << 20;

impl Default for NfsServerConfig {
    fn default() -> Self {
        Self {
            max_connections: 1000,
            idle_timeout: std::time::Duration::from_secs(300),
            write_timeout: std::time::Duration::from_secs(60),
            exports: vec![NfsExport::default()],
            rdma_pool_bytes: DEFAULT_RDMA_POOL_BYTES,
        }
    }
}

/// Check if a client IP is allowed by the export table. An export with no
/// client patterns (and an empty table) admits loopback clients only.
fn is_client_allowed(addr: &std::net::SocketAddr, exports: &[NfsExport]) -> bool {
    // A dual-stack listener may report IPv4 peers as ::ffff:a.b.c.d.
    let client_ip = addr.ip().to_canonical();
    if exports.is_empty() {
        return client_ip.is_loopback();
    }
    for export in exports {
        if export.allowed_clients.is_empty() {
            if client_ip.is_loopback() {
                return true;
            }
            continue;
        }
        for pattern in &export.allowed_clients {
            if pattern == "*" || matches_client(pattern, client_ip) {
                return true;
            }
        }
    }
    false
}

/// Check an `allowed_clients` pattern up front: an address ("10.0.0.5",
/// "fd00::1"), a prefix ("10.0.0.0/8", "fd00:1::/32") or "*". A malformed
/// pattern matches no client, so configuration front ends (such as
/// nvstor-nfs-export's --allow-client) reject it instead of starting an
/// export that silently refuses everyone.
pub fn validate_client_pattern(pattern: &str) -> Result<(), String> {
    use std::net::IpAddr;
    if pattern == "*" {
        return Ok(());
    }
    let (net, bits) = match pattern.split_once('/') {
        Some((net, bits)) => (net, Some(bits)),
        None => (pattern, None),
    };
    let net: IpAddr = net
        .parse()
        .map_err(|_| format!("{pattern:?}: not an IP address or CIDR prefix"))?;
    let max = if net.to_canonical().is_ipv4() {
        32
    } else {
        128
    };
    if let Some(bits) = bits {
        match bits.parse::<u32>() {
            Ok(bits) if bits <= max => {}
            _ => return Err(format!("{pattern:?}: prefix length must be 0..={max}")),
        }
    }
    Ok(())
}

/// `pattern` is an address ("10.0.0.5", "fd00::1") or a prefix ("10.0.0.0/8",
/// "fd00:1::/32"). A pattern only matches clients of its own family.
fn matches_client(pattern: &str, client: std::net::IpAddr) -> bool {
    use std::net::IpAddr;
    let (net, bits) = match pattern.split_once('/') {
        Some((net, bits)) => match bits.parse::<u32>() {
            Ok(bits) => (net, Some(bits)),
            Err(_) => return false,
        },
        None => (pattern, None),
    };
    let Ok(net) = net.parse::<IpAddr>() else {
        return false;
    };
    match (net.to_canonical(), client) {
        (IpAddr::V4(net), IpAddr::V4(client)) => {
            let bits = bits.unwrap_or(32);
            bits <= 32 && {
                let mask = u32::MAX.checked_shl(32 - bits).unwrap_or(0);
                u32::from(net) & mask == u32::from(client) & mask
            }
        }
        (IpAddr::V6(net), IpAddr::V6(client)) => {
            let bits = bits.unwrap_or(128);
            bits <= 128 && {
                let mask = u128::MAX.checked_shl(128 - bits).unwrap_or(0);
                u128::from(net) & mask == u128::from(client) & mask
            }
        }
        _ => false,
    }
}

/// Runtime policy the server applies to every COMPOUND.
///
/// Derived from the (currently single) `NfsExport` in `NfsServerConfig`.
/// Once multi-export support lands, this will be per-FH rather than global.
#[derive(Debug, Clone)]
struct ServerPolicy {
    compound: ExportPolicy,
    allowed_flavors: Vec<u32>,
}

impl ServerPolicy {
    fn from_export(export: &NfsExport) -> Self {
        let mut flavors = Vec::new();
        for name in &export.sec {
            match name.as_str() {
                "sys" => flavors.push(rpc::AUTH_SYS),
                "none" => flavors.push(rpc::AUTH_NONE),
                other => warn!(flavor = other, "NFS: unknown sec flavor in export, ignored"),
            }
        }
        if flavors.is_empty() {
            // Preserve historical behavior: no sec list = accept anything.
            flavors.push(rpc::AUTH_SYS);
            flavors.push(rpc::AUTH_NONE);
        }
        Self {
            compound: ExportPolicy {
                root_squash: export.root_squash,
                anon_uid: export.anon_uid,
                anon_gid: export.anon_gid,
                read_only: export.read_only,
                advertised_flavors: flavors.clone(),
            },
            allowed_flavors: flavors,
        }
    }

    fn default_permissive() -> Self {
        let flavors = vec![rpc::AUTH_SYS, rpc::AUTH_NONE];
        Self {
            compound: ExportPolicy {
                advertised_flavors: flavors.clone(),
                ..ExportPolicy::default()
            },
            allowed_flavors: flavors,
        }
    }
}

/// Most connections one client address may hold (TCP and RDMA together): well above a Linux
/// client's nconnect or session-trunking transports (16 at most) per server
/// address. Also at most a quarter of `max_connections`, so one client can
/// never take every slot of the shared limit.
const MAX_CONNECTIONS_PER_PEER: usize = 64;

/// Connections held per client address, over TCP and RDMA together.
struct PeerSlots {
    limit: usize,
    held: parking_lot::Mutex<HashMap<std::net::IpAddr, usize>>,
}

impl PeerSlots {
    fn new(max_connections: usize) -> Arc<Self> {
        Arc::new(Self {
            limit: (max_connections / 4).clamp(1, MAX_CONNECTIONS_PER_PEER),
            held: parking_lot::Mutex::new(HashMap::new()),
        })
    }

    /// A slot for `ip` (IPv4-mapped IPv6 counts as IPv4), or None when the
    /// address already holds `limit` connections.
    fn acquire(self: &Arc<Self>, ip: std::net::IpAddr) -> Option<PeerSlot> {
        let ip = ip.to_canonical();
        let mut held = self.held.lock();
        let count = held.entry(ip).or_insert(0);
        if *count >= self.limit {
            return None;
        }
        *count += 1;
        Some(PeerSlot {
            slots: self.clone(),
            ip,
        })
    }
}

/// One connection's slot for its client address, returned on drop.
struct PeerSlot {
    slots: Arc<PeerSlots>,
    ip: std::net::IpAddr,
}

impl Drop for PeerSlot {
    fn drop(&mut self) {
        let mut held = self.slots.held.lock();
        if let Some(count) = held.get_mut(&self.ip) {
            *count -= 1;
            if *count == 0 {
                held.remove(&self.ip);
            }
        }
    }
}

/// NFS v4 TCP server.
pub struct NfsServer {
    config: NfsServerConfig,
    processor: Arc<CompoundProcessor>,
    active_connections: Arc<AtomicUsize>,
    /// Per-client-address share of `max_connections`, TCP and RDMA.
    peers: Arc<PeerSlots>,
    policy: Arc<ServerPolicy>,
    metrics: Arc<NfsMetrics>,
}

impl NfsServer {
    /// Create an NFS server with a CompoundProcessor that handles all operations.
    pub fn new(processor: Arc<CompoundProcessor>, config: NfsServerConfig) -> Self {
        Self::with_metrics(processor, config, Arc::new(NfsMetrics::new()))
    }

    /// Same as `new` but takes a pre-built `NfsMetrics` so callers can share
    /// it across the rest of the server (nvstor-server polls these atomics
    /// into its prometheus surface every few seconds).
    pub fn with_metrics(
        processor: Arc<CompoundProcessor>,
        config: NfsServerConfig,
        metrics: Arc<NfsMetrics>,
    ) -> Self {
        let policy = config
            .exports
            .first()
            .map(ServerPolicy::from_export)
            .unwrap_or_else(ServerPolicy::default_permissive);
        let peers = PeerSlots::new(config.max_connections);
        Self {
            config,
            processor,
            active_connections: Arc::new(AtomicUsize::new(0)),
            peers,
            policy: Arc::new(policy),
            metrics,
        }
    }

    /// Shared metrics handle. nvstor-server clones this so the periodic
    /// refresh task can read the atomic counters into prometheus gauges.
    pub fn metrics(&self) -> Arc<NfsMetrics> {
        Arc::clone(&self.metrics)
    }

    /// Run the NFS server, accepting connections on the given listener.
    pub async fn run(&self, listener: TcpListener) -> std::io::Result<()> {
        let (_sender, receiver) = tokio::sync::watch::channel(false);
        self.run_until_shutdown(listener, receiver).await
    }

    pub async fn run_until_shutdown(
        &self,
        listener: TcpListener,
        shutdown: tokio::sync::watch::Receiver<bool>,
    ) -> std::io::Result<()> {
        self.run_listeners_until_shutdown(vec![listener], shutdown)
            .await
    }

    /// Serve every listener (e.g. one per `--bind` address) until
    /// `shutdown`, sharing the connection limits. A failed accept (out of
    /// descriptors, a connection reset before it was accepted) never stops
    /// the server: it is logged and retried with backoff.
    pub async fn run_listeners_until_shutdown(
        &self,
        listeners: Vec<TcpListener>,
        mut shutdown: tokio::sync::watch::Receiver<bool>,
    ) -> std::io::Result<()> {
        if listeners.is_empty() {
            return Err(std::io::Error::new(
                std::io::ErrorKind::InvalidInput,
                "no NFS/TCP listener",
            ));
        }
        for listener in &listeners {
            info!("NFS v4 server listening on {}", listener.local_addr()?);
        }

        let mut failures = 0u32;
        loop {
            let accepted = tokio::select! {
                biased;
                _ = shutdown.wait_for(|stopping| *stopping) => break,
                accepted = accept_any(&listeners) => accepted,
            };
            let (stream, addr) = match accepted {
                Ok(accepted) => {
                    failures = 0;
                    accepted
                }
                Err(error) => {
                    failures = failures.saturating_add(1);
                    let delay = tcp_accept_backoff(failures);
                    warn!(%error, failures, delay_ms = delay.as_millis() as u64,
                          "NFS: accept failed; continuing");
                    tokio::select! {
                        biased;
                        _ = shutdown.wait_for(|stopping| *stopping) => break,
                        _ = tokio::time::sleep(delay) => continue,
                    }
                }
            };

            // Export access control: check if client IP is allowed.
            if !is_client_allowed(&addr, &self.config.exports) {
                warn!(%addr, "NFS: connection rejected: not in the export's client list (allow_clients / --allow-client)");
                drop(stream);
                continue;
            }

            let Some(peer) = self.peers.acquire(addr.ip()) else {
                warn!(%addr, max = self.peers.limit,
                      "NFS: connection rejected (limit per client address)");
                drop(stream);
                continue;
            };
            if let Err(current) = self.active_connections.fetch_update(
                Ordering::Relaxed,
                Ordering::Relaxed,
                |current| (current < self.config.max_connections).then_some(current + 1),
            ) {
                warn!(%addr, current, max = self.config.max_connections,
                      "NFS: connection rejected (limit)");
                drop(stream);
                continue;
            }

            self.metrics
                .active_connections
                .fetch_add(1, Ordering::Relaxed);
            debug!(%addr, "NFS: new connection");

            let active = self.active_connections.clone();
            let metrics_active = self.metrics.clone();
            let proc = self.processor.clone();
            let policy = self.policy.clone();
            let metrics = self.metrics.clone();
            let idle_timeout = self.config.idle_timeout;
            let write_timeout = self.config.write_timeout;
            let stop = shutdown.clone();
            tokio::spawn(async move {
                let _peer = peer;
                if let Err(e) = handle_connection(
                    stream,
                    proc,
                    policy,
                    metrics,
                    idle_timeout,
                    write_timeout,
                    stop,
                )
                .await
                {
                    debug!(%addr, error = %e, "NFS: connection closed");
                }
                active.fetch_sub(1, Ordering::Relaxed);
                metrics_active
                    .active_connections
                    .fetch_sub(1, Ordering::Relaxed);
            });
        }
        while self.active_connections() > 0 {
            tokio::time::sleep(std::time::Duration::from_millis(10)).await;
        }
        Ok(())
    }

    /// Current number of active connections.
    pub fn active_connections(&self) -> usize {
        self.active_connections.load(Ordering::Relaxed)
    }
}

/// The next connection on any of `listeners`.
async fn accept_any(
    listeners: &[TcpListener],
) -> std::io::Result<(TcpStream, std::net::SocketAddr)> {
    std::future::poll_fn(|cx| {
        for listener in listeners {
            if let std::task::Poll::Ready(result) = listener.poll_accept(cx) {
                return std::task::Poll::Ready(result);
            }
        }
        std::task::Poll::Pending
    })
    .await
}

/// Pause after the `failures`-th consecutive failed accept: 10 ms,
/// doubling, at most 1 s.
fn tcp_accept_backoff(failures: u32) -> std::time::Duration {
    std::time::Duration::from_millis(10u64 << failures.saturating_sub(1).min(7))
        .min(std::time::Duration::from_secs(1))
}

/// Bind one NFS/TCP listener per address on `port`. An IPv6 address is
/// bound IPv6-only, so `0.0.0.0` and `::` can be given together; the
/// unspecified addresses listen on every interface of their family.
pub fn bind_tcp_listeners(
    addrs: &[std::net::IpAddr],
    port: u16,
) -> std::io::Result<Vec<TcpListener>> {
    addrs
        .iter()
        .map(|ip| {
            let addr = std::net::SocketAddr::new(*ip, port);
            bind_tcp(addr).map_err(|error| {
                std::io::Error::new(error.kind(), format!("NFS/TCP {addr}: {error}"))
            })
        })
        .collect()
}

fn bind_tcp(addr: std::net::SocketAddr) -> std::io::Result<TcpListener> {
    use std::os::fd::AsRawFd;
    let socket = if addr.is_ipv6() {
        let socket = tokio::net::TcpSocket::new_v6()?;
        let on: libc::c_int = 1;
        // SAFETY: a valid socket descriptor and a c_int option value of
        // the size passed.
        let rc = unsafe {
            libc::setsockopt(
                socket.as_raw_fd(),
                libc::IPPROTO_IPV6,
                libc::IPV6_V6ONLY,
                &on as *const libc::c_int as *const libc::c_void,
                std::mem::size_of::<libc::c_int>() as libc::socklen_t,
            )
        };
        if rc != 0 {
            return Err(std::io::Error::last_os_error());
        }
        socket
    } else {
        tokio::net::TcpSocket::new_v4()?
    };
    socket.set_reuseaddr(true)?;
    socket.bind(addr)?;
    socket.listen(1024)
}

/// What the server does when its NFS/RDMA listener fails for good (device
/// removal, its event channel gone).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RdmaListenerFailure {
    /// Something else (NFS/TCP) is still serving: report RDMA as down,
    /// keep serving, and re-create the listener with backoff until it
    /// binds again (the device may come back).
    Rebind,
    /// Nothing else is serving (`--no-tcp`): return the error so the
    /// process exits and its supervisor restarts it.
    Exit,
}

/// Delay before the `attempt`-th rebind of a failed NFS/RDMA listener:
/// `first`, doubling, at most `max`.
fn rebind_backoff(
    attempt: u32,
    first: std::time::Duration,
    max: std::time::Duration,
) -> std::time::Duration {
    first
        .saturating_mul(1u32 << attempt.saturating_sub(1).min(16))
        .min(max)
}

/// Run a listener with `run` until `shutdown`. When `run` ends any other
/// way, the listener failed: `metrics` shows RDMA down, the failure is
/// logged at error, and per `on_failure` the error is returned or a new
/// listener is made with `bind`, retried with backoff (`first` doubling to
/// `max`) until it succeeds or shutdown starts.
#[cfg_attr(not(feature = "rdma"), allow(dead_code))]
pub(crate) async fn supervise_listener<L, B, R, F>(
    mut listener: L,
    mut bind: B,
    mut run: R,
    on_failure: RdmaListenerFailure,
    (first, max): (std::time::Duration, std::time::Duration),
    metrics: &NfsMetrics,
    mut shutdown: tokio::sync::watch::Receiver<bool>,
) -> std::io::Result<()>
where
    B: FnMut() -> std::io::Result<L>,
    R: FnMut(L) -> F,
    F: std::future::Future<Output = std::io::Result<()>>,
{
    let stopping = |shutdown: &tokio::sync::watch::Receiver<bool>| {
        *shutdown.borrow() || shutdown.has_changed().is_err()
    };
    loop {
        metrics.rdma_listener_up.store(1, Ordering::Relaxed);
        let result = run(listener).await;
        metrics.rdma_listener_up.store(0, Ordering::Relaxed);
        if stopping(&shutdown) {
            return result;
        }
        let error = result
            .err()
            .unwrap_or_else(|| std::io::Error::other("NFS/RDMA listener stopped"));
        metrics
            .rdma_listener_failures
            .fetch_add(1, Ordering::Relaxed);
        if on_failure == RdmaListenerFailure::Exit {
            tracing::error!(%error, "NFS/RDMA listener failed and nothing else is serving; exiting");
            return Err(error);
        }
        tracing::error!(%error, "NFS/RDMA listener failed; NFS/TCP keeps serving, NFS/RDMA is down until the listener can be re-created");
        let mut attempt = 0u32;
        listener = loop {
            attempt = attempt.saturating_add(1);
            let delay = rebind_backoff(attempt, first, max);
            tokio::select! {
                biased;
                _ = shutdown.wait_for(|stopping| *stopping) => return Ok(()),
                _ = tokio::time::sleep(delay) => {}
            }
            match bind() {
                Ok(listener) => {
                    info!(
                        attempt,
                        "NFS/RDMA listener re-created; NFS/RDMA is serving again"
                    );
                    break listener;
                }
                Err(error) => {
                    warn!(%error, attempt, next_ms = rebind_backoff(attempt + 1, first, max).as_millis() as u64,
                    "NFS/RDMA listener still cannot be created")
                }
            }
        };
    }
}

/// Handle a single NFS client connection.
///
/// The connection runs three concurrent halves:
///   * **Reader loop**: pulls RPC records off the socket as fast as they
///     arrive and spawns one worker task per record. Never blocks on
///     processing.
///   * **Worker tasks**: parse the call and dispatch the COMPOUND on
///     the blocking pool (RocksDB + sync VFS need it). Up to
///     `MAX_INFLIGHT_PER_CONN` of these run concurrently per connection.
///   * **Writer task**: drains a bounded mpsc of reply bytes and writes
///     them back to the socket in arrival-completion order. NFS clients
///     match replies to calls by xid, so reply order is irrelevant for
///     correctness.
///
/// This replaces the old single-in-flight-per-connection pattern, where
/// every COMPOUND ran to completion before the next record was even read.
/// On a 1 MiB-write workload that meant kernel writeback pipelining was
/// hitting a server that processed one frame at a time — the protocol's
/// natural concurrency was strangled at the socket layer.
///
/// Timeouts:
///   - `idle_timeout` bounds the wait for the next RPC record on the
///     reader half.
///   - `write_timeout` bounds each reply write on the writer half.
async fn handle_connection(
    stream: TcpStream,
    processor: Arc<CompoundProcessor>,
    policy: Arc<ServerPolicy>,
    metrics: Arc<NfsMetrics>,
    idle_timeout: std::time::Duration,
    write_timeout: std::time::Duration,
    mut shutdown: tokio::sync::watch::Receiver<bool>,
) -> Result<(), rpc::RpcError> {
    let (read_half, write_half) = stream.into_split();
    // 1 MiB buffers match the `rsize`/`wsize` a Linux NFS client
    // negotiates by default. Without this, every 1 MiB op
    // pays ~128 extra read/write syscalls.
    let mut reader = BufReader::with_capacity(1024 * 1024, read_half);
    let mut writer = BufWriter::with_capacity(1024 * 1024, write_half);

    // Bounded reply channel = backpressure on the read side. When the
    // writer or socket can't keep up, `tx.send().await` parks the worker
    // task; the reader keeps spawning workers until they all park; the
    // bounded number of in-flight workers caps memory growth.
    let (reply_tx, mut reply_rx) = mpsc::channel::<Vec<u8>>(MAX_INFLIGHT_PER_CONN);

    // Writer task: drain replies, write each as one RPC record under a
    // per-record write timeout. Exits on close (channel drop) or write
    // failure / timeout.
    let writer_task = tokio::spawn(async move {
        while let Some(reply) = reply_rx.recv().await {
            let res =
                tokio::time::timeout(write_timeout, rpc::write_record(&mut writer, &reply)).await;
            match res {
                Ok(Ok(())) => {}
                Ok(Err(e)) => {
                    debug!(error = %e, "NFS: reply write failed");
                    return Err::<(), rpc::RpcError>(e);
                }
                Err(_) => {
                    warn!(
                        secs = write_timeout.as_secs(),
                        "NFS: reply write timeout — client not draining"
                    );
                    return Ok(());
                }
            }
        }
        Ok(())
    });

    // Reader loop: read frames, dispatch concurrent workers.
    let read_outcome: Result<(), rpc::RpcError> = loop {
        let read = tokio::select! {
            biased;
            _ = shutdown.wait_for(|stopping| *stopping) => break Ok(()),
            read = tokio::time::timeout(idle_timeout, rpc::read_record(&mut reader)) => read,
        };
        let record = match read {
            Ok(Ok(r)) => r,
            Ok(Err(rpc::RpcError::ConnectionClosed)) => break Ok(()),
            Ok(Err(e)) => break Err(e),
            Err(_) => {
                debug!(secs = idle_timeout.as_secs(), "NFS: idle timeout");
                break Ok(());
            }
        };

        let proc = processor.clone();
        let pol = policy.clone();
        let m = metrics.clone();
        let tx = reply_tx.clone();
        tokio::spawn(async move {
            if let Some(reply) = dispatch_record(record, &proc, &pol, &m).await {
                // If the channel is full, this awaits — that's the
                // backpressure mechanism.
                let _ = tx.send(reply).await;
            }
        });
    };

    // Drop the sender so the writer task drains pending replies and exits.
    drop(reply_tx);
    let _ = writer_task.await;
    read_outcome
}

/// Parse one RPC record, run program/version/auth checks, and (for
/// COMPOUND) dispatch the processor on the blocking pool. Returns the
/// reply bytes ready to be framed by `rpc::write_record`, or `None` if
/// the record was so malformed there's nothing to reply to.
async fn dispatch_record(
    record: Vec<u8>,
    processor: &Arc<CompoundProcessor>,
    policy: &ServerPolicy,
    metrics: &Arc<NfsMetrics>,
) -> Option<Vec<u8>> {
    dispatch_record_on_transport(record, processor, policy, metrics, None)
        .await
        .map(|reply| reply.bytes)
}

struct RpcReply {
    bytes: Vec<u8>,
    #[cfg_attr(not(feature = "rdma"), allow(dead_code))]
    data_items: Vec<Option<crate::ddp::DataItem>>,
}

/// Direct-placement inputs from the RDMA transport.
#[cfg_attr(not(feature = "rdma"), allow(dead_code))]
struct RdmaInput {
    /// Leases READ result buffers; None places nothing (inline data).
    alloc: Option<Arc<dyn crate::ddp::DdpAllocator>>,
    /// Placed WRITE data; offsets relative to the start of the RPC record.
    args: Vec<crate::ddp::DdpArg>,
}

impl From<Vec<u8>> for RpcReply {
    fn from(bytes: Vec<u8>) -> Self {
        Self {
            bytes,
            data_items: Vec::new(),
        }
    }
}

async fn dispatch_record_on_transport(
    record: Vec<u8>,
    processor: &Arc<CompoundProcessor>,
    policy: &ServerPolicy,
    metrics: &Arc<NfsMetrics>,
    rdma: Option<RdmaInput>,
) -> Option<RpcReply> {
    let call = match rpc::parse_call(&record) {
        Ok(c) => c,
        Err(e) => {
            warn!(error = %e, "NFS: invalid RPC call");
            return None;
        }
    };

    if call.prog != rpc::NFS_PROGRAM {
        return Some(rpc::build_reply_error(call.xid, rpc::ACCEPT_PROG_UNAVAIL).into());
    }
    if call.vers != rpc::NFS_V4 {
        return Some(rpc::build_reply_prog_mismatch(call.xid, rpc::NFS_V4, rpc::NFS_V4).into());
    }
    if call.proc_num == 0 {
        // NULL — always accepted regardless of auth (RFC 5531 §11.1);
        // kernel clients ping with NULL+AUTH_NONE before COMPOUND.
        return Some(rpc::build_reply_success(call.xid, &[]).into());
    }
    if !policy.allowed_flavors.contains(&call.cred_flavor) {
        warn!(
            xid = call.xid,
            flavor = call.cred_flavor,
            "NFS: auth flavor rejected by export policy"
        );
        return Some(rpc::build_reply_auth_error(call.xid).into());
    }

    match call.proc_num {
        1 => {
            // COMPOUND — RocksDB + sync VFS sit underneath, so we still
            // have to use the blocking pool. Concurrent dispatch means we
            // can have many of these in flight simultaneously across the
            // pool rather than one per connection.
            let proc = processor.clone();
            let comp_policy = policy.compound.clone();
            let xid = call.xid;
            let header_len = record.len() - call.payload.len();
            let payload = call.payload;
            let auth_sys = call.auth_sys;
            // Placed data must lie inside the COMPOUND arguments.
            let rdma = match rdma {
                Some(mut input) => {
                    for arg in &mut input.args {
                        match arg
                            .offset
                            .checked_sub(header_len)
                            .filter(|offset| *offset <= payload.len())
                        {
                            Some(offset) => arg.offset = offset,
                            None => {
                                return Some(
                                    rpc::build_reply_error(xid, rpc::ACCEPT_GARBAGE_ARGS).into(),
                                )
                            }
                        }
                    }
                    Some(input)
                }
                None => None,
            };

            // Metrics: track inflight + wall-clock duration. Inflight
            // is decremented on either success or worker panic, so a
            // crashed COMPOUND doesn't leak the gauge.
            let start = Instant::now();
            metrics.compound_inflight.fetch_add(1, Ordering::Relaxed);

            let result = tokio::task::spawn_blocking(move || match rdma {
                Some(input) => proc.process_rdma(
                    &payload,
                    auth_sys.as_ref(),
                    &comp_policy,
                    input.alloc.as_deref(),
                    &input.args,
                ),
                None => (
                    proc.process_with_creds_policy(&payload, auth_sys.as_ref(), &comp_policy),
                    Vec::new(),
                ),
            })
            .await;

            metrics.compound_inflight.fetch_sub(1, Ordering::Relaxed);
            let is_err = result.is_err();
            metrics.record_compound(start, is_err);

            let (bytes, data_items) = match result {
                Ok(b) => b,
                Err(e) => {
                    warn!(error = %e, xid, "NFS: COMPOUND worker panicked");
                    return Some(rpc::build_reply_error(xid, rpc::ACCEPT_SYSTEM_ERR).into());
                }
            };
            let rpc_bytes = rpc::build_reply_success(xid, &bytes);
            let header_len = rpc_bytes.len() - bytes.len();
            Some(RpcReply {
                bytes: rpc_bytes,
                data_items: data_items
                    .into_iter()
                    .map(|item| {
                        item.map(|mut item| {
                            item.at += header_len;
                            item
                        })
                    })
                    .collect(),
            })
        }
        _ => Some(rpc::build_reply_error(call.xid, rpc::ACCEPT_PROC_UNAVAIL).into()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::vfs::Vfs;
    use crate::xdr::XdrWriter;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    fn export_allowing(patterns: &[&str]) -> Vec<NfsExport> {
        vec![NfsExport {
            allowed_clients: patterns.iter().map(|p| p.to_string()).collect(),
            ..NfsExport::default()
        }]
    }

    #[test]
    fn client_allow_list_handles_both_address_families() {
        let peer = |ip: &str| std::net::SocketAddr::new(ip.parse().expect("ip"), 900);
        let any = export_allowing(&["0.0.0.0/0", "::/0"]);
        for ip in ["192.0.2.34", "fd00:1:0:1::2", "::ffff:192.0.2.34"] {
            assert!(is_client_allowed(&peer(ip), &any), "{ip}");
        }
        // An IPv4 prefix never admits IPv6 peers, and the reverse.
        let v4 = export_allowing(&["192.0.2.0/26"]);
        assert!(is_client_allowed(&peer("192.0.2.34"), &v4));
        assert!(
            is_client_allowed(&peer("::ffff:192.0.2.34"), &v4),
            "v4-mapped peer is IPv4"
        );
        assert!(!is_client_allowed(&peer("192.0.2.64"), &v4));
        assert!(!is_client_allowed(&peer("fd00:1::1"), &v4));
        let rails = export_allowing(&["fd00:1::/32"]);
        assert!(is_client_allowed(&peer("fd00:1:0:1::2"), &rails));
        assert!(!is_client_allowed(&peer("fd00:2::1"), &rails));
        assert!(!is_client_allowed(&peer("192.0.2.34"), &rails));
        // Exact addresses, and malformed patterns that must match nothing.
        assert!(is_client_allowed(
            &peer("fd00::1"),
            &export_allowing(&["fd00::1"])
        ));
        assert!(!is_client_allowed(
            &peer("fd00::2"),
            &export_allowing(&["fd00::1"])
        ));
        let bad = export_allowing(&["192.0.2.0/33", "fd00::/129", "nonsense/8", "10.0.0.0/x"]);
        assert!(!is_client_allowed(&peer("192.0.2.1"), &bad));
        assert!(!is_client_allowed(&peer("fd00::1"), &bad));
    }

    #[test]
    fn an_export_that_names_no_clients_admits_loopback_only() {
        let peer = |ip: &str| std::net::SocketAddr::new(ip.parse().expect("ip"), 900);
        for exports in [export_allowing(&[]), vec![], vec![NfsExport::default()]] {
            for ip in ["127.0.0.1", "127.1.2.3", "::1", "::ffff:127.0.0.1"] {
                assert!(is_client_allowed(&peer(ip), &exports), "{ip} is loopback");
            }
            for ip in ["192.0.2.34", "10.0.0.5", "fd00::1", "::ffff:192.0.2.34"] {
                assert!(!is_client_allowed(&peer(ip), &exports), "{ip} is not named");
            }
        }
        assert!(
            is_client_allowed(&peer("192.0.2.34"), &export_allowing(&["*"])),
            "* is every client"
        );
    }

    /// Every pattern `validate_client_pattern` accepts is one
    /// `matches_client` understands, and every malformed one is refused.
    #[test]
    fn client_patterns_are_validated() {
        for ok in [
            "*",
            "10.0.0.5",
            "10.0.0.0/8",
            "0.0.0.0/0",
            "::/0",
            "fd00:1::/32",
            "fd00::1/128",
            "192.0.2.0/32",
        ] {
            assert!(validate_client_pattern(ok).is_ok(), "{ok}");
        }
        for bad in [
            "",
            "10.0.0.0/33",
            "fd00::/129",
            "nonsense/8",
            "10.0.0.0/x",
            "10.0.0.0/",
            "host.example",
            "10.0.0.0/8/8",
            "10.0.0.0/-1",
        ] {
            assert!(validate_client_pattern(bad).is_err(), "{bad}");
        }
        // Accepted prefixes admit what they say, and only their own family.
        let rails = export_allowing(&["fd00:1::/32"]);
        assert!(validate_client_pattern("fd00:1::/32").is_ok());
        let peer = std::net::SocketAddr::new("fd00:1::7".parse().expect("ip"), 900);
        assert!(is_client_allowed(&peer, &rails));
        let v4 = std::net::SocketAddr::new("10.1.2.3".parse().expect("ip"), 900);
        assert!(
            !is_client_allowed(&v4, &export_allowing(&["::/0"])),
            "::/0 does not admit IPv4"
        );
    }

    fn test_processor() -> Arc<CompoundProcessor> {
        let dir = tempfile::TempDir::new().expect("tempdir");
        let metadata = Arc::new(nvstor_metadata::MetadataStore::open(dir.path()).expect("open"));
        let backend =
            nvstor_storage::backend::PosixBackend::new(dir.path().to_str().expect("path"))
                .expect("backend");
        let mgr = Arc::new(nvstor_storage::manager::ChunkManager::new(
            Box::new(backend),
            metadata,
            Default::default(),
        ));
        let vfs = Arc::new(Vfs::new(mgr, "nfs-test/"));
        // Leak the TempDir to keep it alive for the test duration.
        std::mem::forget(dir);
        Arc::new(CompoundProcessor::new(vfs))
    }

    /// Build an AUTH_SYS credential body for test calls.
    fn auth_sys_body(uid: u32, gid: u32) -> Vec<u8> {
        let mut w = XdrWriter::new();
        w.put_u32(0); // stamp
        w.put_string("test-host"); // machinename
        w.put_u32(uid);
        w.put_u32(gid);
        w.put_u32(0); // 0 aux gids
        w.into_bytes()
    }

    /// Build a minimal NFS NULL RPC call record using AUTH_SYS so the default
    /// `sec=["sys"]` export policy accepts it.
    fn build_null_call(xid: u32) -> Vec<u8> {
        let mut w = XdrWriter::new();
        w.put_u32(xid);
        w.put_u32(rpc::MSG_TYPE_CALL);
        w.put_u32(rpc::RPC_VERSION);
        w.put_u32(rpc::NFS_PROGRAM);
        w.put_u32(rpc::NFS_V4);
        w.put_u32(0); // proc = NULL
        w.put_u32(rpc::AUTH_SYS);
        w.put_opaque(&auth_sys_body(0, 0));
        w.put_u32(rpc::AUTH_NONE);
        w.put_opaque(&[]);
        w.into_bytes()
    }

    /// Build a minimal NFS COMPOUND call using AUTH_SYS credentials.
    fn build_compound_call(xid: u32, tag: &str, ops: &[u8]) -> Vec<u8> {
        let mut w = XdrWriter::new();
        w.put_u32(xid);
        w.put_u32(rpc::MSG_TYPE_CALL);
        w.put_u32(rpc::RPC_VERSION);
        w.put_u32(rpc::NFS_PROGRAM);
        w.put_u32(rpc::NFS_V4);
        w.put_u32(1); // proc = COMPOUND
        w.put_u32(rpc::AUTH_SYS);
        w.put_opaque(&auth_sys_body(0, 0));
        w.put_u32(rpc::AUTH_NONE);
        w.put_opaque(&[]);
        // COMPOUND args: tag + minorversion + ops
        w.put_string(tag);
        w.put_u32(0); // minorversion
        w.put_raw(ops);
        w.into_bytes()
    }

    /// Write a record-marked message to a stream.
    async fn write_record(stream: &mut TcpStream, data: &[u8]) {
        let header = 0x8000_0000 | (data.len() as u32);
        stream.write_u32(header).await.unwrap();
        stream.write_all(data).await.unwrap();
        stream.flush().await.unwrap();
    }

    /// Read a record-marked message from a stream.
    async fn read_record(stream: &mut TcpStream) -> Vec<u8> {
        let header = stream.read_u32().await.unwrap();
        let len = (header & 0x7FFF_FFFF) as usize;
        let mut data = vec![0u8; len];
        stream.read_exact(&mut data).await.unwrap();
        data
    }

    #[tokio::test]
    async fn null_procedure() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        let server = NfsServer::new(test_processor(), NfsServerConfig::default());
        tokio::spawn(async move { server.run(listener).await.unwrap() });

        let mut stream = TcpStream::connect(addr).await.unwrap();

        // Send NULL RPC call
        let call = build_null_call(42);
        write_record(&mut stream, &call).await;

        // Read reply
        let reply = read_record(&mut stream).await;
        let mut r = crate::xdr::XdrReader::new(&reply);
        assert_eq!(r.get_u32().unwrap(), 42); // xid
        assert_eq!(r.get_u32().unwrap(), rpc::MSG_TYPE_REPLY);
        assert_eq!(r.get_u32().unwrap(), rpc::MSG_ACCEPTED);
    }

    /// Heap-backed stand-in for the RDMA pool allocator.
    struct HeapAllocator;
    impl crate::ddp::DdpAllocator for HeapAllocator {
        fn lease(&self, len: usize) -> Option<Box<dyn crate::ddp::DdpBuffer>> {
            Some(Box::new(crate::ddp::HeapBuffer(vec![0; len])))
        }
    }

    /// Apply a reply plan's RDMA WRITEs to a simulated remote region.
    fn apply_writes(plan: &crate::rpcrdma::chunks::ReplyPlan, remote: &mut [u8]) {
        use crate::rpcrdma::chunks::Source;
        for transfer in &plan.transfers {
            let bytes = match &transfer.source {
                Source::Payload(range) => &plan.payload[range.clone()],
                Source::Item { item, range } => &plan.items[*item].bytes()[range.clone()],
            };
            let offset = transfer.remote.offset as usize;
            remote[offset..offset + bytes.len()].copy_from_slice(bytes);
        }
    }

    /// Exercise the real NFS dispatcher and filesystem with a simulated remote
    /// memory region: WRITE data placed by the transport (never inlined into
    /// the call), READ data placed into a transport buffer (never inlined into
    /// the reply), and the inline fallback. Checks wire planning, not verbs.
    #[tokio::test]
    async fn rdma_chunked_write_read_uses_shared_nfs_policy_and_storage() {
        use crate::ddp::{DdpArg, HeapBuffer};
        use crate::nfs4::*;
        use crate::rpcrdma::{chunks::*, nfs::classify_read_chunks};
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("checkpoint"), b"old").unwrap();
        let mut vfs = crate::passthrough::PassthroughVfs::new(dir.path()).unwrap();
        vfs.set_read_write();
        let processor = Arc::new(CompoundProcessor::new_passthrough(Arc::new(vfs)));
        let policy = ServerPolicy::from_export(&NfsExport {
            root_squash: false,
            ..Default::default()
        });
        let metrics = Arc::new(NfsMetrics::new());
        let data: Vec<_> = (0..1024 * 1024 + 3).map(|i| (i * 17 % 251) as u8).collect();
        let mut ops = XdrWriter::new();
        ops.put_u32(3);
        ops.put_u32(OP_PUTROOTFH);
        ops.put_u32(OP_LOOKUP);
        ops.put_string("checkpoint");
        ops.put_u32(OP_WRITE);
        ops.put_raw(&[0; 16]);
        ops.put_u64(0);
        ops.put_u32(2);
        ops.put_opaque(&data);
        let full = build_compound_call(42, "", &ops.into_bytes());
        let padding = (4 - data.len() % 4) % 4;
        let start = full.len() - data.len() - padding;
        // As sent: the data and its padding are absent from the inline message.
        let reduced = full[..start].to_vec();
        let reads = vec![ReadChunk {
            position: start as u32,
            segments: vec![
                Segment {
                    handle: 1,
                    length: 257,
                    offset: 0,
                },
                Segment {
                    handle: 1,
                    length: (data.len() - 257) as u32,
                    offset: 257,
                },
            ],
        }];
        let uses = classify_read_chunks(&reduced, &reads).unwrap();
        assert_eq!(uses, vec![ChunkUse::Placed]);
        let (record, placed) = rebuild(42, &reduced, &reads, &uses, &[&data]).unwrap();
        assert_eq!(record, reduced);
        assert_eq!(placed, vec![(0, start)]);
        let placed_write = || RdmaInput {
            alloc: None,
            args: vec![DdpArg {
                offset: start,
                len: data.len(),
                buf: Box::new(HeapBuffer(data.clone())),
            }],
        };
        let reply = dispatch_record_on_transport(
            record.clone(),
            &processor,
            &policy,
            &metrics,
            Some(placed_write()),
        )
        .await
        .unwrap();
        assert_eq!(&reply.bytes[24..28], &NFS4_OK.to_be_bytes());
        assert_eq!(std::fs::read(dir.path().join("checkpoint")).unwrap(), data);

        let mut ops = XdrWriter::new();
        ops.put_u32(4);
        ops.put_u32(OP_PUTROOTFH);
        ops.put_u32(OP_LOOKUP);
        ops.put_string("checkpoint");
        ops.put_u32(OP_READ);
        ops.put_raw(&[0; 16]);
        ops.put_u64(0);
        ops.put_u32(data.len() as u32);
        ops.put_u32(OP_GETFH);
        let read_call = build_compound_call(43, "", &ops.into_bytes());
        let read = Call {
            xid: 43,
            credits: 16,
            reads: vec![],
            writes: vec![vec![
                Segment {
                    handle: 2,
                    offset: 16,
                    length: 100,
                },
                Segment {
                    handle: 2,
                    offset: 116,
                    length: (data.len() + 100) as u32,
                },
            ]],
            reply: None,
            inline: vec![],
        };
        for alloc in [
            Some(Arc::new(HeapAllocator) as Arc<dyn crate::ddp::DdpAllocator>),
            None,
        ] {
            let placed_read = alloc.is_some();
            let input = RdmaInput {
                alloc,
                args: Vec::new(),
            };
            let reply = dispatch_record_on_transport(
                read_call.clone(),
                &processor,
                &policy,
                &metrics,
                Some(input),
            )
            .await
            .unwrap();
            assert_eq!(reply.data_items.len(), 1);
            let item = reply.data_items[0].as_ref().unwrap();
            let at = item.at;
            assert_eq!(item.len, data.len());
            assert_eq!(item.external.is_some(), placed_read);
            if !placed_read {
                assert_eq!(&reply.bytes[at..at + data.len()], data);
            }
            let plan = plan_reply(&read, 16, reply.bytes.clone(), reply.data_items, 4096).unwrap();
            let mut remote = vec![0xaa; data.len() + 128];
            apply_writes(&plan, &mut remote);
            assert_eq!(&remote[16..16 + data.len()], data);
            assert!(remote[..16].iter().all(|b| *b == 0xaa));
            assert!(remote[16 + data.len()..].iter().all(|b| *b == 0xaa));
            // The client rebuilds the reply by putting the data back at `at`.
            let reduced = &plan.send[28 + 8 + 2 * 16..];
            let mut reconstructed = reduced[..at].to_vec();
            reconstructed.extend_from_slice(&remote[16..16 + data.len()]);
            reconstructed.resize(reconstructed.len() + padding, 0);
            reconstructed.extend_from_slice(&reduced[at..]);
            let full_reply = if placed_read {
                [
                    &reply.bytes[..at],
                    &data[..],
                    &vec![0; padding][..],
                    &reply.bytes[at..],
                ]
                .concat()
            } else {
                reply.bytes.clone()
            };
            assert_eq!(reconstructed, full_reply);
        }

        let mut readonly = policy.clone();
        readonly.compound.read_only = true;
        std::fs::write(dir.path().join("checkpoint"), b"unchanged").unwrap();
        let denied = dispatch_record_on_transport(
            record,
            &processor,
            &readonly,
            &metrics,
            Some(placed_write()),
        )
        .await
        .unwrap();
        assert_eq!(&denied.bytes[24..28], &NFS4ERR_ROFS.to_be_bytes());
        assert_eq!(
            std::fs::read(dir.path().join("checkpoint")).unwrap(),
            b"unchanged"
        );
    }

    /// A pass-through export checks every request against the
    /// caller's AUTH_SYS credentials, identically over TCP and RDMA (with
    /// and without placed READ data): a mode-0600 file is readable by its
    /// owner only, root is squashed by default, ACCESS reports what is
    /// allowed, and a WRITE by a non-owner without write permission fails.
    #[tokio::test]
    async fn passthrough_permissions_follow_caller_credentials_on_every_transport() {
        use crate::nfs4::*;
        let dir = tempfile::tempdir().unwrap();
        let data: Vec<u8> = (0..64 * 1024).map(|i| (i % 251) as u8).collect();
        for (name, mode) in [("private", 0o600), ("shared", 0o644)] {
            let path = dir.path().join(name);
            std::fs::write(&path, &data).unwrap();
            std::fs::set_permissions(&path, std::os::unix::fs::PermissionsExt::from_mode(mode))
                .unwrap();
        }
        let mut vfs = crate::passthrough::PassthroughVfs::new(dir.path()).unwrap();
        vfs.set_read_write();
        let processor = Arc::new(CompoundProcessor::new_passthrough(Arc::new(vfs)));
        // Root squashing on, as nvstor-nfs-export runs by default.
        let policy = ServerPolicy::from_export(&NfsExport::default());
        let metrics = Arc::new(NfsMetrics::new());
        // SAFETY: geteuid / getegid have no preconditions and cannot fail.
        let (owner, group) = unsafe { (libc::geteuid(), libc::getegid()) };
        let stranger = if owner == 4_242_424 {
            4_242_425
        } else {
            4_242_424
        };
        let call = |xid: u32, uid: u32, gid: u32, name: &str, op: (u32, Vec<u8>)| {
            let mut w = XdrWriter::new();
            w.put_u32(xid);
            w.put_u32(rpc::MSG_TYPE_CALL);
            w.put_u32(rpc::RPC_VERSION);
            w.put_u32(rpc::NFS_PROGRAM);
            w.put_u32(rpc::NFS_V4);
            w.put_u32(1);
            w.put_u32(rpc::AUTH_SYS);
            w.put_opaque(&auth_sys_body(uid, gid));
            w.put_u32(rpc::AUTH_NONE);
            w.put_opaque(&[]);
            w.put_string("");
            w.put_u32(0);
            w.put_u32(3);
            w.put_u32(OP_PUTROOTFH);
            w.put_u32(OP_LOOKUP);
            w.put_string(name);
            w.put_u32(op.0);
            w.put_raw(&op.1);
            w.into_bytes()
        };
        let read = || {
            let mut w = XdrWriter::new();
            w.put_raw(&[0; 16]);
            w.put_u64(0);
            w.put_u32(data.len() as u32);
            (OP_READ, w.into_bytes())
        };
        let write = || {
            let mut w = XdrWriter::new();
            w.put_raw(&[0; 16]);
            w.put_u64(0);
            w.put_u32(2);
            w.put_opaque(b"overwritten");
            (OP_WRITE, w.into_bytes())
        };
        let access = || (OP_ACCESS, 0x3fu32.to_be_bytes().to_vec());
        let status = |reply: &RpcReply| u32::from_be_bytes(reply.bytes[24..28].try_into().unwrap());
        for transport in ["tcp", "rdma-inline", "rdma-placed"] {
            let mut xid = 100;
            let mut run = |uid: u32, gid: u32, name: &str, op: (u32, Vec<u8>)| {
                xid += 1;
                let record = call(xid, uid, gid, name, op);
                let input = match transport {
                    "tcp" => None,
                    "rdma-inline" => Some(RdmaInput {
                        alloc: None,
                        args: Vec::new(),
                    }),
                    _ => Some(RdmaInput {
                        alloc: Some(Arc::new(HeapAllocator) as Arc<dyn crate::ddp::DdpAllocator>),
                        args: Vec::new(),
                    }),
                };
                let processor = processor.clone();
                let (policy, metrics) = (policy.clone(), metrics.clone());
                async move {
                    dispatch_record_on_transport(record, &processor, &policy, &metrics, input)
                        .await
                        .unwrap()
                }
            };
            assert_eq!(
                status(&run(owner, group, "private", read()).await),
                NFS4_OK,
                "{transport}: owner reads 0600"
            );
            assert_eq!(
                status(&run(stranger, stranger, "private", read()).await),
                NFS4ERR_ACCESS,
                "{transport}: another uid must not read a 0600 file"
            );
            assert_eq!(
                status(&run(0, 0, "private", read()).await),
                NFS4ERR_ACCESS,
                "{transport}: root is squashed to the anonymous uid"
            );
            assert_eq!(
                status(&run(stranger, stranger, "shared", read()).await),
                NFS4_OK,
                "{transport}: 0644 is world-readable"
            );
            assert_eq!(
                status(&run(stranger, stranger, "shared", write()).await),
                NFS4ERR_ACCESS,
                "{transport}: 0644 is not world-writable"
            );
            // ACCESS: READ|LOOKUP|MODIFY|EXTEND|DELETE|EXECUTE asked; a
            // regular file supports READ|MODIFY|EXTEND|EXECUTE.
            let reply = run(stranger, stranger, "shared", access()).await;
            assert_eq!(status(&reply), NFS4_OK);
            assert_eq!(
                &reply.bytes[reply.bytes.len() - 8..],
                &[0x2du32.to_be_bytes(), 0x01u32.to_be_bytes()].concat()[..],
                "{transport}: supported READ|MODIFY|EXTEND|EXECUTE, granted READ"
            );
            let reply = run(owner, group, "private", access()).await;
            assert_eq!(
                &reply.bytes[reply.bytes.len() - 4..],
                &0x0du32.to_be_bytes(),
                "{transport}: the owner may read and change, not execute, a 0600 file"
            );
        }
        assert_eq!(
            std::fs::read(dir.path().join("shared")).unwrap(),
            data,
            "denied WRITEs changed nothing"
        );
    }

    /// Placed data must lie inside the COMPOUND arguments.
    #[tokio::test]
    async fn rdma_placed_data_outside_the_payload_is_garbage_args() {
        use crate::ddp::{DdpArg, HeapBuffer};
        let mut ops = XdrWriter::new();
        ops.put_u32(0);
        let record = build_compound_call(44, "", &ops.into_bytes());
        let input = RdmaInput {
            alloc: None,
            args: vec![DdpArg {
                offset: 4,
                len: 1,
                buf: Box::new(HeapBuffer(vec![1])),
            }],
        };
        let metrics = Arc::new(NfsMetrics::new());
        let policy = ServerPolicy::from_export(&NfsExport::default());
        let reply =
            dispatch_record_on_transport(record, &test_processor(), &policy, &metrics, Some(input))
                .await
                .unwrap();
        let mut r = crate::xdr::XdrReader::new(&reply.bytes);
        assert_eq!(r.get_u32().unwrap(), 44);
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_opaque().unwrap();
        assert_eq!(r.get_u32().unwrap(), rpc::ACCEPT_GARBAGE_ARGS);
    }

    #[tokio::test]
    async fn compound_procedure() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        let server = NfsServer::new(test_processor(), NfsServerConfig::default());
        tokio::spawn(async move { server.run(listener).await.unwrap() });

        let mut stream = TcpStream::connect(addr).await.unwrap();

        // Send COMPOUND with 0 ops
        let mut ops = XdrWriter::new();
        ops.put_u32(0); // 0 operations
        let call = build_compound_call(99, "test", &ops.into_bytes());
        write_record(&mut stream, &call).await;

        // Read reply
        let reply = read_record(&mut stream).await;
        let mut r = crate::xdr::XdrReader::new(&reply);
        assert_eq!(r.get_u32().unwrap(), 99); // xid
        assert_eq!(r.get_u32().unwrap(), rpc::MSG_TYPE_REPLY);
        assert_eq!(r.get_u32().unwrap(), rpc::MSG_ACCEPTED);
        // Skip verf
        r.get_u32().unwrap();
        r.get_opaque().unwrap();
        // Accept status
        assert_eq!(r.get_u32().unwrap(), rpc::ACCEPT_SUCCESS);
        // COMPOUND reply: status + tag + results
        assert_eq!(r.get_u32().unwrap(), 0); // NFS4_OK
    }

    /// Connection must accept N back-to-back records
    /// without waiting for the previous reply. Old server forced strict
    /// req→reply→req sequencing per connection; new server reads ahead
    /// and dispatches concurrently.
    #[tokio::test]
    async fn pipelined_calls_complete() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        let server = NfsServer::new(test_processor(), NfsServerConfig::default());
        tokio::spawn(async move { server.run(listener).await.unwrap() });

        let mut stream = TcpStream::connect(addr).await.unwrap();

        // Pipeline: send 8 calls, only then start reading replies. With
        // the old serial dispatch this still works because the server
        // happens to handle them one-by-one fast enough; the structural
        // proof comes from `pipelined_compound_throughput`
        // below. Here we just check that pipelining produces all replies
        // and that xids match (replies may arrive in any order — dispatch
        // does not guarantee order, kernel matches by xid).
        const N: u32 = 8;
        for xid in 0..N {
            let call = build_null_call(xid);
            write_record(&mut stream, &call).await;
        }

        let mut seen = std::collections::HashSet::new();
        for _ in 0..N {
            let reply = read_record(&mut stream).await;
            let mut r = crate::xdr::XdrReader::new(&reply);
            let xid = r.get_u32().unwrap();
            assert!(xid < N, "unexpected xid {xid}");
            assert!(seen.insert(xid), "duplicate xid {xid} in replies");
            assert_eq!(r.get_u32().unwrap(), rpc::MSG_TYPE_REPLY);
            assert_eq!(r.get_u32().unwrap(), rpc::MSG_ACCEPTED);
        }
        assert_eq!(seen.len(), N as usize, "did not receive all replies");
    }

    /// Structural test: two connections each holding a slow
    /// COMPOUND must complete in roughly `slow_op_ms`, not `2 ×
    /// slow_op_ms`. With sequential dispatch, every COMPOUND would block
    /// the connection it arrived on AND inflate wall time linearly with
    /// concurrency on the same blocking-pool slot. Now they overlap
    /// because each connection spawns its own worker tasks.
    ///
    /// We use a custom CompoundProcessor wrapper would be cleaner, but
    /// the public processor doesn't expose a hook for sleep. Instead we
    /// use the time of `compound_procedure` × N: if dispatch were
    /// serialized at the connection level (old behavior), 32 pipelined
    /// COMPOUNDs would take noticeably longer than the same 32 sent
    /// serially across 32 connections. We assert the pipelined form
    /// finishes in well under N × per-op time.
    #[tokio::test]
    async fn pipelined_compound_throughput() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        let server = NfsServer::new(test_processor(), NfsServerConfig::default());
        tokio::spawn(async move { server.run(listener).await.unwrap() });

        let mut stream = TcpStream::connect(addr).await.unwrap();

        // 32 zero-op COMPOUNDs (the cheapest path through the dispatcher
        // that still exercises spawn_blocking). The wall time should be
        // dominated by tokio scheduling, not 32 sequential blocking-pool
        // hops.
        const N: u32 = 32;
        let start = std::time::Instant::now();
        for xid in 100..100 + N {
            let mut ops = XdrWriter::new();
            ops.put_u32(0); // zero ops
            let call = build_compound_call(xid, "phase-c", &ops.into_bytes());
            write_record(&mut stream, &call).await;
        }
        for _ in 0..N {
            let _reply = read_record(&mut stream).await;
        }
        let elapsed = start.elapsed();

        // Generous bound — we're proving "not serialized to N slow
        // syscalls", not benchmarking. 5 s for 32 zero-op COMPOUNDs
        // would already be a regression.
        assert!(
            elapsed < std::time::Duration::from_secs(5),
            "32 pipelined COMPOUNDs took {elapsed:?} — looks serialized"
        );
    }

    #[tokio::test]
    async fn wrong_program_rejected() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        let server = NfsServer::new(test_processor(), NfsServerConfig::default());
        tokio::spawn(async move { server.run(listener).await.unwrap() });

        let mut stream = TcpStream::connect(addr).await.unwrap();

        // Send call with wrong program number
        let mut w = XdrWriter::new();
        w.put_u32(1);
        w.put_u32(rpc::MSG_TYPE_CALL);
        w.put_u32(rpc::RPC_VERSION);
        w.put_u32(99999); // Wrong program
        w.put_u32(rpc::NFS_V4);
        w.put_u32(0);
        w.put_u32(rpc::AUTH_NONE);
        w.put_opaque(&[]);
        w.put_u32(rpc::AUTH_NONE);
        w.put_opaque(&[]);
        write_record(&mut stream, &w.into_bytes()).await;

        let reply = read_record(&mut stream).await;
        let mut r = crate::xdr::XdrReader::new(&reply);
        r.get_u32().unwrap(); // xid
        r.get_u32().unwrap(); // type
        r.get_u32().unwrap(); // accepted
        r.get_u32().unwrap(); // verf flavor
        r.get_opaque().unwrap(); // verf body
        assert_eq!(r.get_u32().unwrap(), rpc::ACCEPT_PROG_UNAVAIL);
    }

    #[tokio::test]
    async fn connection_limit() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        let server = Arc::new(NfsServer::new(
            test_processor(),
            NfsServerConfig {
                max_connections: 2,
                ..Default::default()
            },
        ));
        let server_clone = server.clone();
        tokio::spawn(async move { server_clone.run(listener).await.unwrap() });

        // Open 2 connections and verify they work. From two addresses: one
        // address may hold only a quarter of max_connections (at least 1).
        let mut c1 = connect_from("127.0.0.1", addr).await;
        let mut c2 = connect_from("127.0.0.2", addr).await;
        tokio::time::sleep(std::time::Duration::from_millis(50)).await;

        // Both should respond to NULL calls
        let call = build_null_call(1);
        write_record(&mut c1, &call).await;
        let _ = read_record(&mut c1).await;
        write_record(&mut c2, &call).await;
        let _ = read_record(&mut c2).await;

        // Server should have accepted both
        assert!(server.active_connections() >= 1);
    }

    /// A slowloris client that opens a socket and sends nothing
    /// must be dropped after `idle_timeout` instead of holding a slot forever.
    #[test]
    fn peer_slots_cap_one_address_and_release_on_drop() {
        use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};
        let slots = PeerSlots::new(1000);
        assert_eq!(slots.limit, MAX_CONNECTIONS_PER_PEER);
        let a = IpAddr::V4(Ipv4Addr::new(10, 0, 0, 1));
        let held: Vec<_> = (0..MAX_CONNECTIONS_PER_PEER)
            .map(|_| slots.acquire(a).unwrap())
            .collect();
        assert!(slots.acquire(a).is_none());
        // The same client through the dual-stack listener.
        assert!(slots
            .acquire(IpAddr::V6(Ipv4Addr::new(10, 0, 0, 1).to_ipv6_mapped()))
            .is_none());
        // Other clients are unaffected.
        let b = slots.acquire(IpAddr::V6(Ipv6Addr::LOCALHOST)).unwrap();
        drop(held);
        assert!(slots.acquire(a).is_some());
        drop(b);
        assert!(slots.held.lock().is_empty());
    }

    #[test]
    fn one_peer_cannot_take_every_shared_slot() {
        for max in [1, 2, 4, 10, 100, 1000, 1024, 100_000] {
            let limit = PeerSlots::new(max).limit;
            assert!((1..=MAX_CONNECTIONS_PER_PEER).contains(&limit));
            assert!(max < 4 || limit * 4 <= max, "max {max} limit {limit}");
        }
    }

    /// One server serves several bound addresses, of both families,
    /// on the same port; an IPv6 listener is IPv6-only, so 0.0.0.0 and ::
    /// can both be bound.
    #[tokio::test]
    async fn serves_every_bound_address() {
        use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};
        let probe = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let port = probe.local_addr().unwrap().port();
        drop(probe);
        let addrs = [
            IpAddr::V4(Ipv4Addr::LOCALHOST),
            IpAddr::V6(Ipv6Addr::LOCALHOST),
        ];
        let listeners = match bind_tcp_listeners(&addrs, port) {
            Ok(listeners) => listeners,
            // Hosts without IPv6 loopback cannot run the dual-family part.
            Err(error) if error.kind() == std::io::ErrorKind::AddrNotAvailable => return,
            Err(error) => panic!("bind: {error}"),
        };
        let server = Arc::new(NfsServer::new(test_processor(), NfsServerConfig::default()));
        let (stop, stopped) = tokio::sync::watch::channel(false);
        let run = server.clone();
        let task =
            tokio::spawn(async move { run.run_listeners_until_shutdown(listeners, stopped).await });
        for ip in addrs {
            let mut stream = TcpStream::connect((ip, port)).await.unwrap();
            write_record(&mut stream, &build_null_call(3)).await;
            assert!(!read_record(&mut stream).await.is_empty(), "NULL over {ip}");
        }
        stop.send_replace(true);
        assert!(task.await.unwrap().is_ok());
        // Both wildcard addresses on one port.
        let wildcard = [
            IpAddr::V4(Ipv4Addr::UNSPECIFIED),
            IpAddr::V6(Ipv6Addr::UNSPECIFIED),
        ];
        let probe = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let free = probe.local_addr().unwrap().port();
        drop(probe);
        assert_eq!(bind_tcp_listeners(&wildcard, free).unwrap().len(), 2);
        // An address already in use is an error naming it.
        let taken = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let error = bind_tcp_listeners(
            &[IpAddr::V4(Ipv4Addr::LOCALHOST)],
            taken.local_addr().unwrap().port(),
        )
        .unwrap_err();
        assert!(error.to_string().contains("127.0.0.1"), "{error}");
    }

    #[tokio::test]
    async fn no_listener_is_an_error() {
        let server = NfsServer::new(test_processor(), NfsServerConfig::default());
        let (_stop, stopped) = tokio::sync::watch::channel(false);
        assert!(server
            .run_listeners_until_shutdown(Vec::new(), stopped)
            .await
            .is_err());
    }

    #[test]
    fn tcp_accept_backoff_doubles_to_a_second() {
        assert_eq!(tcp_accept_backoff(1), std::time::Duration::from_millis(10));
        assert_eq!(tcp_accept_backoff(2), std::time::Duration::from_millis(20));
        assert_eq!(tcp_accept_backoff(8), std::time::Duration::from_secs(1));
        assert_eq!(
            tcp_accept_backoff(u32::MAX),
            std::time::Duration::from_secs(1)
        );
    }

    const FAST: (std::time::Duration, std::time::Duration) = (
        std::time::Duration::from_millis(1),
        std::time::Duration::from_millis(4),
    );

    fn enodev() -> std::io::Error {
        std::io::Error::from_raw_os_error(libc::ENODEV)
    }

    /// With TCP still serving, a failed RDMA listener is re-created
    /// (after a failed attempt) and serves again; metrics show it down
    /// and back up, and only shutdown ends the supervisor.
    #[tokio::test]
    async fn failed_rdma_listener_is_recreated_while_tcp_serves() {
        let metrics = NfsMetrics::new();
        let (stop, stopped) = tokio::sync::watch::channel(false);
        let binds = std::cell::Cell::new(0u32);
        let runs = std::cell::RefCell::new(Vec::new());
        let stopper = stopped.clone();
        let result = supervise_listener(
            1u32,
            || {
                binds.set(binds.get() + 1);
                if binds.get() == 1 {
                    Err(enodev())
                } else {
                    Ok(10 + binds.get())
                }
            },
            |listener| {
                runs.borrow_mut().push(listener);
                let mut stopper = stopper.clone();
                let stop = stop.clone();
                let up = metrics.rdma_listener_up.load(Ordering::Relaxed);
                async move {
                    assert_eq!(up, 1, "up while the listener runs");
                    if listener == 1 {
                        return Err(enodev());
                    }
                    stop.send_replace(true);
                    let _ = stopper.wait_for(|stopping| *stopping).await;
                    Ok(())
                }
            },
            RdmaListenerFailure::Rebind,
            FAST,
            &metrics,
            stopped,
        )
        .await;
        assert!(result.is_ok());
        assert_eq!(
            *runs.borrow(),
            vec![1, 12],
            "second bind succeeded and served"
        );
        assert_eq!(binds.get(), 2);
        assert_eq!(metrics.rdma_listener_failures.load(Ordering::Relaxed), 1);
        assert_eq!(
            metrics.rdma_listener_up.load(Ordering::Relaxed),
            0,
            "down after shutdown"
        );
    }

    /// With nothing else serving (--no-tcp) the failure is returned,
    /// so the process exits non-zero.
    #[tokio::test]
    async fn failed_rdma_listener_is_fatal_without_tcp() {
        let metrics = NfsMetrics::new();
        let (_stop, stopped) = tokio::sync::watch::channel(false);
        let result = supervise_listener(
            1u32,
            || -> std::io::Result<u32> { panic!("no rebind without TCP") },
            |_| async { Err(enodev()) },
            RdmaListenerFailure::Exit,
            FAST,
            &metrics,
            stopped,
        )
        .await;
        assert_eq!(result.unwrap_err().raw_os_error(), Some(libc::ENODEV));
        assert_eq!(metrics.rdma_listener_failures.load(Ordering::Relaxed), 1);
        assert_eq!(metrics.rdma_listener_up.load(Ordering::Relaxed), 0);
    }

    /// A listener that returns without shutdown counts as failed, and
    /// shutdown during the rebind backoff ends the supervisor cleanly.
    #[tokio::test]
    async fn shutdown_during_rebind_ends_supervision() {
        let metrics = NfsMetrics::new();
        let (stop, stopped) = tokio::sync::watch::channel(false);
        let result = supervise_listener(
            1u32,
            || {
                stop.send_replace(true);
                Err(enodev())
            },
            |_| async { Ok(()) },
            RdmaListenerFailure::Rebind,
            FAST,
            &metrics,
            stopped,
        )
        .await;
        assert!(result.is_ok());
        assert_eq!(metrics.rdma_listener_failures.load(Ordering::Relaxed), 1);
    }

    #[test]
    fn rebind_backoff_doubles_to_its_cap() {
        let (first, max) = (
            std::time::Duration::from_secs(1),
            std::time::Duration::from_secs(30),
        );
        assert_eq!(rebind_backoff(1, first, max), first);
        assert_eq!(
            rebind_backoff(3, first, max),
            std::time::Duration::from_secs(4)
        );
        assert_eq!(rebind_backoff(6, first, max), max);
        assert_eq!(rebind_backoff(u32::MAX, first, max), max);
    }

    /// A connection to `server` from loopback address `local` (any
    /// 127.0.0.0/8 address works on Linux), so tests can be several clients.
    async fn connect_from(local: &str, server: std::net::SocketAddr) -> TcpStream {
        let socket = tokio::net::TcpSocket::new_v4().unwrap();
        socket.bind(format!("{local}:0").parse().unwrap()).unwrap();
        socket.connect(server).await.unwrap()
    }

    /// One client address cannot take every slot of the shared TCP
    /// limit; other addresses still connect, and a closed connection
    /// returns its slot.
    #[tokio::test]
    async fn tcp_connections_are_capped_per_client_address() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        let config = NfsServerConfig {
            max_connections: 8, // 2 per client address
            ..NfsServerConfig::default()
        };
        let server = Arc::new(NfsServer::new(test_processor(), config));
        let run = server.clone();
        tokio::spawn(async move { run.run(listener).await });

        async fn served(stream: &mut TcpStream) -> bool {
            write_record(stream, &build_null_call(7)).await;
            let mut header = [0u8; 4];
            matches!(
                tokio::time::timeout(
                    std::time::Duration::from_secs(5),
                    stream.read_exact(&mut header)
                )
                .await,
                Ok(Ok(_))
            )
        }

        let mut first = connect_from("127.0.0.1", addr).await;
        let mut second = connect_from("127.0.0.1", addr).await;
        assert!(served(&mut first).await);
        assert!(served(&mut second).await);
        let mut third = connect_from("127.0.0.1", addr).await;
        assert!(
            !served(&mut third).await,
            "third connection from one address is refused"
        );
        let mut other = connect_from("127.0.0.2", addr).await;
        assert!(served(&mut other).await, "another address is unaffected");
        drop(first);
        let deadline = std::time::Instant::now() + std::time::Duration::from_secs(5);
        loop {
            let mut retry = connect_from("127.0.0.1", addr).await;
            if served(&mut retry).await {
                break;
            }
            assert!(std::time::Instant::now() < deadline, "slot never returned");
            tokio::time::sleep(std::time::Duration::from_millis(20)).await;
        }
    }

    #[tokio::test]
    async fn idle_connection_is_closed_after_timeout() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        let server = NfsServer::new(
            test_processor(),
            NfsServerConfig {
                max_connections: 4,
                idle_timeout: std::time::Duration::from_millis(120),
                write_timeout: std::time::Duration::from_secs(5),
                ..Default::default()
            },
        );
        tokio::spawn(async move { server.run(listener).await.unwrap() });

        let mut stream = TcpStream::connect(addr).await.unwrap();
        // Send nothing. The server's read_record await is wrapped in
        // `tokio::time::timeout(idle_timeout, ..)`; after the timeout fires,
        // the connection task returns Ok(()), which drops the socket.
        // From the client side we observe EOF when we attempt to read.
        let mut buf = [0u8; 1];
        let read =
            tokio::time::timeout(std::time::Duration::from_secs(2), stream.read(&mut buf)).await;
        // Either EOF (0 bytes) or a read error — both confirm the server
        // closed us out. The important thing is we didn't sit forever.
        let n = read
            .expect("server should drop idle socket within 2s")
            .unwrap_or(0);
        assert_eq!(n, 0, "expected EOF from idle-timeout disconnect");
    }

    /// An export with `sec=["sys"]` must reject an AUTH_NONE call
    /// with an RPC auth-error reject, not a COMPOUND-level error.
    #[tokio::test]
    async fn sec_flavor_filter_rejects_disallowed_auth() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        let server = NfsServer::new(
            test_processor(),
            NfsServerConfig {
                max_connections: 16,
                exports: vec![NfsExport {
                    path: "/".into(),
                    allowed_clients: vec![],
                    read_only: false,
                    root_squash: false,
                    sec: vec!["sys".into()], // only AUTH_SYS allowed
                    ..NfsExport::default()
                }],
                ..Default::default()
            },
        );
        tokio::spawn(async move { server.run(listener).await.unwrap() });

        let mut stream = TcpStream::connect(addr).await.unwrap();

        // Send a COMPOUND call with AUTH_NONE credentials — should be
        // rejected because the export's `sec` list only accepts AUTH_SYS.
        // (NULL is exempt from the sec filter per RFC 5531; COMPOUND is not.)
        let mut compound_args = XdrWriter::new();
        compound_args.put_string("sec-test");
        compound_args.put_u32(0); // minorversion
        compound_args.put_u32(0); // 0 ops
        let mut w = XdrWriter::new();
        w.put_u32(777);
        w.put_u32(rpc::MSG_TYPE_CALL);
        w.put_u32(rpc::RPC_VERSION);
        w.put_u32(rpc::NFS_PROGRAM);
        w.put_u32(rpc::NFS_V4);
        w.put_u32(1); // proc = COMPOUND
        w.put_u32(rpc::AUTH_NONE);
        w.put_opaque(&[]);
        w.put_u32(rpc::AUTH_NONE);
        w.put_opaque(&[]);
        w.put_raw(&compound_args.into_bytes());
        let call = w.into_bytes();
        write_record(&mut stream, &call).await;

        let reply = read_record(&mut stream).await;
        let mut r = crate::xdr::XdrReader::new(&reply);
        assert_eq!(r.get_u32().unwrap(), 777); // xid
        assert_eq!(r.get_u32().unwrap(), rpc::MSG_TYPE_REPLY);
        assert_eq!(r.get_u32().unwrap(), rpc::MSG_DENIED);
        assert_eq!(r.get_u32().unwrap(), rpc::REJECT_AUTH_ERROR);
        assert_eq!(r.get_u32().unwrap(), rpc::AUTH_TOOWEAK);
    }

    /// Regression: Linux kernel NFSv4 clients send a NULL probe with
    /// AUTH_NONE before attempting COMPOUND, regardless of the mount's
    /// intended auth flavor. Per RFC 5531 §11.1 NULL is unauthenticated;
    /// rejecting it causes `mount -t nfs4` to fail with "access denied by
    /// server" even when the subsequent COMPOUND would succeed.
    /// Found by running the real kernel client on a test cluster.
    #[tokio::test]
    async fn null_accepts_auth_none_regardless_of_sec_policy() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        // Export with the strictest sec list — AUTH_SYS only.
        let server = NfsServer::new(
            test_processor(),
            NfsServerConfig {
                max_connections: 4,
                exports: vec![NfsExport {
                    path: "/".into(),
                    allowed_clients: vec![],
                    read_only: false,
                    root_squash: false,
                    sec: vec!["sys".into()],
                    ..NfsExport::default()
                }],
                ..Default::default()
            },
        );
        tokio::spawn(async move { server.run(listener).await.unwrap() });

        let mut stream = TcpStream::connect(addr).await.unwrap();

        // NULL RPC with AUTH_NONE — what the Linux kernel does first.
        let mut w = XdrWriter::new();
        w.put_u32(4242);
        w.put_u32(rpc::MSG_TYPE_CALL);
        w.put_u32(rpc::RPC_VERSION);
        w.put_u32(rpc::NFS_PROGRAM);
        w.put_u32(rpc::NFS_V4);
        w.put_u32(0); // proc = NULL
        w.put_u32(rpc::AUTH_NONE);
        w.put_opaque(&[]);
        w.put_u32(rpc::AUTH_NONE);
        w.put_opaque(&[]);
        write_record(&mut stream, &w.into_bytes()).await;

        let reply = read_record(&mut stream).await;
        let mut r = crate::xdr::XdrReader::new(&reply);
        assert_eq!(r.get_u32().unwrap(), 4242); // xid
        assert_eq!(r.get_u32().unwrap(), rpc::MSG_TYPE_REPLY);
        assert_eq!(
            r.get_u32().unwrap(),
            rpc::MSG_ACCEPTED,
            "NULL with AUTH_NONE must not be rejected by sec policy"
        );
        let _verf_flavor = r.get_u32().unwrap();
        let _verf_body = r.get_opaque().unwrap();
        assert_eq!(r.get_u32().unwrap(), rpc::ACCEPT_SUCCESS);
    }

    #[tokio::test]
    async fn compound_putrootfh_getattr() {
        // End-to-end: send PUTROOTFH + GETATTR through the real server.
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        let server = NfsServer::new(test_processor(), NfsServerConfig::default());
        tokio::spawn(async move { server.run(listener).await.unwrap() });

        let mut stream = TcpStream::connect(addr).await.unwrap();

        // Build COMPOUND: PUTROOTFH + GETATTR(type+size)
        let mut ops = XdrWriter::new();
        ops.put_u32(2); // 2 operations
                        // Op 1: PUTROOTFH (no args)
        ops.put_u32(crate::nfs4::OP_PUTROOTFH);
        // Op 2: GETATTR (bitmap requesting type + size)
        ops.put_u32(crate::nfs4::OP_GETATTR);
        ops.put_u32(1); // bitmap length = 1 word
        ops.put_u32((1 << 1) | (1 << 4)); // FATTR4_TYPE (bit 1) + FATTR4_SIZE (bit 4)

        let call = build_compound_call(200, "e2e", &ops.into_bytes());
        write_record(&mut stream, &call).await;

        let reply = read_record(&mut stream).await;
        let mut r = crate::xdr::XdrReader::new(&reply);
        assert_eq!(r.get_u32().unwrap(), 200); // xid
        assert_eq!(r.get_u32().unwrap(), rpc::MSG_TYPE_REPLY);
        assert_eq!(r.get_u32().unwrap(), rpc::MSG_ACCEPTED);
        r.get_u32().unwrap(); // verf flavor
        r.get_opaque().unwrap(); // verf body
        assert_eq!(r.get_u32().unwrap(), rpc::ACCEPT_SUCCESS);

        // COMPOUND reply: status + tag + num_results + results
        assert_eq!(r.get_u32().unwrap(), 0); // NFS4_OK
        let _tag = r.get_opaque().unwrap();
        let num_results = r.get_u32().unwrap();
        assert_eq!(num_results, 2); // PUTROOTFH + GETATTR results

        // Result 1: PUTROOTFH
        let op1 = r.get_u32().unwrap();
        assert_eq!(op1, crate::nfs4::OP_PUTROOTFH);
        let status1 = r.get_u32().unwrap();
        assert_eq!(status1, 0); // NFS4_OK

        // Result 2: GETATTR
        let op2 = r.get_u32().unwrap();
        assert_eq!(op2, crate::nfs4::OP_GETATTR);
        let status2 = r.get_u32().unwrap();
        assert_eq!(status2, 0); // NFS4_OK
                                // GETATTR returns: bitmap + attr values
                                // Root should be a directory (NF4DIR = 2) with size 0
    }

    #[tokio::test]
    async fn compound_create_write_read() {
        // End-to-end: create file, write data, read it back through the server.
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        let server = NfsServer::new(test_processor(), NfsServerConfig::default());
        tokio::spawn(async move { server.run(listener).await.unwrap() });

        let mut stream = TcpStream::connect(addr).await.unwrap();

        // Step 1: PUTROOTFH + CREATE file "testfile"
        let mut ops = XdrWriter::new();
        ops.put_u32(2);
        ops.put_u32(crate::nfs4::OP_PUTROOTFH);
        // CREATE: type=NF4REG(1), name="testfile", attrs=empty
        ops.put_u32(crate::nfs4::OP_CREATE);
        ops.put_u32(1); // objtype = NF4REG
        ops.put_string("testfile");
        // createattrs: empty bitmap
        ops.put_u32(0); // 0 bitmap words

        let call = build_compound_call(301, "create", &ops.into_bytes());
        write_record(&mut stream, &call).await;
        let reply = read_record(&mut stream).await;
        let mut r = crate::xdr::XdrReader::new(&reply);
        // Skip to compound status
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_opaque().unwrap();
        r.get_u32().unwrap();
        assert_eq!(r.get_u32().unwrap(), 0); // NFS4_OK

        // Step 2: PUTROOTFH + LOOKUP "testfile" + WRITE
        let mut ops = XdrWriter::new();
        ops.put_u32(3);
        ops.put_u32(crate::nfs4::OP_PUTROOTFH);
        ops.put_u32(crate::nfs4::OP_LOOKUP);
        ops.put_string("testfile");
        // WRITE: stateid(special 0) + offset + stable + data
        ops.put_u32(crate::nfs4::OP_WRITE);
        ops.put_u32(0); // stateid seqid
        ops.put_raw(&[0u8; 12]); // stateid other (anonymous)
        ops.put_u64(0); // offset
        ops.put_u32(2); // stable_how = FILE_SYNC4
        ops.put_opaque(b"Hello NFS!"); // data

        let call = build_compound_call(302, "write", &ops.into_bytes());
        write_record(&mut stream, &call).await;
        let reply = read_record(&mut stream).await;
        let mut r = crate::xdr::XdrReader::new(&reply);
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_opaque().unwrap();
        r.get_u32().unwrap();
        assert_eq!(r.get_u32().unwrap(), 0); // NFS4_OK

        // Step 3: PUTROOTFH + LOOKUP "testfile" + READ
        let mut ops = XdrWriter::new();
        ops.put_u32(3);
        ops.put_u32(crate::nfs4::OP_PUTROOTFH);
        ops.put_u32(crate::nfs4::OP_LOOKUP);
        ops.put_string("testfile");
        // READ: stateid(special 0) + offset + count
        ops.put_u32(crate::nfs4::OP_READ);
        ops.put_u32(0); // stateid seqid
        ops.put_raw(&[0u8; 12]); // stateid other
        ops.put_u64(0); // offset
        ops.put_u32(1024); // count

        let call = build_compound_call(303, "read", &ops.into_bytes());
        write_record(&mut stream, &call).await;
        let reply = read_record(&mut stream).await;
        let mut r = crate::xdr::XdrReader::new(&reply);
        // Skip RPC header
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        r.get_opaque().unwrap();
        r.get_u32().unwrap();
        assert_eq!(r.get_u32().unwrap(), 0); // NFS4_OK compound status
        let _tag = r.get_opaque().unwrap();
        let num = r.get_u32().unwrap();
        assert_eq!(num, 3);

        // Skip PUTROOTFH result
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        // Skip LOOKUP result
        r.get_u32().unwrap();
        r.get_u32().unwrap();
        // READ result
        let read_op = r.get_u32().unwrap();
        assert_eq!(read_op, crate::nfs4::OP_READ);
        let read_status = r.get_u32().unwrap();
        assert_eq!(read_status, 0); // NFS4_OK
        let eof = r.get_u32().unwrap();
        assert_eq!(eof, 1); // EOF = true
        let data = r.get_opaque().unwrap();
        assert_eq!(&data, b"Hello NFS!");
    }
}
