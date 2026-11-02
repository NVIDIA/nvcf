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

//! The KV tier's DRAM block store.
//!
//! Hot KV blocks live in one arena of fixed-size slots, mapped once and
//! registered with every NIC by the server, which RDMA-WRITEs a hit
//! straight from its slot and RDMA-READs a PUT straight into one. The
//! store itself does no I/O: it hands out slot offsets and keeps three
//! promises.
//!
//! - **A slot is never reused while the NIC may touch it.** A hit is
//!   returned pinned ([`Hit`]); the slot cannot be evicted until every pin
//!   is dropped, which the server does only after the completion of every
//!   WRITE from it (or the destruction of the QP). A PUT holds its slot
//!   through a [`Reservation`] until it is committed or dropped, which the
//!   server does only after the READ into it has completed. A reservation
//!   that expires frees its key to the next PUT of that key but keeps its
//!   slot until it is dropped, since its READ may still be landing. The
//!   server owns reservations' lifetimes: one it leaks keeps its slot.
//! - **First PUT wins**. The first PUT of a key reserves it
//!   PENDING before a slot is taken; another PUT of the same key is told
//!   [`ReserveError::Exists`] or [`ReserveError::Pending`] before any slot
//!   is taken or any bytes move. A reservation's deadline is the request's
//!   budget capped at [`KvCacheConfig::max_pending`]; once it passes, the
//!   key is free for another PUT and the reservation can no longer commit.
//! - **A lookup sees complete blocks only.** HEAD and GET answer hit for a
//!   committed block, and return the 64-bit block check the writer
//!   supplied verbatim.
//!
//! Eviction is CLOCK over the slots: a hit or a commit sets a slot's
//! reference bit, the hand clears it, and the first unpinned, unreferenced
//! committed slot is reused. A second pass ignores reference bits, so a
//! slot is found whenever one is unpinned and committed. Reserved slots
//! are never taken.
//!
//! This tier is a cache by design: a block lost from it costs a miss,
//! never a wrong answer. An NVMe store behind it and purge are not part
//! of it yet.

use std::collections::hash_map::RandomState;
use std::collections::HashMap;
use std::hash::BuildHasher;
use std::sync::atomic::{AtomicBool, AtomicU32, AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use parking_lot::Mutex;

/// Namespace hash length.
pub const NAMESPACE_LEN: usize = 16;
/// Longest key (vLLM's sha256 block hash plus group index is 36 bytes).
pub const MAX_KEY_LEN: usize = 64;
/// Slot and arena alignment: 64 KiB, the largest page size of common
/// arm64 kernels and a multiple of every smaller page size, so every slot is page
/// aligned for registration and O_DIRECT on any host.
pub const ARENA_ALIGN: usize = 64 << 10;

/// The slot of a key reserved before its slot is taken.
const NO_SLOT: u32 = u32::MAX;

/// A block's identity: its namespace and key. Lookups match both, so two
/// models given the same prompt never share a block.
#[derive(Clone, Copy, PartialEq, Eq, Hash)]
pub struct BlockId {
    namespace: [u8; NAMESPACE_LEN],
    key_len: u8,
    key: [u8; MAX_KEY_LEN],
}

impl BlockId {
    /// `None` for an empty key or one longer than [`MAX_KEY_LEN`].
    pub fn new(namespace: [u8; NAMESPACE_LEN], key: &[u8]) -> Option<BlockId> {
        if key.is_empty() || key.len() > MAX_KEY_LEN {
            return None;
        }
        let mut k = [0u8; MAX_KEY_LEN];
        k[..key.len()].copy_from_slice(key);
        Some(BlockId {
            namespace,
            key_len: key.len() as u8,
            key: k,
        })
    }

    pub fn namespace(&self) -> &[u8; NAMESPACE_LEN] {
        &self.namespace
    }

    pub fn key(&self) -> &[u8] {
        &self.key[..self.key_len as usize]
    }
}

impl std::fmt::Debug for BlockId {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "BlockId(")?;
        for b in self.namespace.iter().chain(self.key()) {
            write!(f, "{b:02x}")?;
        }
        write!(f, ")")
    }
}

#[derive(Clone, Copy, Debug)]
pub struct KvCacheConfig {
    /// Bytes per slot: the largest block the store accepts. Rounded up to
    /// [`ARENA_ALIGN`].
    pub slot_bytes: usize,
    /// Number of slots.
    pub slots: usize,
    /// Index shards (each behind its own lock).
    pub shards: usize,
    /// The longest a PUT may hold its key uncommitted, whatever the
    /// request's budget (the server-side cap on a pending PUT).
    pub max_pending: Duration,
}

