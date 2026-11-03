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

//! Completion engine: one poller per transport routes every CQE to the
//! operation or lane it belongs to.
//!
//! Before this, each operation posted its work requests and then polled the
//! lane's CQ itself under a per-lane lock (spinning 200 µs, then sleeping in
//! steps up to 1 ms), so a lane ran one operation at a time and nearly every
//! wait paid a sleep. Here:
//!
//! - A transport has one send CQ and one receive CQ shared by all its
//!   lanes, both attached to one completion channel, and one poller thread.
//!   The poller drains up to [`POLL_BATCH`] CQEs per call, keeps spinning for
//!   [`SPIN_WINDOW`] after the last CQE while operations are in flight,
//!   then arms both CQs, polls once more (a CQE that arrived before arming
//!   raises no event), and blocks in `poll(2)` on the channel and a shutdown
//!   eventfd. CQ events are acknowledged [`ACK_BATCH`] at a time. Engines
//!   spread over the device's completion vectors (interrupts).
//! - A send-side operation (a chain of one-sided WRs, or a SEND) holds a
//!   [`Slot`]. Every WR it posts carries the slot's id in its `wr_id`, the
//!   last one with [`TAIL_BIT`]. The poller records the first error CQE of
//!   the slot and completes it on the tail CQE, signaled or flushed (after
//!   an error every later WR completes, unsignaled ones included). The
//!   waiting thread parks; the poller unparks it.
//! - A receive's `wr_id` is its lane key and buffer index
//!   ([`recv_wr_id`]); the poller hands the CQE to that lane's
//!   [`RecvQueue`]. A lane being torn down still gets its flush CQEs, which
//!   is how an idle dispatcher learns its connection is gone.
//! - Nothing can overflow: a lane's posters take send-queue [`Credits`] for
//!   every WR they post, and every WR also takes a credit of the send CQ's
//!   budget, both given back when the slot completes. Receive buffers take
//!   receive-CQ budget for the life of their lane. Credits are granted in
//!   arrival order, so a long chain is not starved by small requests.
//! - A slot is reused only after its tail CQE. Its `wr_id` carries a
//!   generation, so a CQE of an older use (which cannot happen while the
//!   rule above holds) would be ignored rather than misrouted.
//!
//! Failure handling ([`Dispatch::run`]):
//!
//! - Waiting for credits has its own bound; on expiry the operation fails
//!   without posting anything and without touching the QP. The completion
//!   bound starts only once the work requests are posted.
//! - A failed completion, or none within the bound, makes the lane
//!   *failed* ([`LaneState::fail`]): its QP is (or is moved) in the error
//!   state, and every later operation on it fails at once. The transport
//!   then removes the peer, so callers see a disconnected peer.
//! - An operation whose tail completion never comes is abandoned and
//!   recorded on its lane; its slot and credits come back when the lane is
//!   unregistered, which happens only after the QP is destroyed (the
//!   provider then discards the QP's CQEs, and the HCA no longer touches
//!   its buffers). Its error says so ([`OpError::unobserved`]), and the
//!   caller hands the buffer's owner to the lane ([`LaneState::hold`])
//!   instead of reusing it. If the QP cannot be destroyed, all of it is
//!   leaked ([`Dispatch::leaked`]), never reused.
//! - If the poller stops for any reason (a failed `ibv_poll_cq`, a fault
//!   on the channel fd, a panic) the engine is *dead* ([`Dispatch::kill`]):
//!   every parked waiter is woken and failed, every receive queue is
//!   closed, every lane is failed, and every later call fails at once.

use std::collections::{HashMap, VecDeque};
use std::sync::atomic::{AtomicBool, AtomicU64, AtomicUsize, Ordering};
use std::sync::Arc;
use std::thread::{self, Thread};
use std::time::{Duration, Instant};

use parking_lot::{Condvar, Mutex, RwLock};
use tracing::{debug, error, warn};

use crate::connection::CompletionQueue;
use crate::device::RdmaDevice;
use crate::ffi;

/// CQEs taken per `ibv_poll_cq`.
pub const POLL_BATCH: usize = 32;
/// How long the poller keeps polling after the last CQE while operations
/// are in flight, before it arms the CQs and sleeps. A wake from the
/// channel costs an interrupt and a context switch, so short gaps between
/// completions are cheaper to spin through.
pub const SPIN_WINDOW: Duration = Duration::from_micros(50);
/// CQ events acknowledged per `ibv_ack_cq_events` call.
pub const ACK_BATCH: u32 = 128;
/// Most operations in flight at once on one engine.
pub const MAX_SLOTS: usize = 8192;
/// Set on the `wr_id` of an operation's last WR.
pub const TAIL_BIT: u64 = 1 << 63;
/// Low bits of a receive `wr_id`: the buffer index within its lane.
pub const RECV_INDEX_BITS: u32 = 8;

/// A failed completion, as the CQE reported it.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct WcError {
    pub status: u32,
    pub opcode: u32,
    pub vendor_err: u32,
}

impl std::fmt::Display for WcError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "WC error: opcode={} status={} (vendor_err=0x{:x})",
            self.opcode,
            ffi::wc_status_str(self.status),
            self.vendor_err
        )
    }
}

fn wc_error(wc: &ffi::ibv_wc) -> Option<WcError> {
    (wc.status != ffi::IBV_WC_SUCCESS).then_some(WcError {
        status: wc.status,
        opcode: wc.opcode,
        vendor_err: wc.vendor_err,
    })
}

// ---------------------------------------------------------------------
// Bounds
// ---------------------------------------------------------------------

/// A bound on a wait. The deadline is read from the clock only when a
/// wait actually has to block, so a wait that is satisfied at once costs
/// no clock read; once read, every later wait under the same bound
/// shares that deadline.
#[derive(Clone, Copy, Debug)]
pub struct Bound {
    timeout: Duration,
    at: Option<Instant>,
}

impl Bound {
    /// `timeout` from the first wait that blocks.
    pub fn new(timeout: Duration) -> Self {
        Self { timeout, at: None }
    }

    /// A fixed deadline.
    pub fn until(deadline: Instant) -> Self {
        Self {
            timeout: Duration::ZERO,
            at: Some(deadline),
        }
    }

    fn deadline(&mut self) -> Instant {
        *self.at.get_or_insert_with(|| Instant::now() + self.timeout)
    }

    /// Time left, `None` once passed: one clock read.
    fn remaining(&mut self) -> Option<Duration> {
        let now = Instant::now();
        let deadline = *self.at.get_or_insert(now + self.timeout);
        deadline
            .checked_duration_since(now)
            .filter(|d| !d.is_zero())
    }
}

/// How long [`Dispatch::run`] waits for each phase of an operation.
#[derive(Clone, Copy, Debug)]
pub struct Limits {
    /// For send-queue, send-CQ and slot credits, before anything is posted.
    pub credits: Duration,
    /// For the completion, from the moment the WRs are posted; and again
    /// for the flush after the QP is stopped.
    pub completion: Duration,
}

// ---------------------------------------------------------------------
// Credits
// ---------------------------------------------------------------------

/// A counting budget (send-queue slots, CQ entries). `acquire` blocks
/// until enough are free or its bound passes. Waiters are served in
/// arrival order: a request for many credits is not overtaken by later
/// requests for few, so it cannot be starved. Only the waiter at the head
/// of the line is woken, and only when its request fits.
pub struct Credits {
    state: Mutex<CreditState>,
    capacity: u64,
}

struct CreditState {
    free: u64,
    closed: bool,
    waiters: VecDeque<CreditWaiter>,
    next_id: u64,
}

struct CreditWaiter {
    id: u64,
    n: u64,
    thread: Thread,
}

impl CreditState {
    /// The head waiter, if its request now fits: the caller unparks it
    /// after dropping the lock, so the woken thread does not run straight
    /// into a held mutex (and the poller, releasing the send-CQ budget,
    /// does not make the syscall under it).
    fn head_to_wake(&self) -> Option<Thread> {
        self.waiters
            .front()
            .filter(|h| self.free >= h.n)
            .map(|h| h.thread.clone())
    }

    fn leave(&mut self, id: u64) -> Option<Thread> {
        if let Some(i) = self.waiters.iter().position(|w| w.id == id) {
            self.waiters.remove(i);
        }
        self.head_to_wake()
    }
}

fn unpark(t: Option<Thread>) {
    if let Some(t) = t {
        t.unpark();
    }
}

impl Credits {
    pub fn new(capacity: u64) -> Arc<Self> {
        Arc::new(Self {
            state: Mutex::new(CreditState {
                free: capacity,
                closed: false,
                waiters: VecDeque::new(),
                next_id: 0,
            }),
            capacity,
        })
    }

    pub fn capacity(&self) -> u64 {
        self.capacity
    }

    pub fn available(&self) -> u64 {
        self.state.lock().free
    }

    /// Threads waiting in [`Self::acquire`].
    pub fn waiting(&self) -> usize {
        self.state.lock().waiters.len()
    }

    /// Take `n`. Fails at once if `n` exceeds the capacity or the budget
    /// is closed, and when `bound` passes before they are free.
    pub fn acquire(&self, n: u64, bound: &mut Bound) -> Result<(), String> {
        if n > self.capacity {
            return Err(format!("{n} credits requested, capacity {}", self.capacity));
        }
        let mut s = self.state.lock();
        if s.closed {
            return Err("credits closed".into());
        }
        // Fast path: nobody is in line and they fit.
        if s.waiters.is_empty() && s.free >= n {
            s.free -= n;
            return Ok(());
        }
        let id = s.next_id;
        s.next_id += 1;
        s.waiters.push_back(CreditWaiter {
            id,
            n,
            thread: thread::current(),
        });
        loop {
            if s.closed {
                let next = s.leave(id);
                drop(s);
                unpark(next);
                return Err("credits closed".into());
            }
            if s.waiters.front().map(|w| w.id) == Some(id) && s.free >= n {
                s.free -= n;
                s.waiters.pop_front();
                let next = s.head_to_wake();
                drop(s);
                unpark(next);
                return Ok(());
            }
            let Some(left) = bound.remaining() else {
                let free = s.free;
                let next = s.leave(id);
                drop(s);
                unpark(next);
                return Err(format!(
                    "{n} credits not free within the deadline ({free} of {} free)",
                    self.capacity
                ));
            };
            drop(s);
            thread::park_timeout(left);
            s = self.state.lock();
        }
    }

    /// Take `n` only if they are free now and nobody is waiting.
    pub fn try_acquire(&self, n: u64) -> bool {
        let mut s = self.state.lock();
        if s.closed || !s.waiters.is_empty() || s.free < n {
            return false;
        }
        s.free -= n;
        true
    }

    pub fn release(&self, n: u64) {
        let mut s = self.state.lock();
        s.free = (s.free + n).min(self.capacity);
        // Nobody waiting (the common case): no clone, no syscall.
        let next = s.head_to_wake();
        drop(s);
        unpark(next);
    }

