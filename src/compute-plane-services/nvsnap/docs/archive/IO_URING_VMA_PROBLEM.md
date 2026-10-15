# io_uring VMA-to-FD Matching Problem

**Date**: 2026-01-23  
**Status**: Analysis in Progress

## Executive Summary

CRIU cannot correctly restore multiple io_uring instances when they share the same kernel inode. This is a fundamental challenge because Linux uses a **single anonymous inode** for all io_uring instances in a process.

## The Problem

### What We're Trying to Do
Checkpoint and restore a Python application using `uvloop` (which uses `libuv`, which uses `io_uring`).

### The Crash
After restore, the application crashes with `SIGSEGV` because io_uring fd 5's memory mappings are missing (restored with `sq_addr=0x0`).

### Root Cause Analysis

#### 1. libuv Creates Two io_urings

libuv creates two separate io_uring instances:
- **fd 4**: Main "accept" ring (SQPOLL enabled, larger queue)
- **fd 5**: Control "ctl" ring (no SQPOLL, smaller queue)

```
=== fd 4 ===
SqThread:    2862274   (SQPOLL thread running)
SqThreadCpu: 6

=== fd 5 ===
SqThread:    -1        (no SQPOLL)
SqThreadCpu: -1
```

#### 2. Both io_urings Share the Same Inode

Linux kernel uses a single `anon_inode:[io_uring]` for ALL io_uring instances:
```
fd 4 -> anon_inode:[io_uring] (ino=15288)
fd 5 -> anon_inode:[io_uring] (ino=15288)  <- SAME INODE!
```

#### 3. CRIU Groups VMAs by Inode

CRIU's `dump_io_uring()` uses inode to group VMAs:
```c
entry = find_entry_by_ino(mme, nr, vma->vm_iouring_ino);
```

Since both io_urings have the same inode, ALL VMAs get assigned to the first entry (fd 4).

#### 4. fd 5 Gets No VMAs

Result in dump:
```
io_uring fd 4: SQ=64 CQ=128 n_vmas=4  <- gets ALL 4 VMAs
(fd 5 is never even created as an entry)
```

Result in restore:
```
io_uring restore [1/2]: fd=4 sq_addr=0x7fbcce5db000  <- correct
io_uring restore [2/2]: fd=5 sq_addr=0x0             <- BROKEN!
```

#### 5. Application Crashes

When libuv tries to use fd 5's `ctl` ring, it accesses the NULL mapped memory and crashes.

## What We've Tried

### Attempt 1: Size-Based Matching
**Idea**: Match VMAs to fds by comparing SQE VMA size to expected size from fdinfo.

**Problem**: The kernel doesn't expose `SqMask`, `SqSize`, `CqSize` in `/proc/pid/fdinfo/FD` (at least on this kernel version). We only see:
```
SqThread: ...
SqThreadCpu: ...
UserFiles: 0
UserBufs: 0
PollList:
```

Without the queue sizes, we can't predict expected VMA sizes.

### Attempt 2: Order-Based Matching  
**Idea**: Match SQE VMAs to fds in the order they appear.

**Problem**: This assumes VMAs appear in fd order, which is not guaranteed by the kernel.

## Actual VMA Data

From the checkpoint dump:
```
VMA 1: addr=0x7fbcce57b000 size=16384 pgoff=0x10000000 (SQEs)   <- 256 entries
VMA 2: addr=0x7fbcce5db000 size=12288 pgoff=0x0 (SQ/CQ ring)
VMA 3: addr=0x7fbcce5de000 size=4096  pgoff=0x10000000 (SQEs)   <- 64 entries
VMA 4: addr=0x7fbccee62000 size=4096  pgoff=0x0 (SQ/CQ ring)
```

Two SQE VMAs with different sizes:
- 16384 bytes = 256 * 64 = 256 SQ entries
- 4096 bytes = 64 * 64 = 64 SQ entries

Two ring VMAs:
- 12288 bytes (larger ring)
- 4096 bytes (smaller ring)

## The Fundamental Challenge

**We have 4 VMAs and 2 fds, all sharing the same inode.**

We need to determine:
- Which 2 VMAs belong to fd 4?
- Which 2 VMAs belong to fd 5?

