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

//! The C ABI of the KV client (`libnvstor_kv`), for engine adapters
//! written in other languages (the vLLM spec in
//! `integrations/vllm/nvstor_kv`).
//!
//! One [`BkvClient`] holds connections to every rail of one server, all
//! with the same landing layout registered: a pool of `slots` blocks in
//! GPU memory the caller owns, laid out per tensor as vLLM lays out its KV
//! cache (tensor t's `slots` pages back to back, tensors one after
//! another). A batch is split across the connections and run on all of
//! them at once.
//!
//! **Landing slots after a failure**. A connection whose
//! call fails in transport or protocol (or panics) is dropped before the
//! call returns, which moves its QPs to the error state: no WRITE of that
//! call can land afterwards, so every slot of the batch may be reused as
//! soon as the call returns, whatever happened. Its blocks read
//! `STATUS_ERROR`. A refused request leaves its connection in service and
//! its blocks read the refusal's status. A dropped connection is
//! re-dialled in the background (a new QP: nothing of the old one can
//! land); while every connection is down, calls fail. A batch may not
//! name a slot twice.
//!
//! **GPU visibility.** On a GPU that does not order NIC writes itself, a
//! GET flushes them (`cuFlushGPUDirectRDMAWrites`) before returning, so
//! work the caller then issues reads the landed bytes.
//!
//! Credentials come from the environment (`NVSTOR_KV_SECRET`, and
//! `NVSTOR_KV_ACCESS_KEY`, default "kv"), never from arguments.
//!
//! Every function returns 0 on success and -1 on failure, with a message
//! in the caller's `err` buffer (truncated, NUL-terminated).

use std::ffi::{c_char, CStr};
use std::sync::Arc;
use std::thread::JoinHandle;
use std::time::{Duration, Instant};

use nvstor_peer::gpu::{GpuBuffer, Mapping};
use nvstor_rdma::auth::Credentials;
use nvstor_rdma::device::Access;
use nvstor_rdma::kv_proto::{
    GetEntry, Key, Layout, PutEntry, TensorRun, MAX_BATCH, MAX_BUDGET_MS, MAX_KEY_LEN,
    MAX_REQUEST_BYTES, MIN_KEY_LEN, NAMESPACE_LEN, STATUS_ERROR,
};
use nvstor_rdma::rail::{open_devices, DeviceSelect, Rails};
use nvstor_rdma::region::RailRegion;
use parking_lot::{Condvar, Mutex};
use tracing::{info, warn};

use crate::client::{connect_rail, kv_rails, KvClient, KvError, RailExchange};

const SECRET_ENV: &str = "NVSTOR_KV_SECRET";
const KEY_ENV: &str = "NVSTOR_KV_ACCESS_KEY";
const LAYOUT_ID: u32 = 1;

/// One connection's share of a batch: its range of entries and results
/// (`None`: the connection failed and its entries read the fail value).
type ChunkResult<T> = (std::ops::Range<usize>, Option<Vec<T>>);

/// How long a failed connection waits before it is dialled again, at
/// first and at most (doubling in between).
const REDIAL_FIRST: Duration = Duration::from_secs(1);
const REDIAL_MAX: Duration = Duration::from_secs(30);

/// The redialer's stop flag and wake-up.
type Stop = (Mutex<bool>, Condvar);

/// One server, every rail, one landing pool.
pub struct BkvClient {
    inner: Arc<Inner>,
    /// Re-dials failed connections; stopped and joined before `inner`
    /// can be dropped.
    redialer: Option<(Arc<Stop>, JoinHandle<()>)>,
}

/// What the calls and the redialer share. Fields drop in order: the
/// connections (destroying their QPs) before the region, the region
/// before the GPU buffer it registers.
struct Inner {
    /// `None` while the connection is down (failed, not yet re-dialled).
    conns: Vec<Mutex<Option<KvClient<RailExchange>>>>,
    slots: u32,
    /// Most entries one GET or PUT request carries: the server refuses a
    /// request that moves more than MAX_REQUEST_BYTES.
    per_request: usize,
    region: Option<RailRegion>,
    /// The landing pool's GPU buffer: for flushing NIC writes after a GET.
    gpu: Option<Arc<GpuBuffer>>,
    dial: Dial,
}

/// Everything needed to (re)open connection `c`.
struct Dial {
    host: String,
    port: u16,
    rails: u16,
    creds: Credentials,
    nics: Arc<Rails>,
    /// The landing pool's address and pages (`None` for a lookup client).
    landing: Option<(u64, Vec<u32>)>,
}