    /// Fail every waiter and every later `acquire`. Releases still count.
    pub fn close(&self) {
        let waiters: Vec<Thread> = {
            let mut s = self.state.lock();
            s.closed = true;
            s.waiters.iter().map(|w| w.thread.clone()).collect()
        };
        for t in waiters {
            t.unpark();
        }
    }
}

/// Credits taken by one operation, given back when it completes.
struct Grant {
    from: Vec<(Arc<Credits>, u64)>,
}

impl std::fmt::Debug for Grant {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let n: Vec<u64> = self.from.iter().map(|(_, n)| *n).collect();
        write!(f, "Grant({n:?})")
    }
}

impl Grant {
    fn release(self) {
        for (c, n) in self.from {
            c.release(n);
        }
    }
}

// ---------------------------------------------------------------------
// Slots
// ---------------------------------------------------------------------

/// What an operation's waiter learns.
#[derive(Debug, PartialEq, Eq)]
pub enum Outcome {
    /// Every WR completed; `Some` is the first one that failed.
    Done(Option<WcError>),
    /// The deadline passed first; the operation is still in flight.
    Pending,
    /// The engine's poller has stopped: no completion will ever come.
    Dead,
}

struct SlotState {
    generation: u32,
    in_use: bool,
    done: bool,
    /// The waiter gave up on it; free the slot when its tail CQE comes,
    /// or when its lane is unregistered.
    abandoned: bool,
    /// Abandoned on a lane whose QP could not be destroyed: counted in
    /// [`SlotTable::leaked`] until (if ever) its tail CQE frees it.
    leaked: bool,
    error: Option<WcError>,
    waiter: Option<Thread>,
    grant: Option<Grant>,
}

struct Slot {
    state: Mutex<SlotState>,
}

/// One operation's claim on a slot.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Ticket {
    index: u32,
    generation: u32,
}

impl Ticket {
    /// `wr_id` for the operation's WRs; `tail` for the last one.
    pub fn wr_id(&self, tail: bool) -> u64 {
        let id = (u64::from(self.generation & 0x7fff_ffff) << 32) | u64::from(self.index);
        if tail {
            id | TAIL_BIT
        } else {
            id
        }
    }
}

/// Fixed table of operation slots, addressed by `wr_id`.
pub struct SlotTable {
    slots: Box<[Slot]>,
    free: Mutex<Vec<u32>>,
    /// One per free slot: taken before `alloc`, so `alloc` never finds the
    /// table empty, and a full table is waited on like any other budget.
    credits: Arc<Credits>,
    in_use: AtomicUsize,
    stale: AtomicU64,
    /// Set once the poller has stopped; no completion will come.
    dead: AtomicBool,
    /// Slots abandoned on lanes whose QP could not be destroyed, not
    /// (yet) freed by a late tail CQE.
    leaked: AtomicU64,
}

impl SlotTable {
    pub fn new(capacity: usize) -> Self {
        let capacity = capacity.clamp(1, u32::MAX as usize);
        let slots = (0..capacity)
            .map(|_| Slot {
                state: Mutex::new(SlotState {
                    generation: 0,
                    in_use: false,
                    done: false,
                    abandoned: false,
                    leaked: false,
                    error: None,
                    waiter: None,
                    grant: None,
                }),
            })
            .collect();
        Self {
            slots,
            free: Mutex::new((0..capacity as u32).rev().collect()),
            credits: Credits::new(capacity as u64),
            in_use: AtomicUsize::new(0),
            stale: AtomicU64::new(0),
            dead: AtomicBool::new(false),
            leaked: AtomicU64::new(0),
        }
    }

    pub fn in_use(&self) -> usize {
        self.in_use.load(Ordering::Relaxed)
    }

    /// CQEs that named a slot's earlier use (none are expected).
    pub fn stale(&self) -> u64 {
        self.stale.load(Ordering::Relaxed)
    }

    /// A slot for the calling thread, which will wait on it; waits for one
    /// to free up within `bound`.
    /// The slot credit guarantees the free list is not empty.
    fn alloc_wait(&self, grant: Grant, bound: &mut Bound) -> Result<Ticket, Grant> {
        if self.credits.acquire(1, bound).is_err() {
            return Err(grant);
        }
        let Some(index) = self.free.lock().pop() else {
            // Unreachable while every free slot has its credit; give the
            // credit back rather than lose it.
            self.credits.release(1);
            return Err(grant);
        };
        let mut st = self.slots[index as usize].state.lock();
        st.generation = st.generation.wrapping_add(1) & 0x7fff_ffff;
        st.in_use = true;
        st.done = false;
        st.abandoned = false;
        st.leaked = false;
        st.error = None;
        st.waiter = Some(thread::current());
        st.grant = Some(grant);
        self.in_use.fetch_add(1, Ordering::Relaxed);
        Ok(Ticket {
            index,
            generation: st.generation,
        })
    }

    fn release_slot(&self, index: u32, st: &mut SlotState) {
        st.in_use = false;
        st.waiter = None;
        self.in_use.fetch_sub(1, Ordering::Relaxed);
        self.free.lock().push(index);
        self.credits.release(1);
    }

    /// Route one send-CQ completion.
    pub fn complete(&self, wr_id: u64, error: Option<WcError>) {
        let index = (wr_id & 0xffff_ffff) as u32;
        let generation = ((wr_id >> 32) & 0x7fff_ffff) as u32;
        let Some(slot) = self.slots.get(index as usize) else {
            self.stale.fetch_add(1, Ordering::Relaxed);
            return;
        };
        let mut st = slot.state.lock();
        if !st.in_use || st.generation != generation || st.done {
            drop(st);
            self.stale.fetch_add(1, Ordering::Relaxed);
            return;
        }
        if st.error.is_none() {
            st.error = error;
        }
        if wr_id & TAIL_BIT == 0 {
            return;
        }
        st.done = true;
        if let Some(g) = st.grant.take() {
            g.release();
        }
        if st.abandoned {
            if st.leaked {
                // Its QP outlived its lane after all; no longer leaked.
                st.leaked = false;
                self.leaked.fetch_sub(1, Ordering::Relaxed);
            }
            self.release_slot(index, &mut st);
            return;
        }
        if let Some(t) = st.waiter.clone() {
            drop(st);
            t.unpark();
        }
    }

    /// Wait for `t` to complete, within `bound`. `Done` frees the slot.
    pub fn wait(&self, t: Ticket, bound: &mut Bound) -> Outcome {
        let slot = &self.slots[t.index as usize];
        loop {
            {
                let mut st = slot.state.lock();
                debug_assert_eq!(st.generation, t.generation);
                if st.done {
                    let error = st.error.take();
                    self.release_slot(t.index, &mut st);
                    return Outcome::Done(error);
                }
            }
            // Only reached while the operation is still in flight.
            if self.dead.load(Ordering::Acquire) {
                return Outcome::Dead;
            }
            let Some(left) = bound.remaining() else {
                return Outcome::Pending;
            };
            thread::park_timeout(left);
        }
    }

    /// Give up waiting for `t`. If it already completed the slot is freed
    /// now (false); otherwise it is freed when its tail CQE arrives or its
    /// lane is swept (true: the caller records it for the sweep).
    pub fn abandon(&self, t: Ticket) -> bool {
        let slot = &self.slots[t.index as usize];
        let mut st = slot.state.lock();
        if st.generation != t.generation || !st.in_use {
            return false;
        }
        if st.done {
            self.release_slot(t.index, &mut st);
            false
        } else {
            st.abandoned = true;
            st.waiter = None;
            true
        }
    }

    /// Free the abandoned operations `tickets` (of one lane) that are
    /// still uncompleted, with their credits. Only for a lane whose QP is
    /// destroyed: no CQE can come for them any more, and the HCA no longer
    /// touches their buffers. A ticket whose tail CQE came meanwhile was
    /// freed then, and its slot may be reused: the generation check skips
    /// it. Returns how many were freed. O(tickets).
    fn sweep(&self, tickets: &[Ticket]) -> usize {
        let mut n = 0;
        for t in tickets {
            let Some(slot) = self.slots.get(t.index as usize) else {
                continue;
            };
            let mut st = slot.state.lock();
            if st.in_use && st.abandoned && !st.done && st.generation == t.generation {
                st.done = true;
                if let Some(g) = st.grant.take() {
                    g.release();
                }
                self.release_slot(t.index, &mut st);
                n += 1;
            }
        }
        n
    }

    /// The abandoned operations `tickets` of a lane whose QP could not be
    /// destroyed: those still uncompleted keep their slots and credits
    /// and are counted as leaked until a tail CQE frees them. Returns how
    /// many were marked.
    fn mark_leaked(&self, tickets: &[Ticket]) -> u64 {
        let mut n = 0;
        for t in tickets {
            let Some(slot) = self.slots.get(t.index as usize) else {
                continue;
            };
            let mut st = slot.state.lock();
            if st.in_use && st.abandoned && !st.done && st.generation == t.generation && !st.leaked
            {
                st.leaked = true;
                n += 1;
            }
        }
        self.leaked.fetch_add(n, Ordering::Relaxed);
        n
    }

    /// The poller is gone: wake every waiter (it sees `Dead`) and fail
    /// every later slot allocation.
    fn kill(&self) {
        self.dead.store(true, Ordering::Release);
        self.credits.close();
        for slot in self.slots.iter() {
            let st = slot.state.lock();
            if st.in_use && !st.done {
                if let Some(t) = &st.waiter {
                    t.unpark();
                }
            }
        }
    }
}

// ---------------------------------------------------------------------
// Receive routing and lanes
// ---------------------------------------------------------------------

/// `wr_id` of receive buffer `index` of the lane with `key`.
pub fn recv_wr_id(key: u64, index: usize) -> u64 {
    (key << RECV_INDEX_BITS) | index as u64
}

/// The buffer index of a receive `wr_id`.
pub fn recv_index(wr_id: u64) -> usize {
    (wr_id & ((1 << RECV_INDEX_BITS) - 1)) as usize
}

fn recv_key(wr_id: u64) -> u64 {
    wr_id >> RECV_INDEX_BITS
}

/// Why [`RecvQueue::pop`] returned no CQE.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RecvError {
    /// The deadline passed.
    Timeout,
    /// The queue is closed (the engine stopped) and drained.
    Closed,
}

/// Receive completions of one lane, in arrival order.
pub struct RecvQueue {
    q: Mutex<RecvState>,
    cv: Condvar,
}

struct RecvState {
    q: VecDeque<ffi::ibv_wc>,
    closed: bool,
}

impl RecvQueue {
    fn new() -> Self {
        Self {
            q: Mutex::new(RecvState {
                q: VecDeque::new(),
                closed: false,
            }),
            cv: Condvar::new(),
        }
    }

    fn push(&self, wc: ffi::ibv_wc) {
        self.q.lock().q.push_back(wc);
        self.cv.notify_one();
    }

    /// Wake every waiter; once drained, `pop` fails with `Closed`.
    fn close(&self) {
        self.q.lock().closed = true;
        self.cv.notify_all();
    }

