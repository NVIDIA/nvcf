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

//! Pre-registered memory pool for RDMA operations.
//!
//! Memory registration (ibv_reg_mr) is expensive — it pins pages and programs
//! the NIC's page table. For high-throughput workloads, we pre-register a large
//! region and hand out slices from it.
//!
//! The pool divides a contiguous allocation into fixed-size slots. Each slot
//! can be checked out for an RDMA operation and returned when done.

use std::collections::VecDeque;
use std::sync::Arc;
use std::time::{Duration, Instant};

use parking_lot::{Condvar, Mutex};
use tracing::info;

use crate::device::{Access, MemoryRegion, RdmaDevice};

/// A single buffer slot from the pool.
pub struct PoolBuffer {
    /// Pointer to the start of this slot within the pool's allocation.
    ptr: *mut u8,
    /// Length of this slot.
    len: usize,
    /// Slot index (for returning to pool).
    index: usize,
    /// Reference back to pool for auto-return.
    pool: Arc<PoolInner>,
}

// SAFETY: The underlying memory is pinned and registered. The pool ensures
// no two PoolBuffers alias the same slot.
unsafe impl Send for PoolBuffer {}
unsafe impl Sync for PoolBuffer {}

impl PoolBuffer {
    /// Get a mutable slice of this buffer.
    pub fn as_mut_slice(&mut self) -> &mut [u8] {
        unsafe { std::slice::from_raw_parts_mut(self.ptr, self.len) }
    }

    /// Get a read-only slice.
    pub fn as_slice(&self) -> &[u8] {
        unsafe { std::slice::from_raw_parts(self.ptr, self.len) }
    }

    /// Get the raw pointer.
    pub fn as_ptr(&self) -> *const u8 {
        self.ptr
    }

    /// Get the address as u64 (for RDMA scatter-gather).
    pub fn addr(&self) -> u64 {
        self.ptr as u64
    }

    /// Get the buffer length.
    pub fn len(&self) -> usize {
        self.len
    }

    /// Whether the buffer has no bytes.
    pub fn is_empty(&self) -> bool {
        self.len == 0
    }

    /// Get the lkey from the pool's memory region.
    pub fn lkey(&self) -> u32 {
        self.pool.lkey
    }

    /// Get the rkey from the pool's memory region.
    pub fn rkey(&self) -> u32 {
        self.pool.rkey
    }
}

impl Drop for PoolBuffer {
    fn drop(&mut self) {
        self.pool.return_slot(self.index);
    }
}

/// Page-aligned, zeroed heap allocation backing a pool. O_DIRECT reads
/// and writes need buffers aligned to the logical block size; a `Vec<u8>`
/// this large comes from glibc's mmap path 16 bytes past a page boundary,
/// so no slot of it could be used for direct I/O.
struct PageAlloc {
    ptr: *mut u8,
    layout: std::alloc::Layout,
}

impl PageAlloc {
    const ALIGN: usize = 4096;

    fn new(size: usize) -> Result<Self, String> {
        if size == 0 {
            return Err("memory pool size must be > 0".into());
        }
        let layout = std::alloc::Layout::from_size_align(size, Self::ALIGN)
            .map_err(|e| format!("memory pool layout for {size} bytes: {e}"))?;
        // SAFETY: the layout has a nonzero size.
        let ptr = unsafe { std::alloc::alloc_zeroed(layout) };
        if ptr.is_null() {
            return Err(format!("allocating {size} bytes for a memory pool failed"));
        }
        Ok(Self { ptr, layout })
    }
}

impl Drop for PageAlloc {
    fn drop(&mut self) {
        // SAFETY: allocated in `new` with this layout and freed only here.
        unsafe { std::alloc::dealloc(self.ptr, self.layout) }
    }
}

// SAFETY: The base pointer is pinned (registered with ibv_reg_mr) and the
// pool ensures exclusive access to each slot via the free_slots queue.
unsafe impl Send for PoolInner {}
unsafe impl Sync for PoolInner {}