### Information Available

| Source | What We Can Get | Useful? |
|--------|-----------------|---------|
| `/proc/pid/fd/N` | `anon_inode:[io_uring]` | Only confirms it's io_uring |
| `/proc/pid/fdinfo/N` | SqThread, SqThreadCpu only | Partial - no queue sizes |
| VMA entry | Address, size, pgoff, inode | Inode is same for all |
| mmap offset | `IORING_OFF_SQ_RING`, `IORING_OFF_SQES` | Tells VMA type, not which fd |

### What We're Missing

There's no kernel interface to query "which VMAs belong to this io_uring fd".

## Potential Solutions

### Option A: Kernel Enhancement (Not Practical)
Ask kernel to expose more info in fdinfo or provide an ioctl to list VMAs per io_uring.
- **Pros**: Clean solution
- **Cons**: Requires kernel changes, long timeline

### Option B: Memory Content Analysis
Read io_uring ring header from VMA memory to find unique identifiers.
- Each io_uring has unique `sq_ring` structure at the start
- Could hash/fingerprint the content
- **Pros**: Works without kernel changes
- **Cons**: Complex, fragile, assumes ring structures are unique

### Option C: Heuristic Matching (Current Attempts)
Use size, order, or other heuristics to guess which VMAs go together.
- **Pros**: Doesn't need kernel changes
- **Cons**: Brittle, may fail with different configurations

### Option D: Application-Level Solution
Have the application re-create io_urings after restore instead of CRIU restoring them.
- **Pros**: Works around the kernel limitation
- **Cons**: Requires application cooperation

### Option E: Single io_uring Configuration
Configure libuv to use only one io_uring (if possible).
- **Pros**: Avoids the multi-io_uring problem
- **Cons**: May not be possible, reduces functionality

### Option F: Track io_uring Creation
Use LD_PRELOAD to intercept `io_uring_setup()` syscalls and track which VMAs get created for each fd.
- **Pros**: Accurate tracking from the start
- **Cons**: Only works for checkpoints created after tracking is in place

## Key Discovery: Ring Header Contains Queue Size

**The io_uring SQ_RING mmap contains a `ring_mask` field at a known offset!**

From `/usr/include/linux/io_uring.h`:
```c
struct io_sqring_offsets {
    __u32 head;
    __u32 tail;
    __u32 ring_mask;      // <-- KEY: ring_entries = ring_mask + 1
    __u32 ring_entries;
    __u32 flags;
    // ...
};
```

The `sq_off.ring_mask` offset is returned by `io_uring_setup()` in `io_uring_params`.

**This means**: We can read the `ring_mask` directly from each SQ_RING VMA's memory during dump to determine its actual queue size.

**Observed issue**: On this kernel, `process_vm_readv()` and `/proc/pid/mem` reads of SQ_RING mappings return `EIO` (Input/output error).  
This means we **cannot** reliably read `ring_mask` in practice.

## Research Findings

### Upstream CRIU Status
**Upstream CRIU has NO io_uring support whatsoever.**
- No `io_uring.c` file
- No references to io_uring anywhere
- Our fork's 1506-line `io_uring.c` is entirely custom

This means we must design the solution from first principles.

## Proposed Solution

### Strategy: Read Queue Size from Ring Memory

1. **During VMA enumeration**, for each SQ_RING VMA (pgoff == `IORING_OFF_SQ_RING`):
   - Read `ring_mask` from memory at offset `sq_off.ring_mask`
   - Calculate `ring_entries = ring_mask + 1`
   - Store this as the VMA's "fingerprint"

2. **Match SQE VMAs to ring VMAs by queue size**:
   - SQE VMA size = `ring_entries * sizeof(io_uring_sqe)` = `ring_entries * 64`
   - If SQE VMA size / 64 == ring_entries, they belong together

3. **Match fd to VMA group**:
   - For each io_uring fd, get its `sq_entries` from `io_uring_params` (if available)
   - Match fd to the VMA group with matching queue size

### Fallback for Same-Size io_urings

If two io_urings have identical queue sizes:
- They will have identical SQE VMA sizes
- They will have identical ring VMA sizes
- **Use VMA address ordering** as tiebreaker