    /// The next receive CQE, waiting until `deadline` (`None`: until one
    /// arrives or the queue is closed).
    pub fn pop(&self, deadline: Option<Instant>) -> Result<ffi::ibv_wc, RecvError> {
        let mut q = self.q.lock();
        loop {
            if let Some(wc) = q.q.pop_front() {
                return Ok(wc);
            }
            if q.closed {
                return Err(RecvError::Closed);
            }
            match deadline {
                None => self.cv.wait(&mut q),
                Some(d) => {
                    if self.cv.wait_until(&mut q, d).timed_out() {
                        return q.q.pop_front().ok_or(RecvError::Timeout);
                    }
                }
            }
        }
    }
}

/// One lane's state in the engine: its send-queue credits, its receive
/// queue, whether it has failed, and what must outlive its QP.
pub struct LaneState {
    key: u64,
    sq: Arc<Credits>,
    recvq: RecvQueue,
    failed: AtomicBool,
    reason: Mutex<Option<String>>,
    leftovers: Mutex<Leftovers>,
}

/// What a lane keeps until its QP is destroyed: operations whose last
/// completion never came, and buffers such operations used (the HCA may
/// still read or write them). Touched only on failure paths.
#[derive(Default)]
struct Leftovers {
    abandoned: Vec<Ticket>,
    held: Vec<Box<dyn Send>>,
    /// Set when the lane is unregistered: `Some(true)` once its QP was
    /// destroyed (held buffers are safe to free), `Some(false)` if
    /// destroying it failed (they are leaked, never reused).
    released: Option<bool>,
}

impl std::fmt::Debug for LaneState {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("LaneState")
            .field("key", &self.key)
            .field("failed", &self.failed())
            .finish()
    }
}

impl LaneState {
    fn new(key: u64, sq_depth: u64) -> Self {
        Self {
            key,
            sq: Credits::new(sq_depth),
            recvq: RecvQueue::new(),
            failed: AtomicBool::new(false),
            reason: Mutex::new(None),
            leftovers: Mutex::new(Leftovers::default()),
        }
    }

    /// Keep `buf` (a buffer an operation of this lane used and whose last
    /// completion was never seen, so the HCA may still DMA to or from it)
    /// until the lane's QP is destroyed; only then is it dropped (a pool
    /// buffer then goes back to its pool). If the QP is already destroyed
    /// it is dropped now; if destroying it failed, it is leaked.
    pub fn hold<T: Send + 'static>(&self, buf: T) {
        let mut l = self.leftovers.lock();
        match l.released {
            None => l.held.push(Box::new(buf)),
            Some(true) => {
                drop(l);
                drop(buf);
            }
            Some(false) => {
                drop(l);
                error!(
                    lane = self.key,
                    "QP of this lane was never destroyed; leaking a buffer it may still use"
                );
                std::mem::forget(buf);
            }
        }
    }

    /// Buffers held by [`Self::hold`] and not yet released.
    pub fn held(&self) -> usize {
        self.leftovers.lock().held.len()
    }

    /// The key its receive `wr_id`s carry ([`recv_wr_id`]).
    pub fn key(&self) -> u64 {
        self.key
    }

    /// Its send-queue credits.
    pub fn sq(&self) -> &Arc<Credits> {
        &self.sq
    }

    /// Whether the lane is unusable (its QP is in the error state, or a
    /// control reply went missing, or the engine stopped).
    pub fn failed(&self) -> bool {
        self.failed.load(Ordering::Acquire)
    }

    /// Why it failed.
    pub fn failure(&self) -> Option<String> {
        self.reason.lock().clone()
    }

    fn failed_error(&self) -> String {
        format!(
            "lane failed: {}",
            self.failure().unwrap_or_else(|| "unknown".into())
        )
    }

    /// Mark the lane unusable: every later operation on it fails at once,
    /// and threads waiting for its send-queue credits are failed now.
    /// Returns true the first time.
    pub fn fail(&self, why: &str) -> bool {
        let first = self.mark_failed(why);
        if first {
            warn!(lane = self.key, why, "RDMA lane failed");
        }
        first
    }

    /// [`Self::fail`] without the log line (the engine stopping fails
    /// every lane and logs once for all of them).
    fn mark_failed(&self, why: &str) -> bool {
        {
            let mut r = self.reason.lock();
            if r.is_some() {
                return false;
            }
            *r = Some(why.to_string());
        }
        self.failed.store(true, Ordering::Release);
        self.sq.close();
        true
    }

    /// The next control message received on this lane: (buffer index,
    /// length). `deadline: None` waits until one arrives or the lane is
    /// torn down (its RECVs then complete with a flush error).
    /// `reply_owed`: a reply to a request already sent is due by
    /// `deadline`. If it does not come, a late one could still arrive and
    /// be taken as the reply to the *next* request, so the lane fails.
    pub fn recv(
        &self,
        deadline: Option<Instant>,
        reply_owed: bool,
    ) -> Result<(usize, usize), String> {
        if self.failed() {
            return Err(self.failed_error());
        }
        let wc = match self.recvq.pop(deadline) {
            Ok(wc) => wc,
            Err(RecvError::Timeout) => {
                let msg = "poll timeout (no RECV completion)";
                if reply_owed {
                    self.fail("control reply timed out; a late reply would desynchronize the lane");
                }
                return Err(msg.into());
            }
            Err(RecvError::Closed) => {
                self.fail("receive queue closed");
                return Err(format!("receive queue closed: {}", self.failed_error()));
            }
        };
        if wc.status != ffi::IBV_WC_SUCCESS {
            let e = format!(
                "WC error: opcode={} status={} (vendor_err=0x{:x})",
                wc.opcode,
                ffi::wc_status_str(wc.status),
                wc.vendor_err
            );
            self.fail(&e);
            return Err(e);
        }
        Ok((recv_index(wc.wr_id), wc.byte_len as usize))
    }

    /// The lane is being torn down (its peer removed): fail it and wake
    /// its receivers, so a thread blocked in [`Self::recv`] with no
    /// deadline returns and drops its reference to the lane, even if the
    /// stopped QP delivers no flush completion.
    pub fn shutdown(&self, why: &str) {
        self.close(why);
    }

    /// Engine stopped: fail the lane and wake its receivers.
    fn close(&self, why: &str) {
        self.mark_failed(why);
        self.recvq.close();
    }
}

/// Lane key → its state.
pub struct RecvRouter {
    lanes: RwLock<HashMap<u64, Arc<LaneState>>>,
    next_key: AtomicU64,
    unrouted: AtomicU64,
    /// Set (under the write lock) when the engine stops.
    closed: AtomicBool,
}

impl Default for RecvRouter {
    fn default() -> Self {
        Self::new()
    }
}

impl RecvRouter {
    pub fn new() -> Self {
        Self {
            lanes: RwLock::new(HashMap::new()),
            next_key: AtomicU64::new(1),
            unrouted: AtomicU64::new(0),
            closed: AtomicBool::new(false),
        }
    }

    /// A new lane with `sq_depth` send-queue credits. `None` once closed.
    pub fn register(&self, sq_depth: u64) -> Option<Arc<LaneState>> {
        let key = self.next_key.fetch_add(1, Ordering::Relaxed);
        let lane = Arc::new(LaneState::new(key, sq_depth));
        let mut lanes = self.lanes.write();
        if self.closed.load(Ordering::Acquire) {
            return None;
        }
        lanes.insert(key, Arc::clone(&lane));
        Some(lane)
    }

    pub fn unregister(&self, key: u64) -> Option<Arc<LaneState>> {
        self.lanes.write().remove(&key)
    }

    /// Hand a receive CQE to its lane. CQEs of a lane already gone are
    /// dropped (counted).
    pub fn route(&self, wc: ffi::ibv_wc) {
        let lane = self.lanes.read().get(&recv_key(wc.wr_id)).cloned();
        match lane {
            Some(l) => l.recvq.push(wc),
            None => {
                self.unrouted.fetch_add(1, Ordering::Relaxed);
            }
        }
    }

    pub fn unrouted(&self) -> u64 {
        self.unrouted.load(Ordering::Relaxed)
    }

    /// Fail every lane, wake its receivers, refuse new lanes.
    fn close(&self, why: &str) {
        let lanes = self.lanes.write();
        self.closed.store(true, Ordering::Release);
        for l in lanes.values() {
            l.close(why);
        }
    }
}

// ---------------------------------------------------------------------
// Dispatch: everything the poller routes to, with no verbs objects
// ---------------------------------------------------------------------

/// What [`Dispatch::run`] needs of a lane's QP besides posting.
pub trait SendQp {
    /// Move the QP to the error state, so every posted WR flushes.
    fn stop(&self) -> Result<(), String>;
    /// Post a zero-length signaled WR with `wr_id` (a fence on a QP in the
    /// error state; it completes after every WR posted before it).
    fn post_fence(&self, wr_id: u64) -> Result<(), i32>;
}

/// A post that `ibv_post_send` refused at WR `posted`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PostFailure {
    /// WRs posted before the refused one: they will run.
    pub posted: usize,
    pub msg: String,
}

/// A failed [`Dispatch::run`].
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct OpError {
    pub msg: String,
    /// The operation's last completion was never seen (it was abandoned):
    /// the HCA may still read or write its local buffers until the lane's
    /// QP is destroyed. False when every posted WR is known to be done, or
    /// nothing was posted.
    pub unobserved: bool,
}

impl OpError {
    fn done(msg: String) -> Self {
        Self {
            msg,
            unobserved: false,
        }
    }
    fn unobserved(msg: String) -> Self {
        Self {
            msg,
            unobserved: true,
        }
    }
}

impl std::fmt::Display for OpError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.msg)
    }
}

impl std::error::Error for OpError {}

impl From<OpError> for String {
    fn from(e: OpError) -> String {
        e.msg
    }
}

/// The engine's routing state: slots, receive routing, budgets and
/// liveness. Holds no verbs object, so it is tested with synthetic CQEs.
pub struct Dispatch {
    slots: SlotTable,
    router: RecvRouter,
    send_budget: Arc<Credits>,
    recv_budget: Arc<Credits>,
    dead: Mutex<Option<String>>,
    /// Held buffers leaked because their lane's QP could not be destroyed.
    leaked_buffers: AtomicU64,
    /// Receive-CQ budget never given back because the QP that has those
    /// receives posted could not be destroyed.
    leaked_recv: AtomicU64,
}

impl Dispatch {
    /// `slots` operations, `send_depth` WRs and `recv_depth` posted
    /// receive buffers in flight at most.
    pub fn new(slots: usize, send_depth: u64, recv_depth: u64) -> Self {
        Self {
            slots: SlotTable::new(slots),
            router: RecvRouter::new(),
            send_budget: Credits::new(send_depth),
            recv_budget: Credits::new(recv_depth),
            dead: Mutex::new(None),
            leaked_buffers: AtomicU64::new(0),
            leaked_recv: AtomicU64::new(0),
        }
    }

    /// Abandoned operations (not freed by a late completion since) and
    /// held buffers leaked because their lane's QP could not be destroyed
    /// (see [`Self::unregister_lane`]).
    pub fn leaked(&self) -> u64 {
        self.slots.leaked.load(Ordering::Relaxed) + self.leaked_buffers.load(Ordering::Relaxed)
    }