struct PoolInner {
    /// Registered memory region covering the entire allocation. Declared
    /// before the allocation so it is deregistered before the memory is
    /// freed (fields drop in declaration order). The region holds the
    /// device, so the PD and context outlive every slot still checked out
    /// or parked when the pool's owner goes away.
    _mr: MemoryRegion,
    /// What remote peers may do to this pool's memory.
    access: Access,
    /// The raw allocation (kept alive for the lifetime of the pool).
    _allocation: PageAlloc,
    /// Base pointer of the allocation.
    base: *mut u8,
    /// lkey for the entire region.
    lkey: u32,
    /// rkey for the entire region.
    rkey: u32,
    /// Slot size in bytes.
    slot_size: usize,
    /// Free slot indices.
    free_slots: FreeSlots,
    /// Total number of slots.
    total_slots: usize,
}

impl PoolInner {
    fn return_slot(&self, index: usize) {
        self.free_slots.push(index);
    }
}

/// Pre-registered memory pool.
pub struct MemoryPool {
    inner: Arc<PoolInner>,
}

impl MemoryPool {
    /// Create a new memory pool.
    ///
    /// `slot_size`: size of each buffer slot (e.g., 4MB for chunk-sized ops).
    /// `num_slots`: number of slots to pre-allocate.
    ///
    /// Total memory = slot_size * num_slots. The allocation is
    /// page-aligned, so every slot is too when `slot_size` is a multiple
    /// of 4096, and slots can be the buffers of O_DIRECT I/O.
    ///
    /// Registers for a client that both GETs (the server RDMA-WRITEs into
    /// a slot) and PUTs (the server RDMA-READs a slot). A server, or a
    /// client that only does one of the two, should use [`Self::with_access`]
    /// and grant no more than that.
    pub fn new(
        device: &Arc<RdmaDevice>,
        slot_size: usize,
        num_slots: usize,
    ) -> Result<Self, String> {
        Self::with_access(device, slot_size, num_slots, Access::REMOTE_READ_WRITE)
    }

    /// `new`, registering the pool with exactly `access`.
    pub fn with_access(
        device: &Arc<RdmaDevice>,
        slot_size: usize,
        num_slots: usize,
        access: Access,
    ) -> Result<Self, String> {
        let total_size = slot_size
            .checked_mul(num_slots)
            .ok_or_else(|| format!("memory pool of {num_slots} x {slot_size} bytes overflows"))?;

        let allocation = PageAlloc::new(total_size)?;
        let base = allocation.ptr;

        // Register the entire allocation as one memory region.
        let mr = device.register_memory(base, total_size, access)?;
        let lkey = mr.lkey;
        let rkey = mr.rkey;

        let free_slots = FreeSlots::new(num_slots);

        info!(
            slot_size,
            num_slots,
            total_mb = total_size / (1024 * 1024),
            lkey,
            rkey,
            ?access,
            "memory pool created"
        );

        Ok(Self {
            inner: Arc::new(PoolInner {
                _mr: mr,
                access,
                _allocation: allocation,
                base,
                lkey,
                rkey,
                slot_size,
                free_slots,
                total_slots: num_slots,
            }),
        })
    }

    /// Check out a buffer from the pool. Returns None if all slots are in use.
    ///
    /// The slot is NOT cleared: it holds whatever its previous user left
    /// in it, possibly another client's data. Clearing a 16 MiB slot on
    /// every request costs CPU time on every GET and limits a server's
    /// request rate, so every caller instead exposes
    /// only bytes it wrote in the same request: the S3-over-RDMA
    /// server sends `slot[..read.len]`, which `ReadFn` filled, and refuses
    /// a `read.len` larger than the buffer it passed; PERSIST stores
    /// `slot[..len]` right after RDMA-READing exactly `len` bytes into it.
    /// A caller that needs cleared memory uses
    /// [`checkout_zeroed`](Self::checkout_zeroed).
    pub fn checkout(&self) -> Option<PoolBuffer> {
        let index = self.inner.free_slots.pop()?;
        Some(self.hand_out(index))
    }

    /// `checkout`, but wait up to `timeout` for a slot to be returned
    /// instead of failing at once. This is the pool's backpressure: a
    /// burst of requests larger than the pool queues for slots rather
    /// than erroring. Keep `timeout` below the peer's response timeout so
    /// a queued request still gets an answer (NO_SLOT) in time.
    pub fn checkout_timeout(&self, timeout: Duration) -> Option<PoolBuffer> {
        let index = self.inner.free_slots.pop_timeout(timeout)?;
        Some(self.hand_out(index))
    }