fn credentials() -> Result<Credentials, String> {
    let secret = std::env::var(SECRET_ENV).map_err(|_| format!("{SECRET_ENV} is not set"))?;
    if secret.len() < 16 {
        return Err(format!("{SECRET_ENV} must be at least 16 characters"));
    }
    Ok(Credentials::new(
        std::env::var(KEY_ENV).unwrap_or_else(|_| "kv".into()),
        secret,
    ))
}

/// The landing layout over `[iova, iova + len)`: per tensor, `slots`
/// pages back to back.
fn landing_layout(iova: u64, rkey: u32, pages: &[u32], slots: u32) -> Layout {
    let mut at = iova;
    let tensors = pages
        .iter()
        .map(|&p| {
            let t = TensorRun {
                base: at,
                stride: u64::from(p),
                page: p,
            };
            at += u64::from(p) * u64::from(slots);
            t
        })
        .collect();
    Layout {
        layout_id: LAYOUT_ID,
        rkey,
        num_blocks: slots,
        tensors,
    }
}

/// Most entries per GET or PUT request for blocks of `pages`: MAX_BATCH,
/// or fewer so that one request moves at most MAX_REQUEST_BYTES.
fn per_request(pages: &[u32]) -> Result<usize, String> {
    let block = landing_bytes(pages, 1);
    if block == 0 || block > MAX_REQUEST_BYTES {
        return Err(format!(
            "a block of {block} bytes: blocks are 1..={MAX_REQUEST_BYTES} bytes"
        ));
    }
    Ok(MAX_BATCH.min((MAX_REQUEST_BYTES / block) as usize))
}

/// The host page size.
fn host_page() -> u64 {
    // SAFETY: sysconf has no preconditions.
    u64::try_from(unsafe { libc::sysconf(libc::_SC_PAGESIZE) })
        .unwrap_or(4096)
        .max(1)
}

/// The bytes the landing pool registers: its size rounded up to whole host
/// pages. The caller's allocation must cover them.
pub fn landing_region_bytes(pages: &[u32], slots: u32) -> u64 {
    landing_bytes(pages, slots).div_ceil(host_page()) * host_page()
}

/// The landing pool's size for `pages` and `slots`.
pub fn landing_bytes(pages: &[u32], slots: u32) -> u64 {
    pages.iter().map(|&p| u64::from(p)).sum::<u64>() * u64::from(slots)
}

impl Inner {
    /// Open connection `c` (on rail c % rails), its landing layout
    /// registered when there is a pool.
    fn dial(&self, c: usize) -> Result<KvClient<RailExchange>, String> {
        let d = &self.dial;
        let rail = c % usize::from(d.rails);
        let addr = format!("{}:{}", d.host, u32::from(d.port) + rail as u32);
        let link =
            connect_rail(&d.nics, &addr, rail, &[]).map_err(|e| format!("connect {addr}: {e}"))?;
        let rkey = match &self.region {
            Some(region) => Some(
                region
                    .rkey(link.rail())
                    .ok_or("no rkey for the connection's NIC")?,
            ),
            None => None,
        };
        let mut client = KvClient::new(link, d.creds.clone());
        if let (Some((ptr, pages)), Some(rkey)) = (&d.landing, rkey) {
            client
                .register_layout(landing_layout(*ptr, rkey, pages, self.slots))
                .map_err(|e| format!("layout on {addr}: {e}"))?;
        }
        Ok(client)
    }

    fn live(&self) -> usize {
        self.conns.iter().filter(|c| c.lock().is_some()).count()
    }

    /// Re-dial every connection that is down and due, until `stop`. A
    /// connection is dialled without holding its slot, so calls go on on
    /// the others meanwhile. One dial is bounded by the bootstrap's
    /// connect and handshake timeouts on each local NIC it tries, which
    /// also bounds how long closing the client can wait for it.
    fn redial(&self, stop: &Stop) {
        let mut backoff = vec![REDIAL_FIRST; self.conns.len()];
        let mut due = vec![Instant::now(); self.conns.len()];
        loop {
            let now = Instant::now();
            for c in 0..self.conns.len() {
                // A connection busy with a request is up: never wait for it.
                if match self.conns[c].try_lock() {
                    None => true,
                    Some(g) => g.is_some(),
                } {
                    backoff[c] = REDIAL_FIRST;
                    continue;
                }
                if due[c] > now {
                    continue;
                }
                if *stop.0.lock() {
                    return;
                }
                match self.dial(c) {
                    Ok(client) => {
                        info!(connection = c, host = %self.dial.host, "nvstor-kv: connection re-dialled");
                        *self.conns[c].lock() = Some(client);
                        backoff[c] = REDIAL_FIRST;
                    }
                    Err(e) => {
                        warn!(
                            connection = c,
                            retry_s = backoff[c].as_secs(),
                            "nvstor-kv: re-dial failed: {e}"
                        );
                        due[c] = Instant::now() + backoff[c];
                        backoff[c] = (backoff[c] * 2).min(REDIAL_MAX);
                    }
                }
            }
            let mut stopped = stop.0.lock();
            if *stopped {
                return;
            }
            // A failed call wakes the redialer; otherwise look again soon.
            let _ = stop.1.wait_for(&mut stopped, REDIAL_FIRST);
            if *stopped {
                return;
            }
        }
    }
}

