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

//! RDMA-CM reliable-connected listener for standard IP-addressed services.
//!
//! Unlike nvstor's inter-node QP exchange, this accepts a kernel RPC/RDMA
//! client's RDMA-CM handshake. Each connection has one I/O owner.
//!
//! Data path: the owner posts chains of work requests and never waits for an
//! individual one. A reply is any number of RDMA WRITEs plus one signaled
//! SEND; a Read chunk fetch is a chain of RDMA READs. Every chain carries a
//! [`Hold`] that owns the memory its work requests reference; the connection
//! keeps it until the chain's completion and returns it in the [`Event`].
//! Dropping a connection destroys its QP before any held memory is released,
//! so DMA can never touch freed or re-leased memory.
//!
//! Bulk data lives in registered memory: a per-device [`BufferPool`] of fixed
//! slots registered once, or a [`RegisteredVec`] registered on demand when
//! the pool is exhausted or a transfer is larger than a slot. Only the small
//! RPC/RDMA header is copied (into a registered send slot).

use std::any::Any;
use std::collections::HashMap;
use std::ffi::{c_char, c_void, CStr};
use std::io;
use std::net::{IpAddr, Ipv4Addr, Ipv6Addr, SocketAddr};
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd, RawFd};
use std::ptr::NonNull;
use std::sync::{Arc, Mutex};
use std::time::Duration;

/// One remote access: `len` bytes between local registered memory
/// (`local`, `lkey`) and the peer's memory (`remote`, `rkey`). Layout shared
/// with `struct br_wr` in cm.c.
#[repr(C)]
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Wr {
    pub local: u64,
    pub lkey: u32,
    pub len: u32,
    pub remote: u64,
    pub rkey: u32,
    reserved: u32,
}

impl Wr {
    pub fn new(local: u64, lkey: u32, len: u32, remote: u64, rkey: u32) -> Self {
        Self {
            local,
            lkey,
            len,
            remote,
            rkey,
            reserved: 0,
        }
    }
}

/// Layout shared with `struct br_event` in cm.c.
#[repr(C)]
#[derive(Clone, Copy, Default)]
struct RawEvent {
    id: u64,
    kind: u32,
    len: u32,
    status: i32,
    reserved: u32,
}
const BR_RECV: u32 = 1;
const BR_SEND_DONE: u32 = 2;
const BR_READ_DONE: u32 = 3;
const BR_FAILED: u32 = 4;

extern "C" {
    fn br_cm_listen(addr: *const libc::sockaddr, out: *mut *mut c_void) -> i32;
    fn br_cm_listener_destroy(listener: *mut c_void);
    fn br_cm_next(
        listener: *mut c_void,
        timeout: i32,
        out: *mut *mut c_void,
        peer: *mut libc::sockaddr_storage,
        private: *mut u8,
        private_len: *mut u32,
    ) -> i32;
    fn br_cm_conn_destroy(conn: *mut c_void);
    fn br_cm_accept(
        conn: *mut c_void,
        rx_size: u32,
        rx_count: u32,
        tx_count: u32,
        sq_depth: u32,
        private: *const u8,
        private_len: u8,
        read_depth: *mut u32,
    ) -> i32;
    fn br_cm_device(conn: *mut c_void) -> usize;
    fn br_cm_reg(conn: *mut c_void, addr: *mut c_void, len: usize, lkey: *mut u32) -> *mut c_void;
    fn br_cm_dereg(mr: *mut c_void) -> i32;
    fn br_cm_rx(conn: *mut c_void, index: u32) -> *const u8;
    fn br_cm_repost(conn: *mut c_void, index: u32) -> i32;
    fn br_cm_post_reply(
        conn: *mut c_void,
        writes: *const Wr,
        n: u32,
        slot: u32,
        header: *const u8,
        len: u32,
        token: u64,
    ) -> i32;
    fn br_cm_post_reads(conn: *mut c_void, reads: *const Wr, n: u32, token: u64) -> i32;
    fn br_cm_poll(
        conn: *mut c_void,
        events: *mut RawEvent,
        max: i32,
        timeout_ms: i32,
        spin_us: i32,
        wake_fd: i32,
    ) -> i32;
    fn ibv_wc_status_str(status: i32) -> *const c_char;
}

fn check(result: i32) -> io::Result<i32> {
    if result < 0 {
        Err(io::Error::from_raw_os_error(-result))
    } else {
        Ok(result)
    }
}