    /// `checkout`, with the whole slot cleared first. The clear is a CPU
    /// write; it does not fence the NIC. On a LOCAL pool only this
    /// process's own work requests reach a slot, and they are drained
    /// before a slot is returned. On a pool registered with remote access,
    /// a peer holding the rkey can reach any slot, free or not, so a
    /// cleared slot is not a guarantee against a remote writer.
    pub fn checkout_zeroed(&self) -> Option<PoolBuffer> {
        let mut buf = self.checkout()?;
        buf.as_mut_slice().fill(0);
        Some(buf)
    }

    fn hand_out(&self, index: usize) -> PoolBuffer {
        // SAFETY: `index` came from the free list, so it is below
        // `total_slots` and the slot lies inside the allocation.
        let ptr = unsafe { self.inner.base.add(index * self.inner.slot_size) };
        PoolBuffer {
            ptr,
            len: self.inner.slot_size,
            index,
            pool: Arc::clone(&self.inner),
        }
    }

    /// What remote peers may do to this pool's memory.
    pub fn access(&self) -> Access {
        self.inner.access
    }

    /// Number of available slots.
    pub fn available(&self) -> usize {
        self.inner.free_slots.len()
    }

    /// Total number of slots.
    pub fn total(&self) -> usize {
        self.inner.total_slots
    }

    /// Slot size in bytes.
    pub fn slot_size(&self) -> usize {
        self.inner.slot_size
    }

    /// lkey for the entire pool region.
    pub fn lkey(&self) -> u32 {
        self.inner.lkey
    }

    /// rkey for the entire pool region.
    pub fn rkey(&self) -> u32 {
        self.inner.rkey
    }

    /// Base address of the pool.
    pub fn base_addr(&self) -> u64 {
        self.inner.base as u64
    }

    /// Total size of the pool in bytes.
    pub fn total_size(&self) -> usize {
        self.inner.slot_size * self.inner.total_slots
    }
}

/// Free slot indices, with a condition variable so a caller can wait
/// for a slot to come back.
struct FreeSlots {
    queue: Mutex<VecDeque<usize>>,
    returned: Condvar,
}

impl FreeSlots {
    fn new(n: usize) -> Self {
        Self {
            queue: Mutex::new((0..n).collect()),
            returned: Condvar::new(),
        }
    }

    fn pop(&self) -> Option<usize> {
        self.queue.lock().pop_front()
    }

    fn pop_timeout(&self, timeout: Duration) -> Option<usize> {
        let deadline = Instant::now() + timeout;
        let mut q = self.queue.lock();
        loop {
            if let Some(i) = q.pop_front() {
                return Some(i);
            }
            if self.returned.wait_until(&mut q, deadline).timed_out() {
                return q.pop_front();
            }
        }
    }

    fn push(&self, index: usize) {
        self.queue.lock().push_back(index);
        self.returned.notify_one();
    }

    fn len(&self) -> usize {
        self.queue.lock().len()
    }
}

#[cfg(test)]
mod free_slot_tests {
    use super::*;

    #[test]
    fn page_alloc_is_page_aligned_and_zeroed() {
        for size in [1usize, 4096, 16 << 20, (16 << 20) + 16] {
            let a = PageAlloc::new(size).expect("alloc");
            assert_eq!(a.ptr as usize % 4096, 0, "size {size}");
            // SAFETY: `a` owns `size` initialized (zeroed) bytes.
            let bytes = unsafe { std::slice::from_raw_parts(a.ptr, size) };
            assert!(bytes.iter().all(|&b| b == 0), "size {size}");
        }
        assert!(PageAlloc::new(0).is_err());
    }

    #[test]
    fn pop_until_empty_then_none() {
        let f = FreeSlots::new(2);
        assert_eq!((f.pop(), f.pop(), f.pop()), (Some(0), Some(1), None));
        assert_eq!(f.len(), 0);
    }

    #[test]
    fn pop_timeout_returns_none_after_deadline() {
        let f = FreeSlots::new(0);
        let t = Instant::now();
        assert_eq!(f.pop_timeout(Duration::from_millis(30)), None);
        assert!(t.elapsed() >= Duration::from_millis(30));
    }