#[derive(Debug, PartialEq, Eq)]
pub enum ReserveError {
    /// The block is stored; nothing to write.
    Exists,
    /// Another PUT of this block is in flight.
    Pending,
    /// No slot is free and none can be evicted (every slot pinned or
    /// reserved).
    NoSpace,
    /// The block is larger than a slot.
    TooLarge,
    /// The reservation's deadline passed: it cannot commit.
    Expired,
    /// The bytes given are not the reserved length.
    LengthMismatch,
}

#[derive(Debug, Default, Clone, Copy, PartialEq, Eq)]
pub struct KvCacheStats {
    /// GETs that found a committed block.
    pub hits: u64,
    /// GETs that did not (HEADs are not counted).
    pub misses: u64,
    pub inserts: u64,
    pub evictions: u64,
    /// Expired reservations whose key a later PUT took over.
    pub expired_reservations: u64,
    pub no_space: u64,
}

#[derive(Clone, Copy)]
enum State {
    /// Reserved by a PUT, readable by nobody; its key is free for another
    /// PUT after `deadline`.
    Pending {
        deadline: Instant,
    },
    Complete,
}

#[derive(Clone, Copy)]
struct Entry {
    /// Which reservation made this entry: a stale reservation never acts
    /// on a newer one's entry for the same key.
    tag: u64,
    slot: u32,
    len: u32,
    check: u64,
    state: State,
}

/// Per-slot bookkeeping, outside the index locks.
struct SlotMeta {
    /// Hits in flight from this slot. Raised only under the owning shard's
    /// lock; lowered without it.
    pins: AtomicU32,
    /// CLOCK reference bit.
    referenced: AtomicBool,
    /// Held by a live [`Reservation`]: never handed out, even after the
    /// reservation's key expired, until the reservation is dropped.
    reserved: AtomicBool,
    /// The block in this slot, if any: the eviction hand finds the index
    /// entry through it. Changed only under the block's shard lock.
    owner: Mutex<Option<BlockId>>,
}

/// The slots' memory: an anonymous mapping, zeroed by the kernel page by
/// page on first touch (no fill at startup), aligned to [`ARENA_ALIGN`].
struct Arena {
    ptr: *mut u8,
    len: usize,
}

// SAFETY: the arena is plain memory owned by the store; access to slot
// contents is coordinated by pins and reservations (and by the NIC, which
// the store never touches), and the pointer itself is never reassigned.
unsafe impl Send for Arena {}
unsafe impl Sync for Arena {}

impl Arena {
    fn map(len: usize) -> std::io::Result<Arena> {
        let span = len.checked_add(ARENA_ALIGN).ok_or_else(|| {
            std::io::Error::new(std::io::ErrorKind::InvalidInput, "arena size overflows")
        })?;
        // SAFETY: a fresh private anonymous mapping; no existing memory is
        // affected. MAP_NORESERVE: the pages are committed when touched or
        // registered, not at startup.
        let raw = unsafe {
            libc::mmap(
                std::ptr::null_mut(),
                span,
                libc::PROT_READ | libc::PROT_WRITE,
                libc::MAP_PRIVATE | libc::MAP_ANONYMOUS | libc::MAP_NORESERVE,
                -1,
                0,
            )
        };
        if raw == libc::MAP_FAILED {
            return Err(std::io::Error::last_os_error());
        }
        let start = raw as usize;
        let aligned = start.next_multiple_of(ARENA_ALIGN);
        let head = aligned - start;
        let tail = span - head - len;
        // SAFETY: both ranges lie inside the mapping just made and outside
        // [aligned, aligned + len).
        unsafe {
            if head > 0 {
                libc::munmap(raw, head);
            }
            if tail > 0 {
                libc::munmap((aligned + len) as *mut libc::c_void, tail);
            }
            // Transparent huge pages where the kernel allows them: fewer
            // translations for the NICs. Advisory; failure changes nothing.
            libc::madvise(aligned as *mut libc::c_void, len, libc::MADV_HUGEPAGE);
        }
        Ok(Arena {
            ptr: aligned as *mut u8,
            len,
        })
    }
}

impl Drop for Arena {
    fn drop(&mut self) {
        // SAFETY: exactly the range kept by Arena::map.
        unsafe { libc::munmap(self.ptr as *mut libc::c_void, self.len) };
    }
}

pub struct KvCache {
    arena: Arena,
    slot_bytes: usize,
    max_pending: Duration,
    slots: Vec<SlotMeta>,
    hasher: RandomState,
    shards: Vec<Mutex<HashMap<BlockId, Entry>>>,
    free: Mutex<Vec<u32>>,
    hand: Mutex<usize>,
    next_tag: AtomicU64,
    hits: AtomicU64,
    misses: AtomicU64,
    inserts: AtomicU64,
    evictions: AtomicU64,
    expired: AtomicU64,
    no_space: AtomicU64,
}