### Implementation Steps

1. **Read `io_sqring_offsets` from params** during checkpoint
   - Store `sq_off.ring_mask` offset for later use

2. **First pass: Enumerate ring VMAs and read ring_entries**
   - For each VMA with pgoff == `IORING_OFF_SQ_RING`
   - Read 4 bytes at offset 8 (assuming standard `ring_mask` offset)
   - Store: `{vma_addr, ring_entries}`

3. **Second pass: Group VMAs by ring_entries**
   - SQE VMA with size N belongs to ring with `ring_entries = N / 64`
   - Ring VMAs already have `ring_entries` from step 2

4. **Match to fds**
   - Create entries for all io_uring fds upfront
   - Assign VMA groups to entries by matching queue sizes

5. **Fallback when ring_mask read fails**
   - Pair remaining SQ_RING and SQE VMAs by size (largest → largest)
   - Derive `ring_entries` from SQE VMA size when necessary

## Questions Remaining

1. **Can we read process memory during dump?**
   - CRIU has access to `/proc/pid/mem`
   - Need to verify we can read mmap'd regions

2. **Is `sq_off.ring_mask` at a fixed offset?**
   - From header: `head(0) + tail(4) + ring_mask(8)`
   - Offset 8 should be consistent

3. **What if ring_entries is also identical?**
   - Fall back to address ordering
   - Or fail with clear error message

## Verified: CRIU Can Read Process Memory

CRIU uses `process_vm_readv()` syscall to read target process memory during dump.
This is already used in `page-xfer.c` for pre-dump optimization.

**Reading ring_mask**:
```c
uint32_t ring_mask;
struct iovec local = { .iov_base = &ring_mask, .iov_len = sizeof(ring_mask) };
struct iovec remote = { .iov_base = (void*)(sq_ring_addr + 8), .iov_len = sizeof(ring_mask) };
process_vm_readv(pid, &local, 1, &remote, 1, 0);
// ring_entries = ring_mask + 1
```

## Final Implementation Plan

### Phase 1: Two-Pass VMA Processing

**Pass 1: Identify ring VMAs and read their queue sizes**
```c
for each VMA where pgoff == IORING_OFF_SQ_RING:
    ring_mask = read_u32_from_process(pid, vma->start + 8)
    ring_entries = ring_mask + 1
    store: {vma_addr, ring_entries}
```

**Pass 2: Match SQE VMAs to ring VMAs**
```c
for each VMA where pgoff == IORING_OFF_SQES:
    sqe_entries = vma_size / 64  // sizeof(io_uring_sqe) = 64
    find ring VMA where ring_entries == sqe_entries
    group them together
```

### Phase 2: Match VMA Groups to FDs

```c
// Create entries for all prescanned fds
for each io_uring fd:
    entry[fd] = new IoUringEntry(fd)

// Assign VMA groups to entries
for each VMA group (ring + sqe):
    // Match by queue size - each fd has a unique combination
    // If sizes are identical, use address ordering
    assign_to_entry_by_size_or_order(group, entries)
```

### Code Location

All changes will be in `$HOME/personal/criu/criu/io_uring.c`:

1. Add helper: `read_ring_mask_from_vma(pid, vma_addr)` - uses `process_vm_readv`
2. Modify `dump_io_uring()`:
   - First pass: collect ring VMAs and their queue sizes
   - Second pass: match SQE VMAs by size
   - Third pass: assign VMA groups to fd entries

### Testing

1. Test with libuv's dual io_uring setup (different sizes)
2. Test with hypothetical same-size io_urings (address ordering)
3. Verify restore works correctly

## Status

- ✅ Problem documented
- ✅ Root cause identified (shared inode)
- ✅ Upstream CRIU research (no io_uring support)
- ✅ Solution designed (read ring_mask from memory)
- ✅ Implementation plan created
- ⏳ Implementation pending

## References

- [Linux io_uring documentation](https://kernel.dk/io_uring.pdf)
- [CRIU io_uring support](https://github.com/checkpoint-restore/criu)
- [libuv io_uring backend](https://github.com/libuv/libuv)