    #[test]
    fn pop_timeout_wakes_when_a_slot_returns() {
        let f = Arc::new(FreeSlots::new(0));
        let g = Arc::clone(&f);
        let t = Instant::now();
        let h = std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(20));
            g.push(7);
        });
        assert_eq!(f.pop_timeout(Duration::from_secs(5)), Some(7));
        assert!(t.elapsed() < Duration::from_secs(2));
        h.join().expect("join");
    }

    #[test]
    fn many_waiters_each_get_one_returned_slot() {
        let f = Arc::new(FreeSlots::new(0));
        let waiters: Vec<_> = (0..4)
            .map(|_| {
                let f = Arc::clone(&f);
                std::thread::spawn(move || f.pop_timeout(Duration::from_secs(5)))
            })
            .collect();
        std::thread::sleep(Duration::from_millis(20));
        for i in 0..4 {
            f.push(i);
        }
        let mut got: Vec<_> = waiters
            .into_iter()
            .map(|h| h.join().expect("join").expect("slot"))
            .collect();
        got.sort_unstable();
        assert_eq!(got, vec![0, 1, 2, 3]);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn pool_checkout_return() {
        let dev = Arc::new(
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device"),
        );

        let pool = MemoryPool::new(&dev, 4096, 4).unwrap();
        assert_eq!(pool.available(), 4);
        assert_eq!(pool.total(), 4);

        // Checkout 3 buffers.
        let buf1 = pool.checkout().unwrap();
        let buf2 = pool.checkout().unwrap();
        let buf3 = pool.checkout().unwrap();
        assert_eq!(pool.available(), 1);

        // Buffers have valid lkeys.
        assert_ne!(buf1.lkey(), 0);
        assert_eq!(buf1.len(), 4096);

        // Addresses are distinct and at correct offsets.
        assert_eq!(buf2.addr() - buf1.addr(), 4096);
        assert_eq!(buf3.addr() - buf2.addr(), 4096);

        // Return by dropping.
        drop(buf2);
        assert_eq!(pool.available(), 2);

        drop(buf1);
        drop(buf3);
        assert_eq!(pool.available(), 4);
    }

    // A slot that outlives the pool's owner and the caller's device
    // handle (a parked ALLOC slot after its transport is torn down) must
    // still deregister against a live PD and context.
    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn slot_outliving_pool_and_device_is_sound() {
        let dev = Arc::new(
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device"),
        );
        let pool = MemoryPool::with_access(&dev, 4096, 2, Access::LOCAL).unwrap();
        let slot = pool.checkout().unwrap();
        drop(pool);
        drop(dev);
        assert_eq!(slot.len(), 4096);
        drop(slot); // deregisters the MR, then closes the device
    }

    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn pool_exhaustion() {
        let dev = Arc::new(
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device"),
        );

        let pool = MemoryPool::new(&dev, 4096, 2).unwrap();
        let _b1 = pool.checkout().unwrap();
        let _b2 = pool.checkout().unwrap();
        assert!(pool.checkout().is_none()); // Pool exhausted.
    }

    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn pool_write_data() {
        let dev = Arc::new(
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device"),
        );

        let pool = MemoryPool::new(&dev, 1024, 2).unwrap();
        let mut buf = pool.checkout().unwrap();

        // Write data into the buffer.
        let data = b"Hello RDMA!";
        buf.as_mut_slice()[..data.len()].copy_from_slice(data);
        assert_eq!(&buf.as_slice()[..data.len()], data);
    }

    // `checkout` leaves a reused slot as its previous user left it (callers
    // expose only bytes they wrote, see s3_rdma's
    // `reused_slot_never_exposes_earlier_bytes`); `checkout_zeroed` clears
    // it. A single-slot pool forces reuse of the same memory.
    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn checkout_zeroed_clears_a_reused_slot() {
        let dev = Arc::new(
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device"),
        );

        let pool = MemoryPool::new(&dev, 1024, 1).unwrap();

        let mut buf = pool.checkout().unwrap();
        let addr_first = buf.addr();
        buf.as_mut_slice().fill(0xAB);
        drop(buf);

        let buf = pool.checkout().unwrap();
        assert_eq!(
            buf.addr(),
            addr_first,
            "single-slot pool must reuse the same memory"
        );
        assert!(
            buf.as_slice().iter().all(|&b| b == 0xAB),
            "checkout leaves the slot as it was"
        );
        drop(buf);

        let buf = pool.checkout_zeroed().unwrap();
        assert_eq!(buf.addr(), addr_first);
        assert!(
            buf.as_slice().iter().all(|&b| b == 0),
            "checkout_zeroed must clear the slot"
        );
        assert!(pool.checkout_zeroed().is_none(), "no free slot left");
    }
}