impl BkvClient {
    /// Connect `connections` times to `host` (rails on `port`, `port` + 1,
    /// ...; connection c on rail c % `rails`) from the NICs `devices`
    /// selects, and register the landing pool at `ptr` on GPU `gpu`. A
    /// connection that fails later is re-dialled in the background (1 s
    /// at first, backing off to 30 s), so a server restart costs the
    /// calls made while it is down, not the client.
    ///
    /// The pool is registered rounded up to whole host pages (a dma-buf
    /// range must be): `ptr` must lie on a host page.
    ///
    /// # Safety
    /// `ptr..ptr + landing_region_bytes(pages, slots)` must be device
    /// memory of GPU `gpu` that stays allocated until the client is closed.
    #[allow(clippy::too_many_arguments)]
    pub unsafe fn open(
        host: &str,
        port: u16,
        rails: u16,
        devices: &str,
        connections: usize,
        gpu: u32,
        ptr: u64,
        pages: &[u32],
        slots: u32,
    ) -> Result<BkvClient, String> {
        if rails == 0 || connections == 0 || slots == 0 || pages.is_empty() {
            return Err("rails, connections, slots and pages must be positive".into());
        }
        let creds = credentials()?;
        let select: DeviceSelect = devices.parse()?;
        let nics = kv_rails(open_devices(&select)?, &creds)?;
        let len = usize::try_from(landing_region_bytes(pages, slots))
            .map_err(|_| "landing pool too large")?;
        // SAFETY: the caller vouches for the range (this function's contract).
        let gpu_buf = Arc::new(unsafe { GpuBuffer::borrowed(gpu, ptr, len, Mapping::Default) }?);
        let region = RailRegion::dmabuf_with(
            &nics,
            gpu_buf.dmabuf()?,
            0,
            len,
            ptr,
            Arc::clone(&gpu_buf) as Arc<dyn Send + Sync>,
            Access::REMOTE_READ_WRITE,
        )?;
        let dial = Dial {
            host: host.into(),
            port,
            rails,
            creds,
            nics,
            landing: Some((ptr, pages.to_vec())),
        };
        let inner = Inner {
            conns: Vec::new(),
            slots,
            per_request: per_request(pages)?,
            region: Some(region),
            gpu: Some(gpu_buf),
            dial,
        };
        Self::connect(inner, connections)
    }

    /// Connections for HEAD only (no landing pool): what an engine's
    /// scheduler needs to answer lookups. Re-dialled as for [`Self::open`].
    pub fn open_lookup(
        host: &str,
        port: u16,
        rails: u16,
        devices: &str,
        connections: usize,
    ) -> Result<BkvClient, String> {
        if rails == 0 || connections == 0 {
            return Err("rails and connections must be positive".into());
        }
        let creds = credentials()?;
        let select: DeviceSelect = devices.parse()?;
        let nics = kv_rails(open_devices(&select)?, &creds)?;
        let dial = Dial {
            host: host.into(),
            port,
            rails,
            creds,
            nics,
            landing: None,
        };
        Self::connect(
            Inner {
                conns: Vec::new(),
                slots: 0,
                per_request: MAX_BATCH,
                region: None,
                gpu: None,
                dial,
            },
            connections,
        )
    }

    /// Open every connection (all must succeed), then start the redialer.
    fn connect(mut inner: Inner, connections: usize) -> Result<BkvClient, String> {
        for c in 0..connections {
            let client = inner.dial(c)?;
            inner.conns.push(Mutex::new(Some(client)));
        }
        let inner = Arc::new(inner);
        let stop: Arc<Stop> = Arc::new((Mutex::new(false), Condvar::new()));
        let (i2, s2) = (Arc::clone(&inner), Arc::clone(&stop));
        let handle = std::thread::Builder::new()
            .name("nvstor-kv-redial".into())
            .spawn(move || i2.redial(&s2))
            .map_err(|e| format!("redial thread: {e}"))?;
        Ok(BkvClient {
            inner,
            redialer: Some((stop, handle)),
        })
    }

    fn live(&self) -> usize {
        self.inner.live()
    }