    /// Receive-CQ entries never given back (see [`Self::unregister_lane`]).
    pub fn leaked_recv_budget(&self) -> u64 {
        self.leaked_recv.load(Ordering::Relaxed)
    }

    /// Free receive-CQ budget.
    pub fn recv_budget_available(&self) -> u64 {
        self.recv_budget.available()
    }

    /// Operations waiting for completion now (abandoned ones included).
    pub fn in_flight(&self) -> usize {
        self.slots.in_use()
    }

    /// (completion CQEs of earlier slot uses, receive CQEs of gone lanes).
    pub fn stray_counts(&self) -> (u64, u64) {
        (self.slots.stale(), self.router.unrouted())
    }

    /// Free send-CQ budget.
    pub fn send_budget_available(&self) -> u64 {
        self.send_budget.available()
    }

    /// Whether the poller has stopped.
    pub fn is_dead(&self) -> bool {
        self.slots.dead.load(Ordering::Acquire)
    }

    /// Why the poller stopped.
    pub fn dead_reason(&self) -> Option<String> {
        self.dead.lock().clone()
    }

    fn dead_error(&self) -> String {
        format!(
            "completion engine stopped: {}",
            self.dead_reason().unwrap_or_else(|| "unknown".into())
        )
    }

    /// Register a lane with `sq_depth` send-queue entries that will post
    /// `recv_buffers` receive buffers. The receive CQ budget is taken now
    /// and given back by [`Self::unregister_lane`].
    pub fn register_lane(
        &self,
        sq_depth: u64,
        recv_buffers: u64,
    ) -> Result<Arc<LaneState>, String> {
        if self.is_dead() {
            return Err(self.dead_error());
        }
        if !self.recv_budget.try_acquire(recv_buffers) {
            return Err(format!(
                "receive CQ full ({} buffers posted)",
                self.recv_budget.capacity() - self.recv_budget.available()
            ));
        }
        match self.router.register(sq_depth) {
            Some(l) => Ok(l),
            None => {
                self.recv_budget.release(recv_buffers);
                Err(self.dead_error())
            }
        }
    }

    /// Unregister lane `key` after its QP is gone: stop routing its
    /// receives. If the QP was destroyed (`qp_destroyed`), give back its
    /// receive budget, free the slots and credits of its abandoned
    /// operations (no CQE can come for them any more) and drop the buffers
    /// it held; returns how many abandoned operations were freed. If
    /// destroying the QP failed, the HCA may still use all of it, and its
    /// receives stay posted on the shared receive CQ: the receive budget,
    /// the abandoned operations and the held buffers are leaked, counted
    /// ([`Self::leaked`], [`Self::leaked_recv_budget`]) and logged.
    pub fn unregister_lane(&self, key: u64, recv_buffers: u64, qp_destroyed: bool) -> usize {
        let lane = self.router.unregister(key);
        if qp_destroyed {
            self.recv_budget.release(recv_buffers);
        } else {
            self.leaked_recv.fetch_add(recv_buffers, Ordering::Relaxed);
        }
        let Some(lane) = lane else { return 0 };
        let (abandoned, held) = {
            let mut l = lane.leftovers.lock();
            l.released = Some(qp_destroyed);
            (
                std::mem::take(&mut l.abandoned),
                std::mem::take(&mut l.held),
            )
        };
        if !qp_destroyed {
            let ops = self.slots.mark_leaked(&abandoned);
            self.leaked_buffers
                .fetch_add(held.len() as u64, Ordering::Relaxed);
            error!(lane = key, operations = ops, buffers = held.len(), recv_buffers,
                   "QP could not be destroyed; leaking its receive budget, abandoned operations and held buffers");
            for b in held {
                std::mem::forget(b);
            }
            return 0;
        }
        let swept = self.slots.sweep(&abandoned);
        if swept > 0 || !held.is_empty() {
            debug!(
                lane = key,
                swept,
                buffers = held.len(),
                "freed what a destroyed lane held"
            );
        }
        drop(held);
        swept
    }

    /// Reserve `wrs` send-queue entries of `lane` and as many send CQ
    /// entries, then a slot for the calling thread, within `bound`.
    pub fn begin(&self, lane: &LaneState, wrs: u64, bound: &mut Bound) -> Result<Ticket, String> {
        lane.sq
            .acquire(wrs, bound)
            .map_err(|e| self.why_refused(lane, "send queue", e))?;
        if let Err(e) = self.send_budget.acquire(wrs, bound) {
            lane.sq.release(wrs);
            return Err(self.why_refused(lane, "send CQ", e));
        }
        let grant = Grant {
            from: vec![
                (Arc::clone(&lane.sq), wrs),
                (Arc::clone(&self.send_budget), wrs),
            ],
        };
        self.slots.alloc_wait(grant, bound).map_err(|g| {
            g.release();
            self.why_refused(
                lane,
                "slot",
                "no completion slot free within the deadline".into(),
            )
        })
    }

    fn why_refused(&self, lane: &LaneState, what: &str, e: String) -> String {
        if self.is_dead() {
            self.dead_error()
        } else if lane.failed() {
            lane.failed_error()
        } else {
            format!("{what}: {e}")
        }
    }

    /// Wait for `t` within `bound`.
    pub fn wait(&self, t: Ticket, bound: &mut Bound) -> Outcome {
        self.slots.wait(t, bound)
    }

    /// Stop waiting for `t` of `lane`; its slot and credits come back
    /// when its tail completion arrives or the lane is unregistered.
    pub fn abandon(&self, lane: &LaneState, t: Ticket) {
        if self.slots.abandon(t) {
            let mut l = lane.leftovers.lock();
            if l.released.is_none() {
                l.abandoned.push(t);
            }
        }
    }

    /// The operation could not be posted at all: release `t` now.
    pub fn cancel(&self, t: Ticket) {
        // Complete it as if its tail had come back, then free it.
        self.slots.complete(t.wr_id(true), None);
        let _ = self.slots.wait(t, &mut Bound::new(Duration::ZERO));
    }

    /// Route a send-CQ completion.
    pub fn complete_send(&self, wr_id: u64, error: Option<WcError>) {
        self.slots.complete(wr_id, error);
    }

    /// Route a receive-CQ completion.
    pub fn route_recv(&self, wc: ffi::ibv_wc) {
        self.router.route(wc);
    }

    /// The poller has stopped (`why`): no completion will come. Wake and
    /// fail every waiter, close every receive queue, fail every lane, and
    /// make every later call fail at once. Logs once.
    pub fn kill(&self, why: &str) {
        {
            let mut d = self.dead.lock();
            if d.is_some() {
                return;
            }
            *d = Some(why.to_string());
        }
        error!(
            why,
            in_flight = self.in_flight(),
            "RDMA completion poller stopped; failing every waiter and lane"
        );
        self.slots.kill();
        self.send_budget.close();
        self.recv_budget.close();
        self.router
            .close(&format!("completion engine stopped: {why}"));
    }

    /// Run one send-side operation of `wrs` WRs on `lane`: take credits
    /// (within `limits.credits`; on expiry nothing is posted and the QP is
    /// not touched), `post` it with the slot's ticket, and wait for its
    /// completion (within `limits.completion` from the post). Returns once
    /// the hardware is done with the local buffers, on success and on
    /// error alike, except when even stopping the QP brings no completion
    /// (then the error says so; the slot is abandoned and freed when the
    /// lane is unregistered, after its QP is destroyed).
    ///
    /// A failed completion or a timeout fails the lane (see
    /// [`LaneState::fail`]); a failed lane refuses every later operation.
    /// An error with [`OpError::unobserved`] set means the operation's
    /// last completion was never seen: the HCA may still use its local
    /// buffers, which must not be reused until the lane's QP is destroyed
    /// ([`LaneState::hold`]).
    pub fn run<Q: SendQp + ?Sized>(
        &self,
        lane: &LaneState,
        qp: &Q,
        wrs: u64,
        limits: &Limits,
        post: impl FnOnce(Ticket) -> Result<(), PostFailure>,
    ) -> Result<(), OpError> {
        if lane.failed() {
            return Err(OpError::done(lane.failed_error()));
        }
        let t = self
            .begin(lane, wrs, &mut Bound::new(limits.credits))
            .map_err(OpError::done)?;
        match post(t) {
            Ok(()) => match self.settle(lane, qp, t, limits) {
                Ok(None) => Ok(()),
                Ok(Some(e)) => Err(OpError::done(e)),
                Err(e) => Err(OpError::unobserved(e)),
            },
            Err(e) if e.posted == 0 => {
                self.cancel(t);
                Err(OpError::done(e.msg))
            }
            Err(e) => {
                // The WRs before the refused one are posted, with no tail
                // behind them: stop the QP and post the tail as a fence;
                // its flush completion comes after theirs.
                lane.fail(&format!("partial post: {}", e.msg));
                if let Err(f) = self.fence(qp, t, limits) {
                    // Nothing will complete the slot: give it up. Its
                    // credits come back when the lane is unregistered.
                    self.abandon(lane, t);
                    warn!(error = %f, "partial post: no fence; work requests may still be running");
                    return Err(OpError::unobserved(format!(
                        "{}; {f}; posted work requests may still be running",
                        e.msg
                    )));
                }
                match self.settle(lane, qp, t, limits) {
                    Ok(_) => Err(OpError::done(format!(
                        "{}; QP stopped, posted work requests finished",
                        e.msg
                    ))),
                    Err(m) => Err(OpError::unobserved(format!("{}; {m}", e.msg))),
                }
            }
        }
    }

    /// Move the QP to the error state and post `t`'s tail as a fence.
    fn fence<Q: SendQp + ?Sized>(&self, qp: &Q, t: Ticket, limits: &Limits) -> Result<(), String> {
        qp.stop()
            .map_err(|e| format!("could not stop the QP ({e})"))?;
        let mut bound = Bound::new(limits.completion);
        loop {
            match qp.post_fence(t.wr_id(true)) {
                Ok(()) => return Ok(()),
                Err(code) if Instant::now() < bound.deadline() => {
                    debug!(code, "fence post refused; retrying while the QP flushes");
                    thread::sleep(Duration::from_millis(1));
                }
                Err(code) => return Err(format!("QP stopped, fence post failed ({code})")),
            }
        }
    }