/// A pinned, committed block: its slot cannot be reused until this is
/// dropped.
pub struct Hit {
    cache: Arc<KvCache>,
    slot: u32,
    len: u32,
    check: u64,
}

/// A slot reserved for one PUT. [`Reservation::commit`] makes the block
/// visible; dropping it uncommitted frees the slot and the key.
pub struct Reservation {
    cache: Arc<KvCache>,
    id: BlockId,
    tag: u64,
    slot: u32,
    len: u32,
    check: u64,
    deadline: Instant,
    committed: bool,
}

impl KvCache {
    pub fn new(config: KvCacheConfig) -> std::io::Result<Arc<KvCache>> {
        let invalid =
            |m: &str| std::io::Error::new(std::io::ErrorKind::InvalidInput, m.to_string());
        if config.slot_bytes == 0 || config.slots == 0 || config.shards == 0 {
            return Err(invalid("slot_bytes, slots and shards must be positive"));
        }
        if config.slots >= NO_SLOT as usize {
            return Err(invalid("too many slots"));
        }
        let slot_bytes = config
            .slot_bytes
            .checked_next_multiple_of(ARENA_ALIGN)
            .filter(|&b| b <= u32::MAX as usize)
            .ok_or_else(|| invalid("slot_bytes too large"))?;
        let total = slot_bytes
            .checked_mul(config.slots)
            .ok_or_else(|| invalid("arena size overflows"))?;
        let arena = Arena::map(total)?;
        let slots = (0..config.slots)
            .map(|_| SlotMeta {
                pins: AtomicU32::new(0),
                referenced: AtomicBool::new(false),
                reserved: AtomicBool::new(false),
                owner: Mutex::new(None),
            })
            .collect();
        Ok(Arc::new(KvCache {
            arena,
            slot_bytes,
            max_pending: config.max_pending,
            slots,
            // Per process: a client cannot aim its keys at one shard lock.
            hasher: RandomState::new(),
            shards: (0..config.shards)
                .map(|_| Mutex::new(HashMap::new()))
                .collect(),
            // Popped from the end: hand out slot 0 first.
            free: Mutex::new((0..config.slots as u32).rev().collect()),
            hand: Mutex::new(0),
            next_tag: AtomicU64::new(1),
            hits: AtomicU64::new(0),
            misses: AtomicU64::new(0),
            inserts: AtomicU64::new(0),
            evictions: AtomicU64::new(0),
            expired: AtomicU64::new(0),
            no_space: AtomicU64::new(0),
        }))
    }

    /// The arena, for registration with each NIC: (address, length).
    pub fn arena(&self) -> (*mut u8, usize) {
        (self.arena.ptr, self.arena.len)
    }

    pub fn slot_bytes(&self) -> usize {
        self.slot_bytes
    }

    /// The longest a PUT may hold its key uncommitted.
    pub fn max_pending(&self) -> Duration {
        self.max_pending
    }

    pub fn stats(&self) -> KvCacheStats {
        KvCacheStats {
            hits: self.hits.load(Ordering::Relaxed),
            misses: self.misses.load(Ordering::Relaxed),
            inserts: self.inserts.load(Ordering::Relaxed),
            evictions: self.evictions.load(Ordering::Relaxed),
            expired_reservations: self.expired.load(Ordering::Relaxed),
            no_space: self.no_space.load(Ordering::Relaxed),
        }
    }

    fn shard(&self, id: &BlockId) -> &Mutex<HashMap<BlockId, Entry>> {
        &self.shards[(self.hasher.hash_one(id) % self.shards.len() as u64) as usize]
    }

    /// Whether a committed block is stored (HEAD). Does not pin.
    pub fn contains(&self, id: &BlockId) -> bool {
        matches!(
            self.shard(id).lock().get(id),
            Some(Entry {
                state: State::Complete,
                ..
            })
        )
    }

    /// A committed block, pinned (GET). `None` is a miss.
    pub fn get(self: &Arc<Self>, id: &BlockId) -> Option<Hit> {
        let shard = self.shard(id).lock();
        match shard.get(id) {
            Some(&Entry {
                slot,
                len,
                check,
                state: State::Complete,
                ..
            }) => {
                let meta = &self.slots[slot as usize];
                // Raised under the shard lock, which eviction also holds
                // when it checks for zero: no hit can slip past it.
                meta.pins.fetch_add(1, Ordering::AcqRel);
                meta.referenced.store(true, Ordering::Relaxed);
                drop(shard);
                self.hits.fetch_add(1, Ordering::Relaxed);
                Some(Hit {
                    cache: Arc::clone(self),
                    slot,
                    len,
                    check,
                })
            }
            _ => {
                drop(shard);
                self.misses.fetch_add(1, Ordering::Relaxed);
                None
            }
        }
    }