    /// Run `op` on contiguous chunks of `n` entries, one chunk per live
    /// connection, all at once, each chunk in requests of at most
    /// `per_request` entries; `op(client, range)` returns that range's results.
    /// A transport or protocol failure, or a panic, drops the connection
    /// (destroying its QPs; the redialer opens it again) and its whole
    /// chunk reads `fail()`; a refusal keeps the connection and its
    /// request's entries read `refused(status)`.
    fn fan_out<T: Send + Clone>(
        &self,
        n: usize,
        out: &mut [T],
        fail: impl Fn() -> T + Sync,
        refused: impl Fn(u8) -> T + Sync,
        op: impl Fn(&mut KvClient<RailExchange>, std::ops::Range<usize>) -> Result<Vec<T>, KvError>
            + Sync,
    ) -> Result<(), String> {
        let (conns, per_request) = (&self.inner.conns, self.inner.per_request);
        let live: Vec<usize> = (0..conns.len())
            .filter(|&i| conns[i].lock().is_some())
            .collect();
        if live.is_empty() {
            return Err("no connection is up (re-dialling in the background)".into());
        }
        let per = n.div_ceil(live.len());
        let ranges: Vec<(usize, std::ops::Range<usize>)> = live
            .iter()
            .enumerate()
            .map(|(k, &i)| (i, (k * per).min(n)..((k + 1) * per).min(n)))
            .filter(|(_, r)| !r.is_empty())
            .collect();
        let results: Vec<ChunkResult<T>> = std::thread::scope(|s| {
            let handles: Vec<_> = ranges
                .into_iter()
                .map(|(i, r)| {
                    let (op, conn, refused) = (&op, &conns[i], &refused);
                    s.spawn(move || {
                        let mut guard = conn.lock();
                        let run = std::panic::catch_unwind(std::panic::AssertUnwindSafe(
                            || -> Option<Vec<T>> {
                                let client = guard.as_mut()?;
                                let mut res = Vec::with_capacity(r.len());
                                let mut at = r.start;
                                while at < r.end {
                                    let sub = at..(at + per_request).min(r.end);
                                    match op(client, sub.clone()) {
                                        Ok(v) if v.len() == sub.len() => res.extend(v),
                                        Ok(_) => return None,
                                        Err(KvError::Refused(st)) => {
                                            res.extend(sub.clone().map(|_| refused(st)))
                                        }
                                        Err(KvError::Invalid(_)) => {
                                            res.extend(sub.clone().map(|_| refused(STATUS_ERROR)))
                                        }
                                        Err(
                                            KvError::Transport(_)
                                            | KvError::Protocol(_)
                                            | KvError::Broken(_),
                                        ) => return None,
                                    }
                                    at = sub.end;
                                }
                                Some(res)
                            },
                        ));
                        let res = run.unwrap_or(None);
                        if res.is_none() {
                            // Dropping the client disconnects the peer and moves
                            // its QPs to the error state before this call returns.
                            *guard = None;
                        }
                        (r, res)
                    })
                })
                .collect();
            // A thread that could not even report has panicked outside the
            // guarded part: nothing to reconcile but its connection, which
            // the guard's drop has left in place; fail the whole batch.
            handles
                .into_iter()
                .map(|h| h.join().unwrap_or((0..n, None)))
                .collect()
        });
        let mut dropped = false;
        for (r, res) in results {
            match res {
                Some(v) => out[r].iter_mut().zip(v).for_each(|(o, x)| *o = x),
                None => {
                    dropped = true;
                    out[r].iter_mut().for_each(|o| *o = fail());
                }
            }
        }
        if dropped {
            if let Some((stop, _)) = &self.redialer {
                stop.1.notify_all();
            }
        }
        Ok(())
    }
}

impl Drop for BkvClient {
    /// Stop the redialer first; then `inner` drops: connections, region,
    /// GPU buffer, in that order.
    fn drop(&mut self) {
        if let Some((stop, handle)) = self.redialer.take() {
            *stop.0.lock() = true;
            stop.1.notify_all();
            let _ = handle.join();
        }
    }
}

/// Checks shared by every batch call; `Ok(false)` for an empty batch.
fn check_batch(n: u32, key_len: u32) -> Result<bool, String> {
    if n == 0 {
        return Ok(false);
    }
    if !(MIN_KEY_LEN..=MAX_KEY_LEN).contains(&(key_len as usize)) {
        return Err(format!(
            "keys of {key_len} bytes: keys are {MIN_KEY_LEN}..={MAX_KEY_LEN}"
        ));
    }
    Ok(true)
}

fn check_budget(budget_ms: u32) -> Result<(), String> {
    if budget_ms == 0 || budget_ms > MAX_BUDGET_MS {
        return Err(format!("budget of {budget_ms} ms (1..={MAX_BUDGET_MS})"));
    }
    Ok(())
}