    /// Wait for `t`: `Ok` once every WR of it has completed (`Some`: the
    /// first that failed, and the lane is failed). If it does not complete
    /// within `limits.completion`, fail the lane, stop the QP so its WRs
    /// flush, and wait for that, so no buffer is released while the NIC may
    /// still use it; `Err` only if even that brings no completion (the
    /// slot is then abandoned).
    fn settle<Q: SendQp + ?Sized>(
        &self,
        lane: &LaneState,
        qp: &Q,
        t: Ticket,
        limits: &Limits,
    ) -> Result<Option<String>, String> {
        let timeout = limits.completion;
        match self.wait(t, &mut Bound::new(timeout)) {
            Outcome::Done(None) => Ok(None),
            Outcome::Done(Some(e)) => {
                let e = e.to_string();
                lane.fail(&e);
                Ok(Some(e))
            }
            Outcome::Pending => {
                lane.fail(&format!("no completion within {timeout:?}"));
                let stopped = qp.stop();
                match self.wait(t, &mut Bound::new(timeout)) {
                    Outcome::Done(_) => Ok(Some(format!(
                        "no completion within {timeout:?}; QP stopped, work requests finished"
                    ))),
                    Outcome::Pending => {
                        self.abandon(lane, t);
                        warn!(?stopped, "no completion even after stopping the QP");
                        Err(format!(
                            "no completion within {timeout:?}, none after stopping the QP \
                                     ({stopped:?}); posted work requests may still be running"
                        ))
                    }
                    Outcome::Dead => {
                        self.abandon(lane, t);
                        Err(self.dead_error())
                    }
                }
            }
            Outcome::Dead => {
                // No completion will ever come: stop the QP so the HCA
                // stops working on it, and leave the slot to the lane
                // sweep.
                let _ = qp.stop();
                self.abandon(lane, t);
                Err(self.dead_error())
            }
        }
    }
}

// ---------------------------------------------------------------------
// Engine
// ---------------------------------------------------------------------

/// Owns the channel; destroyed after both CQs (field order in `Inner`).
struct Channel(*mut ffi::ibv_comp_channel);

// SAFETY: the channel is used only through libibverbs calls, which are
// thread-safe; the poller is the only reader of its fd.
unsafe impl Send for Channel {}
unsafe impl Sync for Channel {}

impl Drop for Channel {
    fn drop(&mut self) {
        // SAFETY: created by ibv_create_comp_channel; every CQ on it is
        // destroyed by now (they are dropped before this field).
        unsafe { ffi::ibv_destroy_comp_channel(self.0) };
    }
}

struct Inner {
    // Drop order: CQs (after their events are acknowledged), then the
    // channel, then the device.
    send_cq: CompletionQueue,
    recv_cq: CompletionQueue,
    channel: Channel,
    _device: Arc<RdmaDevice>,
    dispatch: Dispatch,
    shutdown: AtomicBool,
    /// Written on shutdown to wake the poller from `poll(2)`.
    wake_fd: libc::c_int,
    /// CQ events taken from the channel and not yet acknowledged, per CQ.
    unacked: Mutex<HashMap<usize, u32>>,
}

/// Engines created in this process: spreads them over completion vectors.
static NEXT_ENGINE: AtomicUsize = AtomicUsize::new(0);

/// Completion vector of the `index`th engine on a device with `vectors`.
pub fn comp_vector_for(index: usize, vectors: u32) -> i32 {
    (index % vectors.max(1) as usize) as i32
}

/// The completion engine of one transport. Lanes hold it (`Arc`), so its
/// CQs outlive every QP attached to them.
pub struct CompletionEngine {
    inner: Arc<Inner>,
    poller: Mutex<Option<thread::JoinHandle<()>>>,
    comp_vector: i32,
}

impl CompletionEngine {
    /// Engine with a send CQ of `send_depth` entries (the most WRs in
    /// flight on all lanes together) and a receive CQ of `recv_depth`
    /// (the most receive buffers posted on all lanes together). `name`
    /// names the poller thread.
    pub fn new(
        device: &Arc<RdmaDevice>,
        send_depth: u32,
        recv_depth: u32,
        name: &str,
    ) -> Result<Arc<Self>, String> {
        let max_cqe = u32::try_from(device.attrs.max_cqe).unwrap_or(0).max(1);
        let send_depth = send_depth.clamp(1, max_cqe);
        let recv_depth = recv_depth.clamp(1, max_cqe);
        // Each engine raises its interrupts on its own vector (and so,
        // with the usual IRQ affinity, its own CPU), not all on vector 0.
        let comp_vector = comp_vector_for(
            NEXT_ENGINE.fetch_add(1, Ordering::Relaxed),
            device.num_comp_vectors(),
        );
        // SAFETY: valid device context.
        let ch = unsafe { ffi::ibv_create_comp_channel(device.context()) };
        if ch.is_null() {
            return Err(format!(
                "ibv_create_comp_channel: {}",
                std::io::Error::last_os_error()
            ));
        }
        let channel = Channel(ch);
        // SAFETY: `ch` is a valid channel; its fd is ours to configure.
        let fd = unsafe { (*ch).fd };
        set_nonblocking(fd)?;
        // SAFETY: `ch` is this device's channel; `Inner` drops the CQs
        // before the channel (field order).
        let send_cq =
            unsafe { CompletionQueue::with_channel(device, send_depth as i32, ch, comp_vector)? };
        let recv_cq =
            unsafe { CompletionQueue::with_channel(device, recv_depth as i32, ch, comp_vector)? };
        // SAFETY: plain eventfd.
        let wake_fd = unsafe { libc::eventfd(0, libc::EFD_NONBLOCK | libc::EFD_CLOEXEC) };
        if wake_fd < 0 {
            return Err(format!("eventfd: {}", std::io::Error::last_os_error()));
        }
        let inner = Arc::new(Inner {
            send_cq,
            recv_cq,
            channel,
            _device: Arc::clone(device),
            // Operations, not WRs: a chain is one operation of up to 128 WRs.
            dispatch: Dispatch::new(
                (send_depth as usize).min(MAX_SLOTS),
                u64::from(send_depth),
                u64::from(recv_depth),
            ),
            shutdown: AtomicBool::new(false),
            wake_fd,
            unacked: Mutex::new(HashMap::new()),
        });
        let for_poller = Arc::clone(&inner);
        let poller = thread::Builder::new()
            .name(name.into())
            .spawn(move || {
                let inner = &*for_poller;
                supervise(&inner.dispatch, &inner.shutdown, || poll_loop(inner));
            })
            .map_err(|e| format!("spawn completion poller: {e}"))?;
        Ok(Arc::new(Self {
            inner,
            poller: Mutex::new(Some(poller)),
            comp_vector,
        }))
    }

    pub fn send_cq(&self) -> &CompletionQueue {
        &self.inner.send_cq
    }
    pub fn recv_cq(&self) -> &CompletionQueue {
        &self.inner.recv_cq
    }

    /// The completion vector its CQs signal on.
    pub fn comp_vector(&self) -> i32 {
        self.comp_vector
    }

    /// Its routing state.
    pub fn dispatch(&self) -> &Dispatch {
        &self.inner.dispatch
    }

    /// Operations waiting for completion now.
    pub fn in_flight(&self) -> usize {
        self.inner.dispatch.in_flight()
    }

    /// Whether its poller has stopped.
    pub fn is_dead(&self) -> bool {
        self.inner.dispatch.is_dead()
    }

    /// (completion CQEs of earlier slot uses, receive CQEs of gone lanes).
    pub fn stray_counts(&self) -> (u64, u64) {
        self.inner.dispatch.stray_counts()
    }
}

impl Drop for CompletionEngine {
    fn drop(&mut self) {
        self.inner.shutdown.store(true, Ordering::SeqCst);
        let one: u64 = 1;
        // SAFETY: writing 8 bytes to our eventfd.
        unsafe { libc::write(self.inner.wake_fd, (&one as *const u64).cast(), 8) };
        if let Some(h) = self.poller.lock().take() {
            if h.join().is_err() {
                warn!("completion poller panicked");
            }
        }
        // Acknowledge every event taken from the channel, as libibverbs
        // requires before the CQs are destroyed.
        for (&cq, &n) in self.inner.unacked.lock().iter() {
            if n > 0 {
                // SAFETY: `cq` is one of our live CQs.
                unsafe { ffi::ibv_ack_cq_events(cq as *mut ffi::ibv_cq, n) };
            }
        }
        // SAFETY: our eventfd; nothing uses it after the poller exits.
        unsafe { libc::close(self.inner.wake_fd) };
    }
}

/// Run the poller `body` (which returns why it stopped) and, unless the
/// engine is shutting down, kill `dispatch` when it returns or panics, so
/// no waiter is left parked on completions that will never be routed.
fn supervise(dispatch: &Dispatch, shutdown: &AtomicBool, body: impl FnOnce() -> String) {
    let why = match std::panic::catch_unwind(std::panic::AssertUnwindSafe(body)) {
        Ok(why) => why,
        Err(_) => "completion poller panicked".to_string(),
    };
    if !shutdown.load(Ordering::SeqCst) {
        dispatch.kill(&why);
    }
}

/// A fault `poll(2)` reported on a descriptor the poller waits on: the
/// descriptor will never become readable, so polling it again would spin.
fn fd_fault(revents: libc::c_short) -> Option<&'static str> {
    if revents & libc::POLLNVAL != 0 {
        Some("descriptor is not open (POLLNVAL)")
    } else if revents & libc::POLLERR != 0 {
        Some("descriptor reports an error (POLLERR)")
    } else if revents & libc::POLLHUP != 0 {
        Some("descriptor hung up (POLLHUP)")
    } else {
        None
    }
}

fn set_nonblocking(fd: libc::c_int) -> Result<(), String> {
    // SAFETY: fcntl on a valid fd.
    unsafe {
        let flags = libc::fcntl(fd, libc::F_GETFL);
        if flags < 0 || libc::fcntl(fd, libc::F_SETFL, flags | libc::O_NONBLOCK) < 0 {
            return Err(format!(
                "completion channel fd O_NONBLOCK: {}",
                std::io::Error::last_os_error()
            ));
        }
    }
    Ok(())
}

/// Drain one CQ: route every CQE. Returns how many there were; `Err` if
/// polling failed (the CQ is unusable).
fn drain(
    inner: &Inner,
    cq: &CompletionQueue,
    send: bool,
    wcs: &mut [ffi::ibv_wc; POLL_BATCH],
) -> Result<usize, i32> {
    let mut total = 0;
    loop {
        let n = cq.poll(&mut wcs[..]);
        if n < 0 {
            return Err(n);
        }
        let n = n as usize;
        for wc in &wcs[..n] {
            if send {
                inner.dispatch.complete_send(wc.wr_id, wc_error(wc));
            } else {
                inner.dispatch.route_recv(*wc);
            }
        }
        total += n;
        if n < POLL_BATCH {
            return Ok(total);
        }
    }
}

/// Drain both CQs.
fn drain_both(inner: &Inner, wcs: &mut [ffi::ibv_wc; POLL_BATCH]) -> Result<usize, String> {
    let a = drain(inner, &inner.send_cq, true, wcs)
        .map_err(|e| format!("ibv_poll_cq on the send CQ failed ({e})"))?;
    let b = drain(inner, &inner.recv_cq, false, wcs)
        .map_err(|e| format!("ibv_poll_cq on the receive CQ failed ({e})"))?;
    Ok(a + b)
}

