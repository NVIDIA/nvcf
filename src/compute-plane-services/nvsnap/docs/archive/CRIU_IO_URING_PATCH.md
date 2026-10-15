# CRIU io_uring VMA-to-FD Size-Based Matching Patch

**Date:** January 23, 2026  
**Status:** Implementation needed

---

## Problem

CRIU's `dump_io_uring()` groups VMAs by inode, but Linux uses a **shared anon_inode** for all io_urings in a process. When libuv creates two io_urings (fd 4 and fd 5), all VMAs get assigned to fd 4, leaving fd 5 without memory mappings.

## Evidence from Logs

```
Pre-scan: fd=4 sq_entries=1 expected_sqe_size=64
Pre-scan: fd=5 sq_entries=1 expected_sqe_size=64
SQE VMA size=16384 -> NO MATCH FOUND
SQE VMA size=4096 -> NO MATCH FOUND
```

The `sq_entries=1` is wrong because we're reading `sq_mask + 1` where `sq_mask=0`. The actual sizes (16384 and 4096) indicate 256 and 64 entries respectively.

## Root Cause

1. fdinfo parsing reads `SqMask` which is 0 in some cases
2. Should read `SqSize` / `CqSize` fields (newer kernels) or compute from VMA sizes

## Required Changes

### 1. Add fields to `struct io_uring_fdinfo`
```c
uint32_t sq_entries;  /* From SqSize in fdinfo */
uint32_t cq_entries;  /* From CqSize in fdinfo */
```

### 2. Update `parse_io_uring_fdinfo()` to read SqSize/CqSize
```c
else if (!strcmp(buf, "SqSize")) info->sq_entries = strtoul(value, NULL, 0);
else if (!strcmp(buf, "CqSize")) info->cq_entries = strtoul(value, NULL, 0);
```

### 3. Update size calculation in prescan
```c
if (fdinfo.sq_entries > 0) {
    fd_info[count].sq_entries = fdinfo.sq_entries;
} else {
    // Fallback: compute from mask or default
    fd_info[count].sq_entries = fdinfo.sq_mask > 0 ? fdinfo.sq_mask + 1 : 64;
}
fd_info[count].expected_sqe_size = fd_info[count].sq_entries * 64;
```

### 4. Match VMAs by size
```c
if (vma->e->pgoff == IORING_OFF_SQES) {
    matched_fd = find_fd_by_sqe_size(fd_info, num_fds, vma_size);
}
```

---

## Cleaner Approach

Instead of patching incrementally, I should:

1. Get a clean copy of the original `io_uring.c`
2. Apply changes in a single, reviewable commit
3. Test compilation in Docker before deploying

---

## Action Items

1. [ ] Reset `io_uring.c` to a known good state
2. [ ] Apply the fix as a clean patch
3. [ ] Build and test