/// Every slot inside the pool, none named twice.
fn check_slots(slots: &[u32], pool: u32) -> Result<(), String> {
    if pool == 0 {
        return Err("a lookup-only client has no landing pool".into());
    }
    let mut seen = std::collections::HashSet::with_capacity(slots.len());
    for &s in slots {
        if s >= pool {
            return Err(format!("slot {s} outside the {pool}-slot landing pool"));
        }
        if !seen.insert(s) {
            return Err(format!("slot {s} named twice in one batch"));
        }
    }
    Ok(())
}

fn keys(raw: &[u8], n: usize, key_len: usize) -> Result<Vec<Key>, String> {
    if raw.len() != n * key_len {
        return Err("key buffer size".into());
    }
    raw.chunks(key_len)
        .map(|k| Key::new(k).map_err(|e| e.to_string()))
        .collect()
}

fn write_err(err: *mut c_char, err_len: usize, msg: &str) {
    if err.is_null() || err_len == 0 {
        return;
    }
    let n = msg.len().min(err_len - 1);
    // SAFETY: the caller gave `err_len` writable bytes at `err`.
    unsafe {
        std::ptr::copy_nonoverlapping(msg.as_ptr(), err.cast(), n);
        *err.add(n) = 0;
    }
}

/// Run `f`, turning an error or a panic into -1 and a message.
fn guarded(err: *mut c_char, err_len: usize, f: impl FnOnce() -> Result<(), String>) -> i32 {
    match std::panic::catch_unwind(std::panic::AssertUnwindSafe(f)) {
        Ok(Ok(())) => 0,
        Ok(Err(e)) => {
            write_err(err, err_len, &e);
            -1
        }
        Err(_) => {
            write_err(err, err_len, "nvstor_kv: internal panic");
            -1
        }
    }
}

/// Open a client (see [`BkvClient::open`]); on success `*out` holds it.
///
/// # Safety
/// C strings are NUL-terminated; `pages` has `n_pages` entries; `out` is
/// writable; the landing range (`landing_region_bytes`: rounded up to whole host
/// pages) is valid GPU memory until `bkv_close`.
#[no_mangle]
pub unsafe extern "C" fn bkv_open(
    host: *const c_char,
    port: u16,
    rails: u16,
    devices: *const c_char,
    connections: u32,
    gpu: u32,
    landing_ptr: u64,
    pages: *const u32,
    n_pages: u32,
    slots: u32,
    out: *mut *mut BkvClient,
    err: *mut c_char,
    err_len: usize,
) -> i32 {
    guarded(err, err_len, || {
        if host.is_null() || devices.is_null() || pages.is_null() || out.is_null() {
            return Err("null argument".into());
        }
        // SAFETY: the caller's contract (above).
        let (host, devices) = unsafe { (CStr::from_ptr(host), CStr::from_ptr(devices)) };
        let host = host.to_str().map_err(|_| "host is not UTF-8")?;
        let devices = devices.to_str().map_err(|_| "devices is not UTF-8")?;
        // SAFETY: `pages` has `n_pages` entries.
        let pages = unsafe { std::slice::from_raw_parts(pages, n_pages as usize) };
        // SAFETY: forwarded contract.
        let c = unsafe {
            BkvClient::open(
                host,
                port,
                rails,
                devices,
                connections as usize,
                gpu,
                landing_ptr,
                pages,
                slots,
            )
        }?;
        // SAFETY: `out` is writable.
        unsafe { *out = Box::into_raw(Box::new(c)) };
        Ok(())
    })
}