fn millis(duration: Duration) -> i32 {
    duration.as_millis().min(i32::MAX as u128) as i32
}

pub struct Listener {
    raw: NonNull<c_void>,
}
// SAFETY: a listener has a single owner and no thread-affine rdma-core state.
unsafe impl Send for Listener {}

impl Listener {
    pub fn bind(addr: SocketAddr) -> io::Result<Self> {
        // SAFETY: zero is valid for sockaddr_storage; writes use the matching
        // family layout, which fits within and is aligned by sockaddr_storage.
        let mut storage: libc::sockaddr_storage = unsafe { std::mem::zeroed() };
        match addr {
            SocketAddr::V4(addr) => {
                let value = libc::sockaddr_in {
                    sin_family: libc::AF_INET as _,
                    sin_port: addr.port().to_be(),
                    sin_addr: libc::in_addr {
                        s_addr: u32::from_ne_bytes(addr.ip().octets()),
                    },
                    sin_zero: [0; 8],
                };
                unsafe {
                    std::ptr::write((&mut storage as *mut libc::sockaddr_storage).cast(), value);
                }
            }
            SocketAddr::V6(addr) => {
                let value = libc::sockaddr_in6 {
                    sin6_family: libc::AF_INET6 as _,
                    sin6_port: addr.port().to_be(),
                    sin6_flowinfo: addr.flowinfo(),
                    sin6_addr: libc::in6_addr {
                        s6_addr: addr.ip().octets(),
                    },
                    sin6_scope_id: addr.scope_id(),
                };
                unsafe {
                    std::ptr::write((&mut storage as *mut libc::sockaddr_storage).cast(), value);
                }
            }
        }
        let mut raw = std::ptr::null_mut();
        // SAFETY: valid address and out pointer live through the synchronous call.
        check(unsafe {
            br_cm_listen((&storage as *const libc::sockaddr_storage).cast(), &mut raw)
        })?;
        let raw = NonNull::new(raw)
            .ok_or_else(|| io::Error::other("RDMA-CM returned a null listener"))?;
        Ok(Self { raw })
    }

    pub fn next(&mut self, timeout: Duration) -> io::Result<Option<PendingConnection>> {
        let mut raw = std::ptr::null_mut();
        let mut peer: libc::sockaddr_storage = unsafe { std::mem::zeroed() };
        let mut private = [0u8; 256];
        let mut private_len = 0;
        // SAFETY: the bridge copies at most a sockaddr_storage and 255 private
        // bytes. On success this function takes sole ownership of the new id.
        let n = check(unsafe {
            br_cm_next(
                self.raw.as_ptr(),
                millis(timeout),
                &mut raw,
                &mut peer,
                private.as_mut_ptr(),
                &mut private_len,
            )
        })?;
        if n == 0 {
            return Ok(None);
        }
        let raw = NonNull::new(raw)
            .ok_or_else(|| io::Error::other("RDMA-CM returned a null connection"))?;
        let connection = Connection::pending(raw);
        let addr = unsafe {
            match peer.ss_family as i32 {
                libc::AF_INET => {
                    let a = &*(&peer as *const libc::sockaddr_storage).cast::<libc::sockaddr_in>();
                    SocketAddr::new(
                        IpAddr::V4(Ipv4Addr::from(a.sin_addr.s_addr.to_ne_bytes())),
                        u16::from_be(a.sin_port),
                    )
                }
                libc::AF_INET6 => {
                    let a = &*(&peer as *const libc::sockaddr_storage).cast::<libc::sockaddr_in6>();
                    SocketAddr::V6(std::net::SocketAddrV6::new(
                        Ipv6Addr::from(a.sin6_addr.s6_addr),
                        u16::from_be(a.sin6_port),
                        a.sin6_flowinfo,
                        a.sin6_scope_id,
                    ))
                }
                _ => return Err(io::Error::other("RDMA peer is not an IP address")),
            }
        };
        Ok(Some(PendingConnection {
            connection,
            addr,
            private: private[..private_len as usize].to_vec(),
        }))
    }
}

impl Drop for Listener {
    fn drop(&mut self) {
        unsafe {
            br_cm_listener_destroy(self.raw.as_ptr());
        }
    }
}

/// Dropping a pending connection rejects it without allocating a QP or buffers.
pub struct PendingConnection {
    connection: Connection,
    pub addr: SocketAddr,
    pub private: Vec<u8>,
}