/// The poller. Returns why it stopped.
fn poll_loop(inner: &Inner) -> String {
    // SAFETY: ibv_wc is plain data.
    let mut wcs: [ffi::ibv_wc; POLL_BATCH] = unsafe { ffi::zeroed() };
    let mut last_cqe = Instant::now();
    let ch_fd = {
        // SAFETY: the channel lives as long as `inner`.
        unsafe { (*inner.channel.0).fd }
    };
    // Both CQs start unarmed; the first idle pass arms them.
    while !inner.shutdown.load(Ordering::Relaxed) {
        let got = match drain_both(inner, &mut wcs) {
            Ok(n) => n,
            Err(e) => return e,
        };
        if got > 0 {
            last_cqe = Instant::now();
            continue;
        }
        if inner.dispatch.in_flight() > 0 && last_cqe.elapsed() < SPIN_WINDOW {
            std::hint::spin_loop();
            continue;
        }
        // Arm both CQs, then poll once more: a CQE that landed before the
        // arm raises no event and would otherwise wait for the next one.
        // SAFETY: our live CQs.
        let armed = unsafe {
            ffi::ibv_req_notify_cq(inner.send_cq.as_ptr(), 0) == 0
                && ffi::ibv_req_notify_cq(inner.recv_cq.as_ptr(), 0) == 0
        };
        if !armed {
            warn!("ibv_req_notify_cq failed; completion poller falls back to polling");
            thread::sleep(Duration::from_micros(100));
            continue;
        }
        match drain_both(inner, &mut wcs) {
            Ok(0) => {}
            Ok(_) => {
                last_cqe = Instant::now();
                continue;
            }
            Err(e) => return e,
        }
        let mut fds = [
            libc::pollfd {
                fd: ch_fd,
                events: libc::POLLIN,
                revents: 0,
            },
            libc::pollfd {
                fd: inner.wake_fd,
                events: libc::POLLIN,
                revents: 0,
            },
        ];
        // SAFETY: two valid pollfds.
        let r = unsafe { libc::poll(fds.as_mut_ptr(), 2, -1) };
        if r < 0 {
            let e = std::io::Error::last_os_error();
            if e.kind() != std::io::ErrorKind::Interrupted {
                warn!(error = %e, "poll on the completion channel failed");
                thread::sleep(Duration::from_millis(1));
            }
            continue;
        }
        if let Some(f) = fd_fault(fds[0].revents) {
            return format!("completion channel: {f}");
        }
        if let Some(f) = fd_fault(fds[1].revents) {
            if inner.shutdown.load(Ordering::Relaxed) {
                break;
            }
            return format!("poller wake eventfd: {f}");
        }
        take_events(inner);
    }
    "shutdown".to_string()
}