/// GET `n` blocks: keys (each `key_len` bytes) into landing slots
/// `slots[i]`. Fills `statuses[i]` and `checks[i]`.
///
/// # Safety
/// `client` came from `bkv_open`; `namespace` has 16 bytes; `keys` has
/// `n * key_len`; `slots`, `statuses` and `checks` have `n` entries.
#[no_mangle]
pub unsafe extern "C" fn bkv_get(
    client: *const BkvClient,
    namespace: *const u8,
    keys_raw: *const u8,
    key_len: u32,
    slots: *const u32,
    n: u32,
    budget_ms: u32,
    statuses: *mut u8,
    checks: *mut u64,
    err: *mut c_char,
    err_len: usize,
) -> i32 {
    guarded(err, err_len, || {
        // SAFETY: the caller's contract (above).
        let c = unsafe { client.as_ref() }.ok_or("null client")?;
        if !check_batch(n, key_len)? {
            return Ok(());
        }
        check_budget(budget_ms)?;
        // SAFETY: the caller's contract (above); n > 0, so the pointers are real.
        let (ns, raw, slots, statuses, checks) = unsafe {
            (
                *(namespace as *const [u8; NAMESPACE_LEN]),
                std::slice::from_raw_parts(keys_raw, n as usize * key_len as usize),
                std::slice::from_raw_parts(slots, n as usize),
                std::slice::from_raw_parts_mut(statuses, n as usize),
                std::slice::from_raw_parts_mut(checks, n as usize),
            )
        };
        let keys = keys(raw, n as usize, key_len as usize)?;
        check_slots(slots, c.inner.slots)?;
        let mut out = vec![(STATUS_ERROR, 0u64); n as usize];
        c.fan_out(
            n as usize,
            &mut out,
            || (STATUS_ERROR, 0),
            |st| (st, 0),
            |client, r| {
                let entries = r
                    .clone()
                    .map(|i| GetEntry {
                        key: keys[i],
                        block: slots[i],
                    })
                    .collect();
                client
                    .get(ns, LAYOUT_ID, budget_ms, entries)
                    .map(|v| v.into_iter().map(|g| (g.status, g.check)).collect())
            },
        )?;
        // The replies came after every WRITE completed; make the bytes
        // visible to the GPU work the caller issues next.
        if let Some(gpu) = &c.inner.gpu {
            gpu.make_visible()?;
        }
        for (i, (s, k)) in out.into_iter().enumerate() {
            statuses[i] = s;
            checks[i] = k;
        }
        Ok(())
    })
}

/// PUT `n` blocks from landing slots `slots[i]` under keys, with the
/// writer's block checks `checks[i]`. Fills `statuses[i]`.
///
/// # Safety
/// As for [`bkv_get`], with `checks` read.
#[no_mangle]
pub unsafe extern "C" fn bkv_put(
    client: *const BkvClient,
    namespace: *const u8,
    keys_raw: *const u8,
    key_len: u32,
    slots: *const u32,
    checks: *const u64,
    n: u32,
    budget_ms: u32,
    statuses: *mut u8,
    err: *mut c_char,
    err_len: usize,
) -> i32 {
    guarded(err, err_len, || {
        // SAFETY: the caller's contract (above).
        let c = unsafe { client.as_ref() }.ok_or("null client")?;
        if !check_batch(n, key_len)? {
            return Ok(());
        }
        check_budget(budget_ms)?;
        // SAFETY: the caller's contract (above); n > 0, so the pointers are real.
        let (ns, raw, slots, checks, statuses) = unsafe {
            (
                *(namespace as *const [u8; NAMESPACE_LEN]),
                std::slice::from_raw_parts(keys_raw, n as usize * key_len as usize),
                std::slice::from_raw_parts(slots, n as usize),
                std::slice::from_raw_parts(checks, n as usize),
                std::slice::from_raw_parts_mut(statuses, n as usize),
            )
        };
        let keys = keys(raw, n as usize, key_len as usize)?;
        check_slots(slots, c.inner.slots)?;
        c.fan_out(
            n as usize,
            statuses,
            || STATUS_ERROR,
            |st| st,
            |client, r| {
                let entries = r
                    .clone()
                    .map(|i| PutEntry {
                        key: keys[i],
                        block: slots[i],
                        check: checks[i],
                    })
                    .collect();
                client.put(ns, LAYOUT_ID, budget_ms, entries)
            },
        )
    })
}

/// HEAD `n` keys. Fills `statuses[i]`.
///
/// # Safety
/// `client` came from `bkv_open`; `namespace` has 16 bytes; `keys` has
/// `n * key_len`; `statuses` has `n` entries.
#[no_mangle]
pub unsafe extern "C" fn bkv_head(
    client: *const BkvClient,
    namespace: *const u8,
    keys_raw: *const u8,
    key_len: u32,
    n: u32,
    statuses: *mut u8,
    err: *mut c_char,
    err_len: usize,
) -> i32 {
    guarded(err, err_len, || {
        // SAFETY: the caller's contract (above).
        let c = unsafe { client.as_ref() }.ok_or("null client")?;
        if !check_batch(n, key_len)? {
            return Ok(());
        }
        // SAFETY: the caller's contract (above); n > 0, so the pointers are real.
        let (ns, raw, statuses) = unsafe {
            (
                *(namespace as *const [u8; NAMESPACE_LEN]),
                std::slice::from_raw_parts(keys_raw, n as usize * key_len as usize),
                std::slice::from_raw_parts_mut(statuses, n as usize),
            )
        };
        let keys = keys(raw, n as usize, key_len as usize)?;
        c.fan_out(
            n as usize,
            statuses,
            || STATUS_ERROR,
            |st| st,
            |client, r| client.head(ns, keys[r].to_vec()),
        )
    })
}