    /// Reserve a slot for a PUT of `len` bytes with the writer's block
    /// `check`. The key is held until `deadline`, capped at `max_pending`
    /// from now.
    pub fn reserve(
        self: &Arc<Self>,
        id: BlockId,
        len: u32,
        check: u64,
        deadline: Instant,
    ) -> Result<Reservation, ReserveError> {
        if len as usize > self.slot_bytes {
            return Err(ReserveError::TooLarge);
        }
        let now = Instant::now();
        let deadline = deadline.min(now + self.max_pending);
        let tag = self.next_tag.fetch_add(1, Ordering::Relaxed);
        // Claim the key first, so a PUT that loses the race takes no slot
        // (and evicts nothing).
        {
            let mut shard = self.shard(&id).lock();
            if let Some(e) = shard.get(&id).copied() {
                match e.state {
                    State::Complete => return Err(ReserveError::Exists),
                    State::Pending { deadline: d } if d > now => return Err(ReserveError::Pending),
                    State::Pending { .. } => {
                        // Expired: take the key. Its slot stays with the
                        // stale reservation, whose READ may still land.
                        shard.remove(&id);
                        if e.slot != NO_SLOT {
                            *self.slots[e.slot as usize].owner.lock() = None;
                        }
                        self.expired.fetch_add(1, Ordering::Relaxed);
                    }
                }
            }
            shard.insert(
                id,
                Entry {
                    tag,
                    slot: NO_SLOT,
                    len,
                    check,
                    state: State::Pending { deadline },
                },
            );
        }
        let Some(slot) = self.take_slot() else {
            let mut shard = self.shard(&id).lock();
            if matches!(shard.get(&id), Some(e) if e.tag == tag) {
                shard.remove(&id);
            }
            drop(shard);
            self.no_space.fetch_add(1, Ordering::Relaxed);
            return Err(ReserveError::NoSpace);
        };
        self.slots[slot as usize]
            .reserved
            .store(true, Ordering::Release);
        let mut shard = self.shard(&id).lock();
        match shard.get_mut(&id) {
            Some(e) if e.tag == tag => {
                e.slot = slot;
                *self.slots[slot as usize].owner.lock() = Some(id);
                self.slots[slot as usize]
                    .referenced
                    .store(false, Ordering::Relaxed);
                drop(shard);
                Ok(Reservation {
                    cache: Arc::clone(self),
                    id,
                    tag,
                    slot,
                    len,
                    check,
                    deadline,
                    committed: false,
                })
            }
            _ => {
                // Our claim expired and was taken while we found a slot.
                drop(shard);
                self.release_slot(slot);
                Err(ReserveError::Pending)
            }
        }
    }

    /// A free slot, or one evicted by the CLOCK hand.
    fn take_slot(&self) -> Option<u32> {
        if let Some(s) = self.free.lock().pop() {
            return Some(s);
        }
        let n = self.slots.len();
        let mut hand = self.hand.lock();
        // Two passes: the first honours reference bits, the second does not,
        // so hits racing the hand cannot hide every slot.
        for step in 0..2 * n {
            let s = *hand;
            *hand = (s + 1) % n;
            let meta = &self.slots[s];
            if meta.reserved.load(Ordering::Acquire) {
                continue;
            }
            if meta.referenced.swap(false, Ordering::Relaxed) && step < n {
                continue;
            }
            // The owner guard is a temporary of this statement: the lock
            // order stays shard -> owner.
            let Some(owner) = *meta.owner.lock() else {
                continue;
            };
            let mut shard = self.shard(&owner).lock();
            let Some(e) = shard.get(&owner).copied() else {
                continue;
            };
            if e.slot as usize != s
                || !matches!(e.state, State::Complete)
                || meta.pins.load(Ordering::Acquire) != 0
            {
                continue;
            }
            shard.remove(&owner);
            *meta.owner.lock() = None;
            drop(shard);
            self.evictions.fetch_add(1, Ordering::Relaxed);
            return Some(s as u32);
        }
        drop(hand);
        // Slots released while the hand swept.
        self.free.lock().pop()
    }

    /// Return a reserved slot to the free list.
    fn release_slot(&self, slot: u32) {
        self.slots[slot as usize]
            .reserved
            .store(false, Ordering::Release);
        self.free.lock().push(slot);
    }