/// Take every pending event off the channel, acknowledging them in
/// batches.
fn take_events(inner: &Inner) {
    loop {
        let mut cq: *mut ffi::ibv_cq = std::ptr::null_mut();
        let mut ctx: *mut libc::c_void = std::ptr::null_mut();
        // SAFETY: valid channel; the fd is non-blocking, so this returns -1
        // when no event is pending.
        if unsafe { ffi::ibv_get_cq_event(inner.channel.0, &mut cq, &mut ctx) } != 0 {
            return;
        }
        let mut unacked = inner.unacked.lock();
        let n = unacked.entry(cq as usize).or_insert(0);
        *n += 1;
        if *n >= ACK_BATCH {
            // SAFETY: `cq` came from our channel, so it is one of our CQs.
            unsafe { ffi::ibv_ack_cq_events(cq, *n) };
            *n = 0;
        }
        debug!("completion event");
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn far() -> Bound {
        Bound::new(Duration::from_secs(5))
    }
    fn soon() -> Bound {
        Bound::new(Duration::from_millis(20))
    }

    #[test]
    fn credits_block_and_release() {
        let c = Credits::new(4);
        c.acquire(3, &mut far()).expect("3 of 4");
        assert_eq!(c.available(), 1);
        assert!(!c.try_acquire(2));
        assert!(c.acquire(2, &mut soon()).is_err(), "only 1 free");
        assert!(
            c.acquire(5, &mut far()).is_err(),
            "over capacity fails at once"
        );
        let c2 = Arc::clone(&c);
        let h = thread::spawn(move || c2.acquire(4, &mut far()));
        thread::sleep(Duration::from_millis(20));
        c.release(3);
        h.join().expect("join").expect("all 4 once released");
        assert_eq!(c.available(), 0);
        c.release(10);
        assert_eq!(c.available(), 4, "release never exceeds capacity");
    }

    fn wait_for(cond: impl Fn() -> bool, what: &str) {
        let deadline = Instant::now() + Duration::from_secs(5);
        while !cond() {
            assert!(Instant::now() < deadline, "timed out waiting for {what}");
            thread::sleep(Duration::from_millis(1));
        }
    }

    // Credits are granted in arrival order. A large
    // request queued behind a held credit is not overtaken by a later
    // small one, though the small one would fit; a waiter that times out
    // leaves the line and the next one is served.
    #[test]
    fn credits_are_fifo_so_a_large_request_is_not_starved() {
        let c = Credits::new(128);
        c.acquire(1, &mut far()).expect("hold one");
        let big = {
            let c = Arc::clone(&c);
            thread::spawn(move || c.acquire(128, &mut far()))
        };
        wait_for(|| c.waiting() == 1, "the large request to queue");
        // 127 are free: without FIFO order this small request would take
        // one and the large request could wait forever behind a stream of
        // them.
        assert!(
            c.acquire(1, &mut soon()).is_err(),
            "a later small request must queue behind the large one"
        );
        assert!(
            !c.try_acquire(1),
            "try_acquire does not jump the line either"
        );
        c.release(1);
        big.join()
            .expect("join")
            .expect("large request served once the credit is back");
        assert_eq!(c.available(), 0);
        c.release(128);

        // A waiter that times out leaves the line; the next is served.
        c.acquire(100, &mut far()).expect("hold 100");
        let timed_out = {
            let c = Arc::clone(&c);
            thread::spawn(move || c.acquire(128, &mut Bound::new(Duration::from_millis(50))))
        };
        wait_for(|| c.waiting() == 1, "the doomed request to queue");
        let small = {
            let c = Arc::clone(&c);
            thread::spawn(move || c.acquire(10, &mut far()))
        };
        assert!(timed_out.join().expect("join").is_err());
        small
            .join()
            .expect("join")
            .expect("served after the head left");
        assert_eq!((c.available(), c.waiting()), (18, 0));
    }

    // Only the head is
    // woken on a release, so each grant must wake the next head the
    // remaining credits fit. Release all at once: every waiter is served
    // promptly, not stranded until its bound.
    #[test]
    fn one_release_serves_every_waiter_it_covers() {
        let c = Credits::new(4);
        c.acquire(4, &mut far()).expect("hold all");
        let (tx, rx) = std::sync::mpsc::channel();
        let ws: Vec<_> = (0..4)
            .map(|_| {
                let (c, tx) = (Arc::clone(&c), tx.clone());
                thread::spawn(move || {
                    let _ = tx.send(
                        c.acquire(1, &mut Bound::new(Duration::from_secs(30)))
                            .is_ok(),
                    );
                })
            })
            .collect();
        wait_for(|| c.waiting() == 4, "four waiters");
        c.release(4);
        for _ in 0..4 {
            assert!(rx
                .recv_timeout(Duration::from_secs(2))
                .expect("served promptly"));
        }
        for w in ws {
            w.join().expect("join");
        }
        assert_eq!(c.available(), 0);
    }

    #[test]
    fn closing_credits_fails_waiters_at_once() {
        let c = Credits::new(1);
        c.acquire(1, &mut far()).expect("hold");
        let w = {
            let c = Arc::clone(&c);
            thread::spawn(move || {
                let t = Instant::now();
                (
                    c.acquire(1, &mut Bound::new(Duration::from_secs(30))),
                    t.elapsed(),
                )
            })
        };
        wait_for(|| c.waiting() == 1, "waiter");
        c.close();
        let (r, took) = w.join().expect("join");
        assert!(r.is_err());
        assert!(took < Duration::from_secs(2));
        assert!(c.acquire(0, &mut far()).is_err(), "closed for good");
    }

    fn grant(c: &Arc<Credits>, n: u64) -> Grant {
        c.acquire(n, &mut far()).expect("credits");
        Grant {
            from: vec![(Arc::clone(c), n)],
        }
    }

    #[test]
    fn wr_ids_carry_slot_generation_and_tail() {
        let t = Ticket {
            index: 7,
            generation: 3,
        };
        assert_eq!(t.wr_id(false), (3 << 32) | 7);
        assert_eq!(t.wr_id(true), TAIL_BIT | (3 << 32) | 7);
        let big = Ticket {
            index: u32::MAX,
            generation: 0x7fff_ffff,
        };
        assert_eq!(
            big.wr_id(true) & !TAIL_BIT,
            (0x7fff_ffffu64 << 32) | u64::from(u32::MAX)
        );
    }

    #[test]
    fn a_slot_completes_on_its_tail_with_the_first_error() {
        let table = SlotTable::new(4);
        let c = Credits::new(8);
        let t = table.alloc_wait(grant(&c, 3), &mut far()).expect("slot");
        assert_eq!(table.in_use(), 1);
        // A non-tail success, then a failure, then a flushed tail.
        table.complete(t.wr_id(false), None);
        let bad = WcError {
            status: 12,
            opcode: 0,
            vendor_err: 0x81,
        };
        table.complete(t.wr_id(false), Some(bad));
        assert_eq!(
            table.wait(t, &mut Bound::new(Duration::ZERO)),
            Outcome::Pending,
            "no tail yet"
        );
        table.complete(
            t.wr_id(true),
            Some(WcError {
                status: ffi::IBV_WC_WR_FLUSH_ERR,
                opcode: 0,
                vendor_err: 0,
            }),
        );
        assert_eq!(c.available(), 8, "credits back on the tail");
        assert_eq!(
            table.wait(t, &mut far()),
            Outcome::Done(Some(bad)),
            "first error wins"
        );
        assert_eq!(table.in_use(), 0);
        // The slot is reused with a new generation; the old id is stale.
        let t2 = table.alloc_wait(grant(&c, 1), &mut far()).expect("slot");
        table.complete(t.wr_id(true), None);
        assert_eq!(table.stale(), 1);
        table.complete(t2.wr_id(true), None);
        assert_eq!(table.wait(t2, &mut far()), Outcome::Done(None));
    }

    #[test]
    fn a_waiter_is_woken_by_another_thread() {
        let table = Arc::new(SlotTable::new(2));
        let c = Credits::new(1);
        let t = table.alloc_wait(grant(&c, 1), &mut far()).expect("slot");
        let poller = {
            let table = Arc::clone(&table);
            thread::spawn(move || {
                thread::sleep(Duration::from_millis(30));
                table.complete(t.wr_id(true), None);
            })
        };
        let started = Instant::now();
        assert_eq!(table.wait(t, &mut far()), Outcome::Done(None));
        assert!(started.elapsed() < Duration::from_secs(2));
        poller.join().expect("join");
    }

    #[test]
    fn abandoned_slots_are_freed_by_their_completion() {
        let table = SlotTable::new(1);
        let c = Credits::new(2);
        let t = table.alloc_wait(grant(&c, 2), &mut far()).expect("slot");
        table.abandon(t);
        assert_eq!(table.in_use(), 1, "still in flight");
        assert!(
            table
                .alloc_wait(grant(&Credits::new(1), 1), &mut soon())
                .is_err(),
            "no free slot meanwhile"
        );
        table.complete(t.wr_id(true), None);
        assert_eq!(table.in_use(), 0);
        assert_eq!(c.available(), 2);
        // Abandoning one that already completed frees it at once.
        let t = table.alloc_wait(grant(&c, 1), &mut far()).expect("slot");
        table.complete(t.wr_id(true), None);
        table.abandon(t);
        assert_eq!(table.in_use(), 0);
    }

    #[test]
    fn full_table_returns_the_grant() {
        let table = SlotTable::new(1);
        let c = Credits::new(4);
        let _t = table.alloc_wait(grant(&c, 1), &mut far()).expect("slot");
        let back = table
            .alloc_wait(grant(&c, 2), &mut soon())
            .expect_err("full");
        back.release();
        assert_eq!(c.available(), 3);
    }

    fn wc_for(wr_id: u64, byte_len: u32) -> ffi::ibv_wc {
        // SAFETY: plain data.
        let mut wc: ffi::ibv_wc = unsafe { ffi::zeroed() };
        wc.wr_id = wr_id;
        wc.byte_len = byte_len;
        wc
    }

    #[test]
    fn receives_are_routed_to_their_lane() {
        let r = RecvRouter::new();
        let la = r.register(4).expect("lane a");
        let lb = r.register(4).expect("lane b");
        let (a, b) = (la.key(), lb.key());
        assert_ne!(a, b);
        r.route(wc_for(recv_wr_id(b, 5), 99));
        assert_eq!(
            la.recvq.pop(Some(Instant::now())).err(),
            Some(RecvError::Timeout)
        );
        let got = lb
            .recvq
            .pop(Some(Instant::now() + Duration::from_secs(5)))
            .expect("routed to b");
        assert_eq!((recv_index(got.wr_id), got.byte_len), (5, 99));
        r.unregister(a);
        r.route(wc_for(recv_wr_id(a, 1), 0));
        assert_eq!(r.unrouted(), 1);
        // A blocked pop wakes on a push from another thread.
        let lb2 = Arc::clone(&lb);
        let h = thread::spawn(move || lb2.recvq.pop(None).map(|w| recv_index(w.wr_id)));
        thread::sleep(Duration::from_millis(20));
        r.route(wc_for(recv_wr_id(b, 7), 0));
        assert_eq!(h.join().expect("join"), Ok(7));
        assert!(
            recv_index(recv_wr_id(3, (1 << RECV_INDEX_BITS) - 1)) == (1 << RECV_INDEX_BITS) - 1
        );
    }

    // ---- Dispatch::run with a synthetic QP and poller ----

    /// A QP that counts `stop` calls and, when `flush_on_stop`, flushes
    /// the tickets it was told about (as a QP in ERR would).
    struct FakeQp {
        d: Arc<Dispatch>,
        stops: AtomicUsize,
        flush_on_stop: bool,
        fence_ok: bool,
        posted: Mutex<Vec<Ticket>>,
    }

    impl FakeQp {
        fn new(d: &Arc<Dispatch>, flush_on_stop: bool, fence_ok: bool) -> Self {
            Self {
                d: Arc::clone(d),
                stops: AtomicUsize::new(0),
                flush_on_stop,
                fence_ok,
                posted: Mutex::new(Vec::new()),
            }
        }
        fn stops(&self) -> usize {
            self.stops.load(Ordering::SeqCst)
        }
    }

    const FLUSH: Option<WcError> = Some(WcError {
        status: ffi::IBV_WC_WR_FLUSH_ERR,
        opcode: 0,
        vendor_err: 0,
    });

    impl SendQp for FakeQp {
        fn stop(&self) -> Result<(), String> {
            self.stops.fetch_add(1, Ordering::SeqCst);
            if self.flush_on_stop {
                for t in self.posted.lock().drain(..) {
                    self.d.complete_send(t.wr_id(true), FLUSH);
                }
            }
            Ok(())
        }
        fn post_fence(&self, wr_id: u64) -> Result<(), i32> {
            if !self.fence_ok {
                return Err(-22);
            }
            if self.flush_on_stop {
                self.d.complete_send(wr_id, FLUSH);
            }
            Ok(())
        }
    }

    fn dispatch() -> Arc<Dispatch> {
        Arc::new(Dispatch::new(64, 1024, 1024))
    }

    /// Complete `t` successfully after `after`, from another thread.
    fn complete_later(d: &Arc<Dispatch>, t: Ticket, after: Duration) {
        let d = Arc::clone(d);
        thread::spawn(move || {
            thread::sleep(after);
            d.complete_send(t.wr_id(true), None);
        });
    }

    const MS: fn(u64) -> Duration = Duration::from_millis;

    #[test]
    fn run_succeeds_and_returns_every_credit() {
        let d = dispatch();
        let lane = d.register_lane(4, 0).expect("lane");
        let qp = FakeQp::new(&d, true, true);
        let limits = Limits {
            credits: MS(500),
            completion: MS(500),
        };
        d.run(&lane, &qp, 3, &limits, |t| {
            complete_later(&d, t, MS(5));
            Ok(())
        })
        .expect("run");
        assert_eq!(
            (
                d.in_flight(),
                lane.sq().available(),
                d.send_budget_available()
            ),
            (0, 4, 1024)
        );
        assert!(!lane.failed());
        assert_eq!(qp.stops(), 0);
    }

    // The credit wait and the completion wait have
    // separate bounds. An operation that waits most of its credit bound
    // and then posts gets a full completion bound; one whose credits never
    // come fails without posting and without stopping the QP.
    #[test]
    fn credit_wait_does_not_eat_the_completion_bound() {
        let d = dispatch();
        let lane = d.register_lane(1, 0).expect("lane");
        let qp = Arc::new(FakeQp::new(&d, true, true));
        let limits = Limits {
            credits: MS(1500),
            completion: MS(1500),
        };
        // A holds the lane's only credit; its completion comes at 1000 ms.
        // (Margins of 500 ms: the host may be loaded.)
        let a = {
            let (d, lane, qp) = (Arc::clone(&d), Arc::clone(&lane), Arc::clone(&qp));
            thread::spawn(move || {
                d.run(&lane, &*qp, 1, &limits, |t| {
                    complete_later(&d, t, MS(1000));
                    Ok(())
                })
            })
        };
        wait_for(|| d.in_flight() == 1, "A to post");
        // B waits ~1000 ms for the credit, posts, and completes 1000 ms
        // later: 2000 ms after it started (past one shared 1500 ms bound),
        // inside a fresh 1500 ms completion bound.
        d.run(&lane, &*qp, 1, &limits, |t| {
            complete_later(&d, t, MS(1000));
            Ok(())
        })
        .expect("B gets a full completion bound after its credit wait");
        a.join().expect("join").expect("A");
        assert_eq!(qp.stops(), 0, "no healthy QP was stopped");
        assert!(!lane.failed());

        // C holds the credit and never completes in time for D: D fails on
        // its credit bound without posting and without touching the QP.
        let hold = d.begin(&lane, 1, &mut far()).expect("hold the credit");
        let r = d.run(
            &lane,
            &*qp,
            1,
            &Limits {
                credits: MS(50),
                completion: MS(300),
            },
            |_| panic!("must not post without credits"),
        );
        assert!(r.expect_err("credit timeout").msg.contains("send queue"));
        assert_eq!(qp.stops(), 0);
        assert!(!lane.failed(), "a credit timeout does not fail the lane");
        d.cancel(hold);
        assert_eq!(lane.sq().available(), 1);
    }

    // A timed-out operation stops the QP and fails the
    // lane; later operations fail at once, without posting or waiting.
    #[test]
    fn a_timeout_fails_the_lane_and_later_operations_fail_fast() {
        let d = dispatch();
        let lane = d.register_lane(8, 0).expect("lane");
        let qp = FakeQp::new(&d, true, true);
        let limits = Limits {
            credits: MS(100),
            completion: MS(50),
        };
        let r = d.run(&lane, &qp, 2, &limits, |t| {
            qp.posted.lock().push(t);
            Ok(())
        });
        assert!(r
            .expect_err("timeout")
            .msg
            .contains("QP stopped, work requests finished"));
        assert_eq!(qp.stops(), 1);
        assert!(lane.failed());
        assert_eq!(
            (d.in_flight(), lane.sq().available()),
            (0, 8),
            "the flush returned everything"
        );
        let started = Instant::now();
        let r = d.run(
            &lane,
            &qp,
            1,
            &Limits {
                credits: MS(5000),
                completion: MS(5000),
            },
            |_| panic!("a failed lane must not post"),
        );
        assert!(r.expect_err("failed lane").msg.contains("lane failed"));
        assert!(started.elapsed() < MS(100));
        // An error completion fails the lane too (an RC QP is then in ERR).
        let lane2 = d.register_lane(8, 0).expect("lane");
        let r = d.run(&lane2, &qp, 1, &limits, |t| {
            d.complete_send(
                t.wr_id(true),
                Some(WcError {
                    status: 10,
                    opcode: 0,
                    vendor_err: 0,
                }),
            );
            Ok(())
        });
        assert!(r.is_err());
        assert!(lane2.failed());
    }

    // An abandoned operation (no completion even after
    // stopping the QP, or a partial post with no fence) keeps its slot and
    // credits only until its lane is unregistered.
    #[test]
    fn abandoned_operations_are_swept_when_their_lane_goes() {
        let d = dispatch();
        let lane = d.register_lane(8, 2).expect("lane");
        let other = d.register_lane(8, 2).expect("other lane");
        let dead_qp = FakeQp::new(&d, false, false);
        let limits = Limits {
            credits: MS(100),
            completion: MS(20),
        };
        // No completion, even after stop.
        let e = d
            .run(&lane, &dead_qp, 3, &limits, |_| Ok(()))
            .expect_err("abandoned");
        assert!(e.msg.contains("may still be running") && e.unobserved);
        // Partial post, fence refused.
        let lane_b = d.register_lane(8, 2).expect("lane b");
        let e = d
            .run(&lane_b, &dead_qp, 4, &limits, |_| {
                Err(PostFailure {
                    posted: 2,
                    msg: "refused".into(),
                })
            })
            .expect_err("partial");
        assert!(e.msg.contains("fence post failed") && e.unobserved);
        // An operation of another lane is still in flight.
        let live = d.begin(&other, 1, &mut far()).expect("live");
        assert_eq!(d.in_flight(), 3);
        assert_eq!(d.send_budget_available(), 1024 - 8);
        assert_eq!(d.unregister_lane(lane.key(), 2, true), 1);
        assert_eq!(d.unregister_lane(lane_b.key(), 2, true), 1);
        assert_eq!(d.in_flight(), 1, "only the other lane's operation remains");
        assert_eq!(d.send_budget_available(), 1024 - 1);
        d.complete_send(live.wr_id(true), None);
        assert_eq!(d.wait(live, &mut far()), Outcome::Done(None));
        assert_eq!(d.in_flight(), 0);
    }

    // When the poller stops, every waiter is woken and
    // failed, receive queues close, and later calls fail at once.
    #[test]
    fn a_dead_engine_fails_every_waiter_and_every_later_call() {
        let d = dispatch();
        let lane = d.register_lane(1, 2).expect("lane");
        let qp = Arc::new(FakeQp::new(&d, false, true));
        let long = Limits {
            credits: Duration::from_secs(30),
            completion: Duration::from_secs(30),
        };
        // A waits for a completion, B for A's credit, C for a receive.
        let a = {
            let (d, lane, qp) = (Arc::clone(&d), Arc::clone(&lane), Arc::clone(&qp));
            thread::spawn(move || d.run(&lane, &*qp, 1, &long, |_| Ok(())))
        };
        wait_for(|| d.in_flight() == 1, "A to post");
        let b = {
            let (d, lane, qp) = (Arc::clone(&d), Arc::clone(&lane), Arc::clone(&qp));
            thread::spawn(move || d.run(&lane, &*qp, 1, &long, |_| panic!("B must not post")))
        };
        wait_for(|| lane.sq().waiting() == 1, "B to wait for credits");
        let c = {
            let lane = Arc::clone(&lane);
            thread::spawn(move || lane.recv(None, false))
        };
        thread::sleep(MS(20));
        let started = Instant::now();
        let flag = AtomicBool::new(false);
        supervise(&d, &flag, || {
            "ibv_poll_cq on the send CQ failed (-5)".to_string()
        });
        let ea = a.join().expect("join A").expect_err("A");
        assert!(
            ea.msg.contains("completion engine stopped") && ea.unobserved,
            "{ea:?}"
        );
        assert!(b.join().expect("join B").is_err());
        assert!(c
            .join()
            .expect("join C")
            .expect_err("C")
            .contains("receive queue closed"));
        assert!(
            started.elapsed() < Duration::from_secs(2),
            "woken at once, not on a timeout"
        );
        assert!(d.is_dead() && lane.failed());
        assert!(qp.stops() >= 1, "A stopped its QP");
        assert!(d
            .register_lane(1, 0)
            .expect_err("no new lanes")
            .contains("ibv_poll_cq"));
        let lane2_err = d
            .begin(&lane, 1, &mut far())
            .expect_err("no new operations");
        assert!(lane2_err.contains("completion engine stopped"));
        // A's abandoned slot goes with its lane.
        assert_eq!(d.unregister_lane(lane.key(), 2, true), 1);
        assert_eq!(d.in_flight(), 0);
    }

    /// Counts its drops.
    struct Tracked(Arc<AtomicUsize>);
    impl Drop for Tracked {
        fn drop(&mut self) {
            self.0.fetch_add(1, Ordering::SeqCst);
        }
    }

    // A buffer whose operation's last completion was
    // never seen is not reused (dropped, i.e. returned to its pool) until
    // the lane's QP is destroyed; errors with a known outcome say so.
    #[test]
    fn unobserved_buffers_are_held_until_the_qp_is_destroyed() {
        let d = dispatch();
        let lane = d.register_lane(8, 2).expect("lane");
        let limits = Limits {
            credits: MS(100),
            completion: MS(20),
        };
        // A flushed operation is observed: its buffer may be reused.
        let flushing = FakeQp::new(&d, true, true);
        let e = d
            .run(&lane, &flushing, 1, &limits, |t| {
                flushing.posted.lock().push(t);
                Ok(())
            })
            .expect_err("timeout");
        assert!(!e.unobserved, "the flush completion was seen");
        // One that never completes, even after stop, is not.
        let lane = d.register_lane(8, 2).expect("lane");
        let dead_qp = FakeQp::new(&d, false, true);
        let e = d
            .run(&lane, &dead_qp, 1, &limits, |_| Ok(()))
            .expect_err("abandoned");
        assert!(e.unobserved);
        let drops = Arc::new(AtomicUsize::new(0));
        lane.hold(Tracked(Arc::clone(&drops)));
        assert_eq!(
            (drops.load(Ordering::SeqCst), lane.held()),
            (0, 1),
            "held, not reused"
        );
        assert_eq!(d.unregister_lane(lane.key(), 2, true), 1);
        assert_eq!(
            drops.load(Ordering::SeqCst),
            1,
            "released once the QP is destroyed"
        );
        // Held after the QP is gone: nothing can touch it, dropped at once.
        lane.hold(Tracked(Arc::clone(&drops)));
        assert_eq!(drops.load(Ordering::SeqCst), 2);
        assert_eq!(d.leaked(), 0);
    }

    // If the QP could not be destroyed, its abandoned
    // operations' slots and credits and its held buffers are leaked
    // (counted), never released for reuse.
    #[test]
    fn a_lane_whose_qp_was_not_destroyed_leaks_instead_of_releasing() {
        let d = dispatch();
        let lane = d.register_lane(8, 2).expect("lane");
        assert_eq!(d.recv_budget_available(), 1024 - 2);
        let dead_qp = FakeQp::new(&d, false, true);
        let limits = Limits {
            credits: MS(100),
            completion: MS(20),
        };
        let posted = Mutex::new(None);
        assert!(
            d.run(&lane, &dead_qp, 3, &limits, |t| {
                *posted.lock() = Some(t);
                Ok(())
            })
            .expect_err("abandoned")
            .unobserved
        );
        let drops = Arc::new(AtomicUsize::new(0));
        lane.hold(Tracked(Arc::clone(&drops)));
        assert_eq!(d.unregister_lane(lane.key(), 2, false), 0);
        assert_eq!(d.in_flight(), 1, "the slot stays taken");
        assert_eq!(
            d.send_budget_available(),
            1024 - 3,
            "its send-CQ credits stay taken"
        );
        // Its receives are still posted on the shared
        // receive CQ, so their budget is not given back either.
        assert_eq!(
            d.recv_budget_available(),
            1024 - 2,
            "receive budget stays taken"
        );
        assert_eq!(d.leaked_recv_budget(), 2);
        assert_eq!(
            drops.load(Ordering::SeqCst),
            0,
            "the buffer is never released"
        );
        assert_eq!(d.leaked(), 2);
        lane.hold(Tracked(Arc::clone(&drops)));
        assert_eq!(drops.load(Ordering::SeqCst), 0, "later holds leak too");
        // The live QP delivers the tail after all: the slot is freed and
        // no longer counted as leaked.
        let t = posted.lock().take().expect("ticket");
        d.complete_send(t.wr_id(true), None);
        assert_eq!(d.in_flight(), 0);
        assert_eq!(d.leaked(), 1, "only the held buffer is still leaked");
    }

    // The sweep works from the lane's own list. An
    // abandoned operation whose tail CQE came after all was freed then;
    // its slot, reused by another lane, is not swept with the first lane.
    #[test]
    fn the_sweep_skips_slots_completed_and_reused_since() {
        let d = Arc::new(Dispatch::new(1, 64, 64));
        let lane = d.register_lane(8, 0).expect("lane");
        let other = d.register_lane(8, 0).expect("other");
        let qp = FakeQp::new(&d, false, true);
        let limits = Limits {
            credits: MS(100),
            completion: MS(10),
        };
        let posted = Mutex::new(None);
        assert!(
            d.run(&lane, &qp, 1, &limits, |t| {
                *posted.lock() = Some(t);
                Ok(())
            })
            .expect_err("abandoned")
            .unobserved
        );
        let t = posted.lock().take().expect("ticket");
        d.complete_send(t.wr_id(true), None); // late tail: frees the slot
        assert_eq!(d.in_flight(), 0);
        let live = d
            .begin(&other, 1, &mut far())
            .expect("the only slot, reused");
        // The reused slot is abandoned too, now by `other`: only the
        // generation tells it apart from `lane`'s stale ticket.
        d.abandon(&other, live);
        assert_eq!(
            d.unregister_lane(lane.key(), 0, true),
            0,
            "nothing of this lane left"
        );
        assert_eq!(d.in_flight(), 1, "the other lane's operation is untouched");
        assert_eq!(
            d.unregister_lane(other.key(), 0, true),
            1,
            "and is swept with its own lane"
        );
        assert_eq!(d.in_flight(), 0);
    }

    #[test]
    fn a_panicking_poller_kills_the_engine_and_a_shutdown_does_not() {
        let d = dispatch();
        let flag = AtomicBool::new(false);
        supervise(&d, &flag, || panic!("poller bug"));
        assert_eq!(
            d.dead_reason().as_deref(),
            Some("completion poller panicked")
        );
        let d = dispatch();
        let flag = AtomicBool::new(true);
        supervise(&d, &flag, || "shutdown".to_string());
        assert!(!d.is_dead());
    }

    #[test]
    fn channel_faults_stop_the_poller_instead_of_spinning() {
        assert_eq!(fd_fault(libc::POLLIN), None);
        assert_eq!(fd_fault(0), None);
        for bad in [
            libc::POLLERR,
            libc::POLLHUP,
            libc::POLLNVAL,
            libc::POLLIN | libc::POLLERR,
        ] {
            assert!(fd_fault(bad).is_some(), "{bad:#x}");
        }
    }

    // A control reply that does not come by its
    // deadline fails the lane, so a late reply can never be paired with
    // the next request. An idle wait for a request (no reply owed) that
    // times out does not.
    #[test]
    fn a_missing_control_reply_fails_the_lane() {
        let d = dispatch();
        let lane = d.register_lane(4, 2).expect("lane");
        let soon = || Some(Instant::now() + MS(20));
        assert!(lane.recv(soon(), false).is_err());
        assert!(!lane.failed(), "an idle request wait may time out");
        assert!(lane.recv(soon(), true).is_err());
        assert!(lane.failed(), "a missing reply fails the lane");
        // The late reply arrives; nobody can take it as the next reply.
        d.route_recv(wc_for(recv_wr_id(lane.key(), 3), 64));
        assert!(lane
            .recv(soon(), true)
            .expect_err("failed lane")
            .contains("lane failed"));
        // A healthy lane delivers its message.
        let ok = d.register_lane(4, 2).expect("lane");
        d.route_recv(wc_for(recv_wr_id(ok.key(), 1), 32));
        assert_eq!(ok.recv(soon(), true), Ok((1, 32)));
        // A flushed receive fails the lane.
        let mut flushed = wc_for(recv_wr_id(ok.key(), 2), 0);
        flushed.status = ffi::IBV_WC_WR_FLUSH_ERR;
        d.route_recv(flushed);
        assert!(ok.recv(None, false).is_err());
        assert!(ok.failed());
    }

    #[test]
    fn comp_vectors_spread_over_the_device() {
        assert_eq!(
            (0..6).map(|i| comp_vector_for(i, 4)).collect::<Vec<_>>(),
            vec![0, 1, 2, 3, 0, 1]
        );
        assert_eq!(
            comp_vector_for(7, 0),
            0,
            "a device reporting none gets vector 0"
        );
        assert_eq!(comp_vector_for(7, 1), 0);
    }
}