/// Open a client for HEAD only (no landing pool; see
/// [`BkvClient::open_lookup`]); on success `*out` holds it.
///
/// # Safety
/// C strings are NUL-terminated; `out` is writable.
#[no_mangle]
pub unsafe extern "C" fn bkv_open_lookup(
    host: *const c_char,
    port: u16,
    rails: u16,
    devices: *const c_char,
    connections: u32,
    out: *mut *mut BkvClient,
    err: *mut c_char,
    err_len: usize,
) -> i32 {
    guarded(err, err_len, || {
        if host.is_null() || devices.is_null() || out.is_null() {
            return Err("null argument".into());
        }
        // SAFETY: the caller's contract (above).
        let (host, devices) = unsafe { (CStr::from_ptr(host), CStr::from_ptr(devices)) };
        let host = host.to_str().map_err(|_| "host is not UTF-8")?;
        let devices = devices.to_str().map_err(|_| "devices is not UTF-8")?;
        let c = BkvClient::open_lookup(host, port, rails, devices, connections as usize)?;
        // SAFETY: `out` is writable.
        unsafe { *out = Box::into_raw(Box::new(c)) };
        Ok(())
    })
}

/// The 64-bit block check of a payload in host memory:
/// XXH3-64 over (namespace ‖ key ‖ payload), into `*out`.
///
/// # Safety
/// `namespace` has 16 bytes, `key` `key_len`, `data` `len`; `out` is
/// writable. A pointer may be null only when its length is 0.
#[no_mangle]
pub unsafe extern "C" fn bkv_block_check(
    namespace: *const u8,
    key: *const u8,
    key_len: u32,
    data: *const u8,
    len: usize,
    out: *mut u64,
    err: *mut c_char,
    err_len: usize,
) -> i32 {
    guarded(err, err_len, || {
        if namespace.is_null()
            || out.is_null()
            || (key.is_null() && key_len > 0)
            || (data.is_null() && len > 0)
        {
            return Err("null argument".into());
        }
        // SAFETY: the caller's contract (above); empty slices for zero lengths.
        let (ns, key, data) = unsafe {
            (
                &*(namespace as *const [u8; NAMESPACE_LEN]),
                if key_len == 0 {
                    &[][..]
                } else {
                    std::slice::from_raw_parts(key, key_len as usize)
                },
                if len == 0 {
                    &[][..]
                } else {
                    std::slice::from_raw_parts(data, len)
                },
            )
        };
        // SAFETY: `out` is writable.
        unsafe { *out = crate::server::block_check(ns, key, data) };
        Ok(())
    })
}

/// Connections still usable.
///
/// # Safety
/// `client` came from `bkv_open`.
#[no_mangle]
pub unsafe extern "C" fn bkv_live_connections(client: *const BkvClient) -> u32 {
    // SAFETY: the caller's contract.
    unsafe { client.as_ref() }.map_or(0, |c| c.live() as u32)
}

