# CRIU io_uring VMA-to-FD Assignment Fix

**Date:** January 23, 2026  
**Status:** Implementation

---

## Problem Statement

CRIU's `dump_io_uring()` groups VMAs by inode, assuming each io_uring has a unique inode. However, Linux uses a **shared `anon_inode`** for all io_urings in a process.

**Result:** When a process has multiple io_urings (e.g., libuv creates two), all VMAs get assigned to the first fd, leaving other io_urings without memory mappings.

---

## Current Behavior (Bug)

```
fd 4: io_uring with 64 entries, inode 15288
fd 5: io_uring with 256 entries, inode 15288  ← SAME INODE!

VMAs (all inode 15288):
  16KB @ 0x7f223349a000 - sqe for 256 entries
  12KB @ 0x7f22334fa000 - ring for 256 entries
   4KB @ 0x7f22334fd000 - sqe for 64 entries
   4KB @ 0x7f2233d81000 - ring for 64 entries

CRIU assigns ALL to fd 4 → fd 5 has no memory mappings → crash
```

---

## Fix Approach

**Match VMAs to fds by SIZE, not by inode.**

### io_uring Memory Layout

Each io_uring has two types of mappings:

1. **SQE array** (submission queue entries):
   - Size = `sq_entries × 64` bytes (sizeof(struct io_uring_sqe) = 64)
   - Mapped with `pgoff = 0x10000000` (IORING_OFF_SQES)

2. **Ring buffer** (SQ + CQ combined or separate):
   - Size varies, but roughly `sq_entries × 8 + cq_entries × 16 + overhead`
   - Mapped with `pgoff = 0x0` (IORING_OFF_SQ_RING)

### Algorithm

1. **Phase 1: Collect io_uring fd info**
   - Scan `/proc/pid/fd` for all io_uring fds
   - Parse fdinfo to get `sq_entries` for each
   - Calculate expected sqe_size = `sq_entries × 64`

2. **Phase 2: Match VMAs to fds**
   - For each VMA:
     - If `pgoff == 0x10000000` (SQE mapping):
       - Match to fd where `vma_size == fd.sq_entries × 64`
     - If `pgoff == 0x0` (Ring mapping):
       - Match to fd based on remaining unmatched fd

3. **Phase 3: Assign VMAs**
   - Store VMA addresses in the correct fd's entry

---

## Implementation

### File: `$HOME/personal/criu/criu/io_uring.c`

### Key Changes

1. Add struct to track fd info before VMA processing:
```c
struct io_uring_fd_info {
    int fd;
    uint32_t sq_entries;
    uint32_t cq_entries;
    size_t expected_sqe_size;
    int sqe_vma_assigned;
    int ring_vma_assigned;
};
```

2. Pre-scan all io_uring fds and calculate expected sizes

3. Match VMAs by size instead of inode

---

## Test Case

- libuv creates two io_urings:
  - `iou` (fd 4): 64 SQ entries → sqe_size = 4096 bytes
  - `ctl` (fd 5): 256 SQ entries → sqe_size = 16384 bytes

- After fix:
  - 4KB SQE VMA → assigned to fd 4
  - 16KB SQE VMA → assigned to fd 5
  - Ring VMAs matched accordingly

---

## Success Criteria

1. Both fd 4 and fd 5 have their correct VMA addresses in dump
2. After restore, both io_urings have proper memory mappings
3. App runs without segfault