    fn slot_offset(&self, slot: u32) -> usize {
        slot as usize * self.slot_bytes
    }

    /// Every slot is free, reserved, or owned by exactly one index entry;
    /// counts add up. For tests.
    #[cfg(test)]
    fn check_invariants(&self) {
        let free = self.free.lock().clone();
        let mut seen = vec![false; self.slots.len()];
        for &s in &free {
            assert!(
                !std::mem::replace(&mut seen[s as usize], true),
                "slot {s} twice in the free list"
            );
            let m = &self.slots[s as usize];
            assert!(
                m.owner.lock().is_none() && !m.reserved.load(Ordering::Relaxed),
                "free slot {s} owned or reserved"
            );
            assert_eq!(m.pins.load(Ordering::Relaxed), 0, "free slot {s} pinned");
        }
        for shard in &self.shards {
            for (id, e) in shard.lock().iter() {
                if e.slot == NO_SLOT {
                    continue;
                }
                assert!(
                    !std::mem::replace(&mut seen[e.slot as usize], true),
                    "slot {} owned twice",
                    e.slot
                );
                assert_eq!(
                    *self.slots[e.slot as usize].owner.lock(),
                    Some(*id),
                    "owner of slot {}",
                    e.slot
                );
            }
        }
        for (s, was_seen) in seen.iter().enumerate() {
            if !was_seen {
                // Neither free nor indexed: only a live reservation (whose
                // key may have been retaken) may hold it.
                assert!(
                    self.slots[s].reserved.load(Ordering::Relaxed),
                    "slot {s} leaked"
                );
            }
        }
    }
}

impl Hit {
    /// Byte offset of the block in the arena (for the WRITE's local address).
    pub fn offset(&self) -> usize {
        self.cache.slot_offset(self.slot)
    }
    pub fn len(&self) -> u32 {
        self.len
    }
    pub fn is_empty(&self) -> bool {
        self.len == 0
    }
    /// The writer's 64-bit block check, verbatim.
    pub fn check(&self) -> u64 {
        self.check
    }
    /// A copy of the block's bytes (CPU path; the server sends by RDMA).
    pub fn to_vec(&self) -> Vec<u8> {
        let off = self.offset();
        // SAFETY: the slot is pinned and committed, so nothing writes it;
        // `off + len` is within the arena (len <= slot_bytes).
        unsafe {
            std::slice::from_raw_parts(self.cache.arena.ptr.add(off), self.len as usize).to_vec()
        }
    }
}

impl Drop for Hit {
    fn drop(&mut self) {
        self.cache.slots[self.slot as usize]
            .pins
            .fetch_sub(1, Ordering::AcqRel);
    }
}

impl Reservation {
    /// Byte offset of the reserved slot (for the READ's local address).
    pub fn offset(&self) -> usize {
        self.cache.slot_offset(self.slot)
    }
    pub fn len(&self) -> u32 {
        self.len
    }
    pub fn is_empty(&self) -> bool {
        self.len == 0
    }
    pub fn check(&self) -> u64 {
        self.check
    }
    /// Copy `data` into the slot (CPU path; the server fills it by RDMA).
    pub fn write(&mut self, data: &[u8]) -> Result<(), ReserveError> {
        if data.len() != self.len as usize {
            return Err(ReserveError::LengthMismatch);
        }
        let off = self.offset();
        // SAFETY: the slot is reserved to this PUT alone and not visible
        // to readers; `off + len` is within the arena.
        unsafe {
            std::ptr::copy_nonoverlapping(data.as_ptr(), self.cache.arena.ptr.add(off), data.len())
        }
        Ok(())
    }
    /// A copy of the slot's bytes, to verify a PUT. Call it only after the
    /// READ into the slot has completed: before that the NIC may still be
    /// writing them.
    pub fn to_vec(&self) -> Vec<u8> {
        // SAFETY: the slot is reserved to this PUT; the caller has waited
        // for the READ's completion, so nothing writes it while it is
        // copied. `offset + len` is within the arena.
        unsafe {
            std::slice::from_raw_parts(self.cache.arena.ptr.add(self.offset()), self.len as usize)
                .to_vec()
        }
    }