/// Resources of an accepted connection.
#[derive(Clone, Copy, Debug)]
pub struct Limits {
    /// Size of each receive buffer and send slot (largest inline message).
    pub message_size: usize,
    /// Receive buffers posted; the peer may have this many messages in flight.
    pub receives: u32,
    /// Send slots: replies that may await their SEND completion at once.
    pub sends: u32,
    /// Send-queue entries shared by every posted chain.
    pub send_queue: u32,
}

impl PendingConnection {
    pub fn accept(mut self, limits: Limits, private: &[u8]) -> io::Result<Connection> {
        if limits.message_size == 0
            || limits.message_size > 262144
            || limits.receives == 0
            || limits.receives > 4096
            || limits.sends == 0
            || limits.sends > 4096
            || limits.send_queue < 2
            || limits.send_queue > 65536
            || private.len() > 255
        {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "invalid RDMA connection limits",
            ));
        }
        let mut read_depth = 0;
        // SAFETY: the bridge copies private data and allocates its own receive
        // and send-slot buffers; failure drops this pending connection, which
        // releases everything allocated so far.
        check(unsafe {
            br_cm_accept(
                self.connection.raw.as_ptr(),
                limits.message_size as u32,
                limits.receives,
                limits.sends,
                limits.send_queue,
                private.as_ptr(),
                private.len() as u8,
                &mut read_depth,
            )
        })?;
        let c = &mut self.connection;
        c.message_size = limits.message_size;
        c.send_queue = limits.send_queue as usize;
        c.free_slots = (0..limits.sends).rev().collect();
        c.read_depth = read_depth;
        Ok(self.connection)
    }
}

/// Memory a posted chain references, owned by the connection until the
/// chain completes.
pub type Hold = Box<dyn Any + Send>;

pub enum Event {
    /// A message of `len` bytes arrived in receive buffer `index`. Read it with
    /// [`Connection::received`], then return the buffer with
    /// [`Connection::repost`].
    Receive { index: u32, len: usize },
    /// A reply chain completed; its memory is returned.
    Sent(Hold),
    /// A READ chain completed; the destination memory is returned, filled.
    Read(Hold),
}

struct Chain {
    wrs: usize,
    slot: Option<u32>,
    hold: Hold,
}

pub struct Connection {
    raw: NonNull<c_void>,
    message_size: usize,
    send_queue: usize,
    queued: usize,
    free_slots: Vec<u32>,
    read_depth: u32,
    next_token: u64,
    chains: HashMap<u64, Chain>,
    raw_events: Vec<RawEvent>,
}
// SAFETY: Connection is Send but not Sync. All operations that touch the QP
// require &mut self; held memory is Send.
unsafe impl Send for Connection {}

impl Connection {
    fn pending(raw: NonNull<c_void>) -> Self {
        Self {
            raw,
            message_size: 0,
            send_queue: 0,
            queued: 0,
            free_slots: Vec::new(),
            read_depth: 0,
            next_token: 1,
            chains: HashMap::new(),
            raw_events: vec![RawEvent::default(); 64],
        }
    }

    /// RDMA READs the peer allows outstanding (0: none; Read chunks unusable).
    pub fn read_depth(&self) -> u32 {
        self.read_depth
    }

    pub fn message_size(&self) -> usize {
        self.message_size
    }

    /// Whether a reply with `writes` RDMA WRITEs can be posted now.
    pub fn can_post_reply(&self, writes: usize) -> bool {
        !self.free_slots.is_empty() && self.queued + writes + 1 <= self.send_queue
    }

    /// Whether a fetch of `reads` RDMA READs can be posted now.
    pub fn can_post_reads(&self, reads: usize) -> bool {
        reads > 0 && self.queued + reads <= self.send_queue
    }

    /// Chains posted and not yet completed.
    pub fn outstanding(&self) -> usize {
        self.chains.len()
    }

    fn token(&mut self) -> u64 {
        let token = self.next_token;
        self.next_token = (self.next_token + 1) & !(1 << 63);
        token
    }