/// Close a client: connections first (destroying their QPs), then the
/// landing pool's registrations. The landing memory may be freed after.
///
/// # Safety
/// `client` came from `bkv_open` and is not used again.
#[no_mangle]
pub unsafe extern "C" fn bkv_close(client: *mut BkvClient) {
    if !client.is_null() {
        // SAFETY: the caller's contract; Box::from_raw of our own pointer.
        drop(unsafe { Box::from_raw(client) });
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_landing_layout_puts_each_tensor_s_pages_back_to_back() {
        let l = landing_layout(1 << 20, 7, &[100, 50], 4);
        assert_eq!(
            l.tensors[0],
            TensorRun {
                base: 1 << 20,
                stride: 100,
                page: 100
            }
        );
        assert_eq!(
            l.tensors[1],
            TensorRun {
                base: (1 << 20) + 400,
                stride: 50,
                page: 50
            }
        );
        assert_eq!(l.num_blocks, 4);
        assert_eq!(landing_bytes(&[100, 50], 4), 600);
        let page = host_page();
        assert_eq!(
            landing_region_bytes(&[100, 50], 4),
            page,
            "rounded up to a whole host page"
        );
        assert_eq!(
            landing_region_bytes(&[page as u32], 3),
            3 * page,
            "already whole"
        );
        let pieces: Vec<_> = l.block_pieces(2).expect("slot").collect();
        assert_eq!(
            pieces,
            vec![((1 << 20) + 200, 100, 0), ((1 << 20) + 500, 50, 100)]
        );
    }

    #[test]
    fn requests_are_cut_to_the_server_s_byte_limit() {
        assert_eq!(per_request(&[4096]).expect("small"), MAX_BATCH);
        let big = 3 << 20; // 3 MiB blocks: 85 fit in 256 MiB
        assert_eq!(
            per_request(&[big]).expect("big"),
            (MAX_REQUEST_BYTES / big as u64) as usize
        );
        assert_eq!(
            per_request(&[
                (MAX_REQUEST_BYTES / 2) as u32,
                (MAX_REQUEST_BYTES / 2) as u32
            ])
            .expect("max"),
            1
        );
        assert!(
            per_request(&[u32::MAX, u32::MAX]).is_err(),
            "a block over the limit"
        );
        assert!(per_request(&[0]).is_err());
    }

    #[test]
    fn errors_are_written_truncated_and_terminated() {
        let mut buf = [0x55 as c_char; 8];
        write_err(buf.as_mut_ptr(), buf.len(), "a long error message");
        assert_eq!(
            unsafe { CStr::from_ptr(buf.as_ptr()) }
                .to_str()
                .expect("utf8"),
            "a long "
        );
        assert_eq!(
            guarded(buf.as_mut_ptr(), buf.len(), || Err("no".into())),
            -1
        );
        assert_eq!(guarded(buf.as_mut_ptr(), buf.len(), || panic!("boom")), -1);
        assert_eq!(guarded(buf.as_mut_ptr(), buf.len(), || Ok(())), 0);
        write_err(std::ptr::null_mut(), 0, "ignored");
    }

    #[test]
    fn keys_are_split_and_checked() {
        let raw = [1u8; 72];
        assert_eq!(keys(&raw, 2, 36).expect("keys").len(), 2);
        assert!(keys(&raw, 3, 36).is_err(), "size mismatch");
        assert!(keys(&[1u8; 40], 2, 20).is_err(), "20-byte keys are refused");
    }

    #[test]
    fn the_exported_block_check_is_the_server_s() {
        let ns = [4u8; 16];
        let key = [7u8; 36];
        let data = vec![9u8; 1000];
        let err = [0 as c_char; 64];
        let check = |ns: *const u8, key: *const u8, kl: u32, data: *const u8, len: usize| {
            let mut c = 0u64;
            let r = unsafe {
                bkv_block_check(
                    ns,
                    key,
                    kl,
                    data,
                    len,
                    &mut c,
                    err.as_ptr() as *mut c_char,
                    err.len(),
                )
            };
            (r, c)
        };
        assert_eq!(
            check(ns.as_ptr(), key.as_ptr(), 36, data.as_ptr(), data.len()),
            (0, crate::server::block_check(&ns, &key, &data))
        );
        assert_eq!(
            check(ns.as_ptr(), std::ptr::null(), 0, std::ptr::null(), 0),
            (0, crate::server::block_check(&ns, &[], &[]))
        );
        assert_eq!(
            check(std::ptr::null(), key.as_ptr(), 36, data.as_ptr(), 1).0,
            -1,
            "no namespace"
        );
        assert_eq!(
            check(ns.as_ptr(), std::ptr::null(), 36, data.as_ptr(), 1).0,
            -1,
            "a null key with a length"
        );
        assert_eq!(
            check(ns.as_ptr(), key.as_ptr(), 36, std::ptr::null(), 1).0,
            -1,
            "null data with a length"
        );
        assert!(
            check_slots(&[0], 0).is_err(),
            "a lookup-only client has no slots"
        );
    }

    #[test]
    fn batches_budgets_and_slots_are_checked() {
        assert_eq!(
            check_batch(0, 0),
            Ok(false),
            "an empty batch is a no-op, whatever else"
        );
        assert!(check_batch(1, 20).is_err() && check_batch(1, 65).is_err());
        assert_eq!(check_batch(1, 36), Ok(true));
        assert!(check_budget(0).is_err() && check_budget(MAX_BUDGET_MS + 1).is_err());
        assert!(check_budget(1).is_ok());
        assert!(check_slots(&[0, 1, 2], 3).is_ok());
        assert!(check_slots(&[3], 3).is_err(), "outside the pool");
        assert!(check_slots(&[1, 2, 1], 3).is_err(), "named twice");
    }

    #[test]
    fn a_null_client_is_an_error_not_a_crash() {
        let mut err = [0 as c_char; 64];
        let mut statuses = [0u8; 1];
        let ns = [0u8; 16];
        let key = [1u8; 36];
        let r = unsafe {
            bkv_head(
                std::ptr::null(),
                ns.as_ptr(),
                key.as_ptr(),
                36,
                1,
                statuses.as_mut_ptr(),
                err.as_mut_ptr(),
                err.len(),
            )
        };
        assert_eq!(r, -1);
        assert_eq!(unsafe { bkv_live_connections(std::ptr::null()) }, 0);
        unsafe { bkv_close(std::ptr::null_mut()) };
    }
}