    /// Make the block visible to GET and HEAD. Fails with
    /// [`ReserveError::Expired`] once the deadline has passed (whether or
    /// not another PUT took the key): nothing becomes visible and the slot
    /// is freed.
    pub fn commit(mut self) -> Result<(), ReserveError> {
        let mut shard = self.cache.shard(&self.id).lock();
        match shard.get_mut(&self.id) {
            Some(e) if e.tag == self.tag && Instant::now() < self.deadline => {
                e.state = State::Complete;
                let meta = &self.cache.slots[self.slot as usize];
                // A new block gets one pass of the hand before eviction.
                meta.referenced.store(true, Ordering::Relaxed);
                meta.reserved.store(false, Ordering::Release);
                drop(shard);
                self.committed = true;
                self.cache.inserts.fetch_add(1, Ordering::Relaxed);
                Ok(())
            }
            _ => {
                drop(shard);
                // Drop removes the entry if it is still ours and frees the slot.
                Err(ReserveError::Expired)
            }
        }
    }
}

impl Drop for Reservation {
    fn drop(&mut self) {
        if self.committed {
            return;
        }
        let mut shard = self.cache.shard(&self.id).lock();
        if matches!(shard.get(&self.id), Some(e) if e.tag == self.tag) {
            shard.remove(&self.id);
            *self.cache.slots[self.slot as usize].owner.lock() = None;
        }
        drop(shard);
        // The slot is this reservation's until now, expired or not.
        self.cache.release_slot(self.slot);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn config(slots: usize) -> KvCacheConfig {
        KvCacheConfig {
            slot_bytes: 4096,
            slots,
            shards: 4,
            max_pending: Duration::from_secs(60),
        }
    }
    fn cache(slots: usize) -> Arc<KvCache> {
        KvCache::new(config(slots)).expect("cache")
    }
    fn id(n: u8) -> BlockId {
        BlockId::new([7; NAMESPACE_LEN], &[n; 36]).expect("id")
    }
    fn later() -> Instant {
        Instant::now() + Duration::from_secs(60)
    }
    fn put(c: &Arc<KvCache>, n: u8, fill: u8) {
        let mut r = c.reserve(id(n), 100, n as u64, later()).expect("reserve");
        r.write(&[fill; 100]).expect("write");
        r.commit().expect("commit");
    }

    #[test]
    fn a_committed_block_is_found_with_its_bytes_and_check() {
        let c = cache(4);
        assert!(c.get(&id(1)).is_none());
        put(&c, 1, 0xab);
        assert!(c.contains(&id(1)));
        let hit = c.get(&id(1)).expect("hit");
        assert_eq!(hit.to_vec(), vec![0xab; 100]);
        assert_eq!(hit.check(), 1);
        assert_eq!(hit.offset() % ARENA_ALIGN, 0);
        let s = c.stats();
        assert_eq!((s.hits, s.misses, s.inserts), (1, 1, 1));
        drop(hit);
        c.check_invariants();
    }

    #[test]
    fn namespaces_separate_identical_keys() {
        let c = cache(4);
        put(&c, 1, 1);
        let other = BlockId::new([8; NAMESPACE_LEN], &[1; 36]).expect("id");
        assert!(c.get(&other).is_none());
    }

    #[test]
    fn a_pending_block_is_invisible_and_the_first_put_wins() {
        let c = cache(4);
        let r = c.reserve(id(1), 100, 1, later()).expect("first");
        assert!(!c.contains(&id(1)));
        assert!(c.get(&id(1)).is_none());
        assert_eq!(
            c.reserve(id(1), 100, 2, later()).err(),
            Some(ReserveError::Pending)
        );
        r.commit().expect("commit");
        assert_eq!(
            c.reserve(id(1), 100, 3, later()).err(),
            Some(ReserveError::Exists)
        );
        assert_eq!(
            c.get(&id(1)).expect("hit").check(),
            1,
            "the first writer's check"
        );
        c.check_invariants();
    }

    #[test]
    fn a_losing_put_evicts_nothing() {
        let c = cache(2);
        put(&c, 1, 1);
        let first = c.reserve(id(2), 100, 2, later()).expect("first");
        // The store is full: a second PUT of the same key must lose
        // without evicting block 1.
        assert_eq!(
            c.reserve(id(2), 100, 2, later()).err(),
            Some(ReserveError::Pending)
        );
        assert!(c.contains(&id(1)));
        assert_eq!(c.stats().evictions, 0);
        first.commit().expect("commit");
        c.check_invariants();
    }

    #[test]
    fn a_dropped_reservation_frees_key_and_slot() {
        let c = cache(1);
        drop(c.reserve(id(1), 100, 1, later()).expect("reserve"));
        assert!(!c.contains(&id(1)));
        put(&c, 2, 2);
        assert!(c.contains(&id(2)), "the only slot was freed");
        c.check_invariants();
    }

    #[test]
    fn an_expired_reservation_frees_its_key_but_keeps_its_slot() {
        let c = cache(2);
        let stale = c.reserve(id(1), 100, 1, Instant::now()).expect("reserve");
        // The key is free again for another PUT...
        let mut fresh = c.reserve(id(1), 100, 2, later()).expect("retaken");
        assert_ne!(fresh.offset(), stale.offset());
        fresh.write(&[9; 100]).expect("write");
        // ...but the stale slot, whose READ may still land, is not reused.
        assert_eq!(
            c.reserve(id(2), 100, 3, later()).err(),
            Some(ReserveError::NoSpace)
        );
        c.check_invariants();
        assert_eq!(stale.commit().err(), Some(ReserveError::Expired));
        // Dropped by the failed commit: its slot is free now.
        put(&c, 2, 2);
        fresh.commit().expect("commit");
        assert_eq!(c.get(&id(1)).expect("hit").check(), 2);
        assert_eq!(c.stats().expired_reservations, 1);
        c.check_invariants();
    }

    #[test]
    fn a_late_commit_fails_even_if_nobody_took_the_key() {
        let c = cache(2);
        let r = c.reserve(id(1), 100, 1, Instant::now()).expect("reserve");
        assert_eq!(r.commit().err(), Some(ReserveError::Expired));
        assert!(!c.contains(&id(1)));
        c.check_invariants();
    }

    #[test]
    fn the_deadline_is_capped() {
        let c = KvCache::new(KvCacheConfig {
            max_pending: Duration::ZERO,
            ..config(2)
        })
        .expect("cache");
        let r = c
            .reserve(id(1), 100, 1, Instant::now() + Duration::from_secs(3600))
            .expect("reserve");
        // The cap made the far-future deadline already past.
        assert!(c.reserve(id(1), 100, 2, later()).is_ok(), "the key is free");
        drop(r);
        c.check_invariants();
    }

    #[test]
    fn an_expired_reservation_never_frees_its_slot_to_the_hand() {
        let c = cache(1);
        let stale = c.reserve(id(1), 100, 1, Instant::now()).expect("reserve");
        assert_eq!(
            c.reserve(id(2), 100, 2, later()).err(),
            Some(ReserveError::NoSpace)
        );
        drop(stale);
        put(&c, 2, 2);
        assert!(c.contains(&id(2)));
        c.check_invariants();
    }

    #[test]
    fn a_pinned_slot_is_never_evicted() {
        let c = cache(2);
        put(&c, 1, 1);
        put(&c, 2, 2);
        let h1 = c.get(&id(1)).expect("hit");
        let h2 = c.get(&id(2)).expect("hit");
        assert_eq!(
            c.reserve(id(3), 100, 3, later()).err(),
            Some(ReserveError::NoSpace)
        );
        assert!(!c.contains(&id(3)), "a refused PUT leaves no claim");
        assert_eq!(h1.to_vec(), vec![1; 100], "bytes untouched");
        drop(h1);
        put(&c, 3, 3);
        assert!(!c.contains(&id(1)), "the unpinned block went");
        assert_eq!(h2.to_vec(), vec![2; 100]);
        assert!(c.contains(&id(2)));
        assert_eq!(c.stats().evictions, 1);
        drop(h2);
        c.check_invariants();
    }

    #[test]
    fn clock_spares_a_recently_hit_block() {
        let c = cache(3);
        put(&c, 1, 1);
        put(&c, 2, 2);
        put(&c, 3, 3);
        // A full sweep clears every bit (new blocks start referenced) and
        // evicts one; then hit one of the survivors.
        put(&c, 4, 4);
        let survivor = (1..=3).find(|&n| c.contains(&id(n))).expect("a survivor");
        drop(c.get(&id(survivor)).expect("hit"));
        put(&c, 5, 5);
        assert!(c.contains(&id(survivor)), "the referenced block survives");
        c.check_invariants();
    }

    #[test]
    fn hits_racing_the_hand_cannot_hide_every_slot() {
        let c = cache(2);
        put(&c, 1, 1);
        put(&c, 2, 2);
        // Both blocks referenced, none pinned: the second pass evicts one.
        drop(c.get(&id(1)).expect("hit"));
        drop(c.get(&id(2)).expect("hit"));
        put(&c, 3, 3);
        assert!(c.contains(&id(3)));
        c.check_invariants();
    }

    #[test]
    fn oversized_blocks_and_bad_configs_are_refused() {
        let c = cache(1);
        assert_eq!(
            c.reserve(id(1), (c.slot_bytes() + 1) as u32, 0, later())
                .err(),
            Some(ReserveError::TooLarge)
        );
        let mut r = c.reserve(id(1), 100, 0, later()).expect("reserve");
        assert_eq!(r.write(&[0; 99]), Err(ReserveError::LengthMismatch));
        drop(r);
        assert!(KvCache::new(KvCacheConfig {
            slot_bytes: 0,
            ..config(1)
        })
        .is_err());
        assert!(KvCache::new(KvCacheConfig {
            slots: 0,
            ..config(1)
        })
        .is_err());
        assert!(KvCache::new(KvCacheConfig {
            shards: 0,
            ..config(1)
        })
        .is_err());
        assert!(KvCache::new(KvCacheConfig {
            slot_bytes: usize::MAX,
            ..config(2)
        })
        .is_err());
        assert!(BlockId::new([0; NAMESPACE_LEN], &[]).is_none());
        assert!(BlockId::new([0; NAMESPACE_LEN], &[0; MAX_KEY_LEN + 1]).is_none());
    }

    #[test]
    fn slots_are_page_aligned_and_inside_the_arena() {
        let c = KvCache::new(KvCacheConfig {
            slot_bytes: 3_052_800,
            slots: 3,
            ..config(1)
        })
        .expect("cache");
        assert_eq!(c.slot_bytes(), 3_080_192);
        let (ptr, len) = c.arena();
        assert_eq!(ptr as usize % ARENA_ALIGN, 0);
        assert_eq!(len, 3 * 3_080_192);
        for n in 0..3 {
            let r = c.reserve(id(n), 3_052_800, 0, later()).expect("reserve");
            assert!(r.offset() + r.len() as usize <= len);
            r.commit().expect("commit");
        }
        c.check_invariants();
    }

    #[test]
    fn a_large_arena_maps_without_touching_its_pages() {
        // 64 GiB mapped lazily: this would take minutes (or fail) if the
        // arena were filled at startup.
        let start = Instant::now();
        let c = KvCache::new(KvCacheConfig {
            slot_bytes: 64 << 20,
            slots: 1024,
            ..config(1)
        })
        .expect("cache");
        assert!(
            start.elapsed() < Duration::from_secs(5),
            "{:?}",
            start.elapsed()
        );
        put(&c, 1, 1);
        assert_eq!(c.get(&id(1)).expect("hit").to_vec(), vec![1; 100]);
    }

    #[test]
    fn concurrent_puts_gets_and_expiry_keep_every_promise() {
        let c = KvCache::new(KvCacheConfig {
            slot_bytes: 4096,
            slots: 64,
            shards: 8,
            max_pending: Duration::from_millis(2),
        })
        .expect("cache");
        let threads: Vec<_> = (0..8u8)
            .map(|t| {
                let c = Arc::clone(&c);
                std::thread::spawn(move || {
                    let mut held: Vec<(u8, Hit)> = Vec::new();
                    let (mut committed, mut expired) = (0u32, 0u32);
                    for i in 0..4_000u32 {
                        let n = ((i * 7 + t as u32 * 13) % 200) as u8;
                        let key = BlockId::new([1; NAMESPACE_LEN], &[n; 36]).expect("id");
                        if let Some(h) = c.get(&key) {
                            // The bytes of a hit are always its own block's.
                            assert_eq!(h.to_vec(), vec![n; 64]);
                            assert_eq!(h.check(), n as u64);
                            // Hold some hits across other threads' evictions.
                            held.push((n, h));
                            if held.len() > 4 {
                                let (m, h) = held.remove(0);
                                assert_eq!(h.to_vec(), vec![m; 64], "a held hit kept its bytes");
                            }
                        } else if let Ok(mut r) =
                            c.reserve(key, 64, n as u64, Instant::now() + Duration::from_secs(5))
                        {
                            r.write(&[n; 64]).expect("write");
                            if i % 5 == 0 {
                                // Let some reservations run past their (capped) deadline.
                                std::thread::sleep(Duration::from_millis(3));
                            }
                            match r.commit() {
                                Ok(()) => committed += 1,
                                Err(ReserveError::Expired) => expired += 1,
                                Err(e) => panic!("commit: {e:?}"),
                            }
                        }
                    }
                    for (m, h) in held {
                        assert_eq!(h.to_vec(), vec![m; 64]);
                    }
                    (committed, expired)
                })
            })
            .collect();
        let (mut committed, mut expired) = (0u32, 0u32);
        for t in threads {
            let (a, b) = t.join().expect("thread");
            committed += a;
            expired += b;
        }
        let s = c.stats();
        assert_eq!(s.inserts, committed as u64);
        assert!(
            committed > 0 && expired > 0 && s.hits > 0 && s.evictions > 0,
            "{s:?} committed {committed} expired {expired}"
        );
        c.check_invariants();
    }
}