    /// Post `writes` then a signaled SEND of `header`.
    ///
    /// # Safety
    /// Every `Wr::local` range must be registered on this connection's device
    /// with the given lkey and stay valid until the chain completes: memory
    /// owned by `hold`, or a [`BufferPool`]/[`RegisteredVec`] owned by `hold`.
    pub unsafe fn post_reply(
        &mut self,
        header: &[u8],
        writes: &[Wr],
        hold: Hold,
    ) -> io::Result<()> {
        if header.len() > self.message_size || !self.can_post_reply(writes.len()) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "no send capacity for reply",
            ));
        }
        let slot = self
            .free_slots
            .pop()
            .expect("invariant: can_post_reply checked a free slot");
        let token = self.token();
        // SAFETY: header is copied into the registered send slot before
        // return; write ranges are valid per the caller's contract.
        let rc = check(br_cm_post_reply(
            self.raw.as_ptr(),
            writes.as_ptr(),
            writes.len() as u32,
            slot,
            header.as_ptr(),
            header.len() as u32,
            token,
        ));
        if let Err(error) = rc {
            self.free_slots.push(slot);
            return Err(error);
        }
        self.queued += writes.len() + 1;
        self.chains.insert(
            token,
            Chain {
                wrs: writes.len() + 1,
                slot: Some(slot),
                hold,
            },
        );
        Ok(())
    }

    /// Post a chain of RDMA READs; completion returns `hold` in [`Event::Read`].
    ///
    /// # Safety
    /// Every `Wr::local` range must be registered on this connection's device,
    /// owned by `hold`, and not otherwise accessed until the chain completes.
    pub unsafe fn post_reads(&mut self, reads: &[Wr], hold: Hold) -> io::Result<()> {
        if self.read_depth == 0 || !self.can_post_reads(reads.len()) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "no send capacity for RDMA READ",
            ));
        }
        let token = self.token();
        check(br_cm_post_reads(
            self.raw.as_ptr(),
            reads.as_ptr(),
            reads.len() as u32,
            token,
        ))?;
        self.queued += reads.len();
        self.chains.insert(
            token,
            Chain {
                wrs: reads.len(),
                slot: None,
                hold,
            },
        );
        Ok(())
    }

    /// Wait up to `timeout` for events, busy-polling for `spin` first. Returns
    /// early (possibly with no events) when `wake` is notified. A failed work
    /// request or a disconnect is an error; the connection is then unusable.
    pub fn poll(
        &mut self,
        events: &mut Vec<Event>,
        timeout: Duration,
        spin: Duration,
        wake: Option<&Wake>,
    ) -> io::Result<()> {
        // SAFETY: raw_events is a valid array of RawEvent of the given length.
        let n = check(unsafe {
            br_cm_poll(
                self.raw.as_ptr(),
                self.raw_events.as_mut_ptr(),
                self.raw_events.len() as i32,
                millis(timeout),
                spin.as_micros().min(i32::MAX as u128) as i32,
                wake.map_or(-1, |w| w.0.as_raw_fd()),
            )
        })? as usize;
        for i in 0..n {
            let raw = self.raw_events[i];
            match raw.kind {
                BR_RECV => events.push(Event::Receive {
                    index: raw.id as u32,
                    len: raw.len as usize,
                }),
                BR_SEND_DONE | BR_READ_DONE => {
                    let chain = self.chains.remove(&raw.id).ok_or_else(|| {
                        io::Error::new(
                            io::ErrorKind::InvalidData,
                            "completion for unknown RDMA chain",
                        )
                    })?;
                    self.queued -= chain.wrs;
                    if let Some(slot) = chain.slot {
                        self.free_slots.push(slot);
                    }
                    events.push(if raw.kind == BR_SEND_DONE {
                        Event::Sent(chain.hold)
                    } else {
                        Event::Read(chain.hold)
                    });
                }
                BR_FAILED => {
                    // SAFETY: ibv_wc_status_str returns a static string.
                    let status = unsafe { CStr::from_ptr(ibv_wc_status_str(raw.status)) };
                    return Err(io::Error::new(
                        io::ErrorKind::ConnectionAborted,
                        format!(
                            "RDMA work request failed: {} (vendor error {:#x})",
                            status.to_string_lossy(),
                            raw.len
                        ),
                    ));
                }
                _ => {
                    return Err(io::Error::new(
                        io::ErrorKind::InvalidData,
                        "unknown RDMA event",
                    ))
                }
            }
        }
        Ok(())
    }

    /// The message in receive buffer `index` (from [`Event::Receive`]).
    pub fn received(&self, index: u32, len: usize) -> io::Result<&[u8]> {
        // SAFETY: br_cm_rx bounds-checks the index; the buffer is message_size
        // bytes and is not reposted (so not DMA-written) until repost().
        let ptr = unsafe { br_cm_rx(self.raw.as_ptr(), index) };
        if ptr.is_null() || len > self.message_size {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "invalid receive buffer",
            ));
        }
        Ok(unsafe { std::slice::from_raw_parts(ptr, len) })
    }

    /// Return receive buffer `index` to the peer.
    pub fn repost(&mut self, index: u32) -> io::Result<()> {
        check(unsafe { br_cm_repost(self.raw.as_ptr(), index) }).map(|_| ())
    }

    /// Register `data` on this connection's device. Used when the pool cannot
    /// serve a transfer; costs a registration but no copy.
    pub fn register(&self, mut data: Vec<u8>) -> io::Result<RegisteredVec> {
        if data.is_empty() {
            data.reserve(1);
        }
        let mut lkey = 0;
        // SAFETY: the Vec's heap buffer stays at the same address while owned
        // by RegisteredVec, which deregisters before freeing it.
        let mr = unsafe {
            br_cm_reg(
                self.raw.as_ptr(),
                data.as_mut_ptr().cast(),
                data.capacity().max(1),
                &mut lkey,
            )
        };
        let mr = NonNull::new(mr).ok_or_else(io::Error::last_os_error)?;
        Ok(RegisteredVec { mr, lkey, data })
    }

    /// The shared buffer pool of this connection's device, created on first
    /// use with `slots` slots of `slot_size` bytes. Later calls return the
    /// existing pool whatever geometry they ask for.
    pub fn pool(&self, slot_size: usize, slots: usize) -> io::Result<Arc<BufferPool>> {
        static POOLS: Mutex<Vec<(usize, Arc<BufferPool>)>> = Mutex::new(Vec::new());
        let device = unsafe { br_cm_device(self.raw.as_ptr()) };
        let mut pools = POOLS
            .lock()
            .map_err(|_| io::Error::other("RDMA pool registry poisoned"))?;
        if let Some((_, pool)) = pools.iter().find(|(d, _)| *d == device) {
            return Ok(pool.clone());
        }
        let pool = Arc::new(BufferPool::new(self, slot_size, slots)?);
        pools.push((device, pool.clone()));
        Ok(pool)
    }
}

impl Drop for Connection {
    fn drop(&mut self) {
        // Destroys the QP (quiescing DMA) before `chains` releases held memory.
        unsafe {
            br_cm_conn_destroy(self.raw.as_ptr());
        }
    }
}

/// A Vec registered on a device. Deregistered before the memory is freed.
pub struct RegisteredVec {
    mr: NonNull<c_void>,
    lkey: u32,
    data: Vec<u8>,
}
// SAFETY: the MR handle is only used to deregister, once, on drop.
unsafe impl Send for RegisteredVec {}

impl RegisteredVec {
    pub fn addr(&self) -> u64 {
        self.data.as_ptr() as u64
    }
    pub fn lkey(&self) -> u32 {
        self.lkey
    }
    pub fn as_slice(&self) -> &[u8] {
        &self.data
    }
    pub fn as_mut_slice(&mut self) -> &mut [u8] {
        &mut self.data
    }
}

impl Drop for RegisteredVec {
    fn drop(&mut self) {
        unsafe {
            br_cm_dereg(self.mr.as_ptr());
        }
    }
}

/// Fixed-size slots of one registered region, shared by every connection on
/// a device. Leasing never blocks: an exhausted pool returns None and the
/// caller registers its own memory instead.
pub struct BufferPool {
    base: NonNull<u8>,
    bytes: usize,
    slot_size: usize,
    lkey: u32,
    mr: NonNull<c_void>,
    free: Mutex<Vec<u32>>,
}
// SAFETY: slots are handed out exclusively (one PoolBuf per index); the base
// mapping and MR live until the pool is dropped.
unsafe impl Send for BufferPool {}
unsafe impl Sync for BufferPool {}

impl BufferPool {
    fn new(conn: &Connection, slot_size: usize, slots: usize) -> io::Result<Self> {
        let page = 1 << 16;
        if slot_size == 0 || slot_size % 4096 != 0 || slots == 0 || slots > u32::MAX as usize {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "invalid RDMA pool geometry",
            ));
        }
        let bytes = slot_size
            .checked_mul(slots)
            .and_then(|n| n.checked_add(page - 1))
            .map(|n| n & !(page - 1))
            .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidInput, "RDMA pool too large"))?;
        // SAFETY: anonymous private mapping; checked for MAP_FAILED.
        let base = unsafe {
            libc::mmap(
                std::ptr::null_mut(),
                bytes,
                libc::PROT_READ | libc::PROT_WRITE,
                libc::MAP_PRIVATE | libc::MAP_ANONYMOUS,
                -1,
                0,
            )
        };
        if base == libc::MAP_FAILED {
            return Err(io::Error::last_os_error());
        }
        // Huge pages shrink the NIC's translation table; failure is harmless.
        unsafe {
            libc::madvise(base, bytes, libc::MADV_HUGEPAGE);
        }
        let mut lkey = 0;
        let mr = unsafe { br_cm_reg(conn.raw.as_ptr(), base, bytes, &mut lkey) };
        let Some(mr) = NonNull::new(mr) else {
            let error = io::Error::last_os_error();
            unsafe {
                libc::munmap(base, bytes);
            }
            return Err(error);
        };
        Ok(Self {
            base: NonNull::new(base.cast()).expect("invariant: mmap succeeded"),
            bytes,
            slot_size,
            lkey,
            mr,
            free: Mutex::new((0..slots as u32).rev().collect()),
        })
    }

    pub fn slot_size(&self) -> usize {
        self.slot_size
    }

    /// Slots currently free.
    pub fn available(&self) -> usize {
        self.free.lock().map_or(0, |free| free.len())
    }

    pub fn lease(self: &Arc<Self>) -> Option<PoolBuf> {
        let index = self.free.lock().ok()?.pop()?;
        Some(PoolBuf {
            pool: self.clone(),
            index,
        })
    }
}

impl Drop for BufferPool {
    fn drop(&mut self) {
        unsafe {
            br_cm_dereg(self.mr.as_ptr());
            libc::munmap(self.base.as_ptr().cast(), self.bytes);
        }
    }
}

/// One pool slot, exclusively owned; returned to the pool on drop.
pub struct PoolBuf {
    pool: Arc<BufferPool>,
    index: u32,
}

impl PoolBuf {
    pub fn capacity(&self) -> usize {
        self.pool.slot_size
    }
    pub fn addr(&self) -> u64 {
        self.as_slice().as_ptr() as u64
    }
    pub fn lkey(&self) -> u32 {
        self.pool.lkey
    }
    pub fn as_slice(&self) -> &[u8] {
        // SAFETY: the slot lies inside the mapping and this PoolBuf is its only owner.
        unsafe {
            std::slice::from_raw_parts(
                self.pool
                    .base
                    .as_ptr()
                    .add(self.index as usize * self.pool.slot_size),
                self.pool.slot_size,
            )
        }
    }
    pub fn as_mut_slice(&mut self) -> &mut [u8] {
        // SAFETY: as above; &mut self guarantees exclusive access.
        unsafe {
            std::slice::from_raw_parts_mut(
                self.pool
                    .base
                    .as_ptr()
                    .add(self.index as usize * self.pool.slot_size),
                self.pool.slot_size,
            )
        }
    }
}

impl Drop for PoolBuf {
    fn drop(&mut self) {
        if let Ok(mut free) = self.pool.free.lock() {
            free.push(self.index);
        }
    }
}

/// Worker completions wake the RDMA I/O loop without busy polling.
pub struct Wake(OwnedFd);
impl Wake {
    pub fn new() -> io::Result<Self> {
        let fd = unsafe { libc::eventfd(0, libc::EFD_CLOEXEC | libc::EFD_NONBLOCK) };
        if fd < 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(Self(unsafe { OwnedFd::from_raw_fd(fd as RawFd) }))
    }
    pub fn notify(&self) {
        let one = 1u64;
        // A saturated event counter already has a pending wakeup.
        unsafe {
            libc::write(self.0.as_raw_fd(), (&one as *const u64).cast(), 8);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn worker_wakeup_is_pollable_and_coalesces() {
        let wake = Wake::new().unwrap();
        wake.notify();
        wake.notify();
        let mut p = libc::pollfd {
            fd: wake.0.as_raw_fd(),
            events: libc::POLLIN,
            revents: 0,
        };
        assert_eq!(unsafe { libc::poll(&mut p, 1, 0) }, 1);
        let mut count = 0u64;
        assert_eq!(
            unsafe { libc::read(p.fd, (&mut count as *mut u64).cast(), 8) },
            8
        );
        assert_eq!(count, 2);
        assert_eq!(unsafe { libc::poll(&mut p, 1, 0) }, 0);
    }
    #[test]
    fn shared_layouts_match_the_c_bridge() {
        assert_eq!(std::mem::size_of::<Wr>(), 32);
        assert_eq!(std::mem::size_of::<RawEvent>(), 24);
    }
}
