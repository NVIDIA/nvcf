# libuv io_uring Backend - Comprehensive Analysis

## Executive Summary

libuv's io_uring backend (Linux 5.13+) uses io_uring as an ASYNC filesystem operation interface. It does NOT use io_uring for socket/FD polling (that's still epoll). Two separate io_uring rings exist: one for fast-path filesystem ops and one for epoll_ctl operations.

After CRIU restore, the current `uv_loop_fork()` recreates the io_uring rings, but the mmap'd memory regions from the old rings become invalid (kernel FDs are different in new PID), leaving kernel pointers stale.

---

## 1. io_uring Initialization

### Setup Function: `uv__iou_init()` (line 502-621)

**Called from:** `uv__platform_loop_init()` at lines 648-649

```c
uv__iou_init(loop->backend_fd, &lfields->iou, 64, UV__IORING_SETUP_SQPOLL);
uv__iou_init(loop->backend_fd, &lfields->ctl, 256, 0);
```

**Two separate rings:**
1. **`lfields->iou`** (64 entries, SQPOLL mode):
   - Flags: `UV__IORING_SETUP_SQPOLL` (kernel thread polls SQ)
   - sq_thread_idle: 10ms (line 530)
   - Used for filesystem operations (read, write, fsync, openat, statx, etc.)

2. **`lfields->ctl`** (256 entries, normal mode):
   - Flags: 0 (normal blocking enter)
   - Used for epoll_ctl operations via `IORING_OP_EPOLL_CTL`

### Ring Setup Process

```c
// Line 526-535: Setup io_uring_setup syscall
ringfd = uv__io_uring_setup(entries, &params);
if (ringfd == -1) return;

// Lines 537-549: Feature checks
// Requires: IORING_FEAT_RSRC_TAGS, IORING_FEAT_SINGLE_MMAP, IORING_FEAT_NODROP

// Lines 551-555: Calculate mmap sizes
sqlen = params.sq_off.array + params.sq_entries * sizeof(uint32_t);
cqlen = params.cq_off.cqes + params.cq_entries * sizeof(struct uv__io_uring_cqe);
maxlen = sqlen < cqlen ? cqlen : sqlen;  // Single mmap covers both
sqelen = params.sq_entries * sizeof(struct uv__io_uring_sqe);

// Lines 557-569: Two mmap calls
sq = mmap(0, maxlen, PROT_READ | PROT_WRITE, MAP_SHARED | MAP_POPULATE, ringfd, 0);
sqe = mmap(0, sqelen, PROT_READ | PROT_WRITE, MAP_SHARED | MAP_POPULATE, ringfd, 0x10000000ull);

// Lines 574-584: For SQPOLL mode, add ringfd to epoll
epoll_ctl(epollfd, EPOLL_CTL_ADD, ringfd, &e);  // Watch for CQ events

// Lines 586-610: Store pointers and metadata
```

### Ring Metadata Structure: `struct uv__iou` (uv-common.h:400-419)

```c
struct uv__iou {
  uint32_t* sqhead;      // Kernel writes submission queue head
  uint32_t* sqtail;      // User writes submission queue tail
  uint32_t* sqarray;     // Submission queue array (mapping tail → sqe)
  uint32_t sqmask;       // Mask for ring indexing (entries - 1)
  uint32_t* sqflags;     // Kernel sets NEED_WAKEUP, CQ_OVERFLOW flags
  uint32_t* cqhead;      // User writes completion queue head
  uint32_t* cqtail;      // Kernel writes completion queue tail
  uint32_t cqmask;       // Mask for completion ring indexing
  void* sq;              // mmap'd SQ ring (shared with kernel)
  void* cqe;             // Pointer to CQ entries within sq
  void* sqe;             // mmap'd SQE ring (separate mmap)
  size_t sqlen;          // Size of SQ ring mmap
  size_t cqlen;          // Size of CQ ring mmap
  size_t maxlen;         // Max of sqlen/cqlen
  size_t sqelen;         // Size of SQE ring mmap
  int ringfd;            // io_uring ring file descriptor
  uint32_t in_flight;    // Count of submitted operations
  uint32_t flags;        // Feature flags (MKDIRAT_SYMLINKAT_LINKAT)
};
```

**Key insight:** All pointer fields (sqhead, sqtail, sqarray, sqflags, cqhead, cqtail, cqe, sqe) point into the mmap'd regions. After fork/restore, these pointers are INVALID.

---

## 2. io_uring for Polling (INCOMPLETE PICTURE)

### Key Finding: io_uring is NOT used for socket/FD polling

- Socket/FD polling is still done via `epoll_pwait()` (line 1526)
- io_uring is ONLY used for:
  1. Filesystem operations (read, write, fsync, open, stat, etc.)
  2. Epoll_ctl operations (via IORING_OP_EPOLL_CTL at line 1285)

### How io_uring completion events are retrieved

In `uv__poll_io_uring()` (line 1163-1237), after epoll returns:

```c
if (fd == iou->ringfd) {
  uv__poll_io_uring(loop, iou);  // Line 1568
  have_iou_events = 1;
  continue;
}

// uv__poll_io_uring polls the completion ring
head = *iou->cqhead;  // Line 1175
tail = atomic_load_explicit((_Atomic uint32_t*) iou->cqtail, memory_order_acquire);
// Process completions from head to tail
atomic_store_explicit((_Atomic uint32_t*) iou->cqhead, tail, memory_order_release);

// If CQ overflow occurred, call io_uring_enter to get more events
if (flags & UV__IORING_SQ_CQ_OVERFLOW)
  rc = uv__io_uring_enter(iou->ringfd, 0, 0, UV__IORING_ENTER_GETEVENTS);
```

---

## 3. Futex Wait Mechanism

### The io_uring_enter syscall (line 437-451)

```c
int uv__io_uring_enter(int fd, unsigned to_submit, unsigned min_complete, unsigned flags) {
  return syscall(__NR_io_uring_enter, fd, to_submit, min_complete, flags, NULL, 0L);
}
```

**How it's used:**

1. **Line 838 (submit with wakeup):**
   ```c
   if (flags & UV__IORING_SQ_NEED_WAKEUP)
     uv__io_uring_enter(iou->ringfd, 0, 0, UV__IORING_ENTER_SQ_WAKEUP);
   ```
   Wakes the kernel SQ polling thread (SQPOLL mode).

2. **Line 1227, 1334 (wait for completions):**
   ```c
   rc = uv__io_uring_enter(iou->ringfd, 0, 0, UV__IORING_ENTER_GETEVENTS);
   ```
   Enters kernel to wait for completion events.

**The 128 blocked threads:**
These are threads blocked in `epoll_pwait()` at line 1526, NOT in io_uring_enter. The issue is that after CRIU restore, the io_uring ring FDs are invalid but still registered with epoll, causing epoll to return spurious events or hang.

---

## 4. What `uv_loop_fork()` Does Today

### Flow (unix/loop.c:132-162)

```c
int uv_loop_fork(uv_loop_t* loop) {
  int err;
  unsigned int i;
  uv__io_t* w;

  err = uv__io_fork(loop);          // Closes epoll, deletes io_uring, reinits
  err = uv__async_fork(loop);       // Reinitialize async pipes
  err = uv__signal_loop_fork(loop); // Reinitialize signal handlers

  // Re-arm all file descriptor watchers
  for (i = 0; i < loop->nwatchers; i++) {
    w = loop->watchers[i];
    if (w != NULL && w->pevents != 0 && uv__queue_empty(&w->watcher_queue)) {
      w->events = 0;  // Force re-registration in uv__io_poll
      uv__queue_insert_tail(&loop->watcher_queue, &w->watcher_queue);
    }
  }
  return 0;
}
```

### The uv__io_fork implementation (linux.c:655-672)

```c
int uv__io_fork(uv_loop_t* loop) {
  int err;
  struct watcher_list* root;

  root = uv__inotify_watchers(loop)->rbh_root;

  uv__close(loop->backend_fd);      // Close epoll FD
  loop->backend_fd = -1;

  uv__platform_loop_delete(loop);   // Delete OLD io_uring rings
  err = uv__platform_loop_init(loop);     // Reinit: new epoll, new io_uring rings
  if (err) return err;

  return uv__inotify_fork(loop, root);
}
```

### Platform-specific delete/init (linux.c:675-687, 634-652)

```c
// DELETE: munmap + close
void uv__platform_loop_delete(uv_loop_t* loop) {
  uv__loop_internal_fields_t* lfields = uv__get_internal_fields(loop);
  uv__iou_delete(&lfields->ctl);
  uv__iou_delete(&lfields->iou);
  if (loop->inotify_fd != -1) {
    uv__io_stop(loop, &loop->inotify_read_watcher, POLLIN);
    uv__close(loop->inotify_fd);
    loop->inotify_fd = -1;
  }
}

// INIT: new epoll, new io_uring
int uv__platform_loop_init(uv_loop_t* loop) {
  uv__loop_internal_fields_t* lfields = uv__get_internal_fields(loop);
  lfields->ctl.ringfd = -1;
  lfields->iou.ringfd = -1;
  loop->backend_fd = epoll_create1(O_CLOEXEC);
  if (loop->backend_fd == -1) return UV__ERR(errno);
  uv__iou_init(loop->backend_fd, &lfields->iou, 64, UV__IORING_SETUP_SQPOLL);
  uv__iou_init(loop->backend_fd, &lfields->ctl, 256, 0);
  return 0;
}
```

**Key comment at line 664:**
```c
/* TODO(bnoordhuis) Loses items from the submission and completion rings. */
```

This is EXACTLY what's broken after CRIU restore - pending operations are lost.

---

## 5. io_uring Teardown Path

### Function: `uv__iou_delete()` (line 624-631)

```c
static void uv__iou_delete(struct uv__iou* iou) {
  if (iou->ringfd != -1) {
    munmap(iou->sq, iou->maxlen);    // Unmap SQ/CQ rings
    munmap(iou->sqe, iou->sqelen);   // Unmap SQE ring
    uv__close(iou->ringfd);          // Close ring FD
    iou->ringfd = -1;
  }
}
```

**Called from:**
1. `uv__platform_loop_delete()` (line 679-680)
2. Via `uv__io_fork()` during loop fork

---

## 6. io_uring Ring Structures and Memory Layout

### Submission Queue Layout

```
mmap'ed region (ringfd, offset 0, maxlen bytes):
  [SQ headers: sqhead, sqtail, ring_mask, ring_entries, flags, dropped]
  [SQ array: indices 0 to entries-1, each pointing to an SQE slot]
  [CQ headers: cqhead, cqtail, ring_mask, ring_entries, overflow, (reserved)]
  [CQ entries: array of struct uv__io_uring_cqe]

mmap'ed region (ringfd, offset 0x10000000, sqelen bytes):
  [SQE entries: array of struct uv__io_uring_sqe, 64 bytes each]
```

### Submission Queue Entry (line 201-237)

```c
struct uv__io_uring_sqe {
  uint8_t opcode;          // IORING_OP_*
  uint8_t flags;           // SQE flags
  uint16_t ioprio;         // I/O priority
  int32_t fd;              // File descriptor
  uint64_t off;            // File offset
  uint64_t addr;           // Pointer to data
  uint32_t len;            // Length
  uint32_t rw_flags;       // Read/write flags
  uint64_t user_data;      // User-supplied data
  uint32_t buf_index;      // Buffer index
  // ... (unused fields)
};  // 64 bytes total
```

### Completion Queue Entry (line 193-197)

```c
struct uv__io_uring_cqe {
  uint64_t user_data;      // Echo of SQE user_data
  int32_t res;             // Result (< 0 means error)
  uint32_t flags;          // CQE flags
};  // 16 bytes total
```

---

## 7. How FD Watchers Are Registered

### Entry point: `uv__io_start()` → FD is watched via io_uring or epoll

**Sequence:**
1. Application calls `uv_poll()` or `uv_read()` with a TCP socket
2. This calls `uv__io_start()` internally
3. Watcher is queued in `loop->watcher_queue`
4. In `uv__io_poll()` (line 1481-1496), watchers are processed:

```c
while (!uv__queue_empty(&loop->watcher_queue)) {
  q = uv__queue_head(&loop->watcher_queue);
  w = uv__queue_data(q, uv__io_t, watcher_queue);
  uv__queue_remove(q);

  op = EPOLL_CTL_MOD;
  if (w->events == 0)
    op = EPOLL_CTL_ADD;

  w->events = w->pevents;
  e.events = w->pevents;
  e.data.fd = w->fd;

  uv__epoll_ctl_prep(epollfd, ctl, &prep, op, w->fd, &e);  // Line 1495
}
```

### Two modes in `uv__epoll_ctl_prep()` (line 1240-1298)

**Mode 1: Direct epoll (if ctl->ringfd == -1):**
```c
if (ctl->ringfd == -1) {
  if (!epoll_ctl(epollfd, op, fd, e))
    return;
  // ... fallback handling
}
```

**Mode 2: io_uring EPOLL_CTL (if ctl->ringfd != -1):**
```c
else {
  uv__iou_ensure_sqarray(ctl);
  mask = ctl->sqmask;
  slot = (*ctl->sqtail)++ & mask;
  
  sqe = &ctl->sqe[slot];
  memset(sqe, 0, sizeof(*sqe));
  sqe->addr = (uintptr_t) pe;          // Pointer to epoll_event
  sqe->fd = epollfd;
  sqe->len = op;                       // EPOLL_CTL_ADD/MOD/DEL
  sqe->off = fd;                       // Target FD
  sqe->opcode = UV__IORING_OP_EPOLL_CTL;
  
  if ((*ctl->sqhead & mask) == (*ctl->sqtail & mask))
    uv__epoll_ctl_flush(epollfd, ctl, events);
}
```

**Key insight:** Socket FD watching goes through epoll (accessed via io_uring's EPOLL_CTL op), not directly through io_uring's IORING_OP_POLL_ADD.

---

## 8. Critical State That Gets Lost After Fork/Restore

### Pointer Invalidation After CRIU Restore

After `CRIU restore`:
1. New process has NEW epoll FD
2. New process has NEW io_uring ring FDs
3. OLD mmap'ed regions are from OLD ring FDs (invalid kernel references)
4. Pointers in `struct uv__iou` still point to OLD mmap'd regions
5. Reading/writing to OLD regions causes kernel to use invalid state

### What needs to happen in uv_loop_fork():

```
Current state (BROKEN):
  1. Close backend_fd (epoll)
  2. Delete io_uring rings (munmap, close ringfd)
  3. Reinit epoll (new FD)
  4. Reinit io_uring (new ringfd, new mmap)
  5. Rearm watchers (queue them for re-registration)
  
  PROBLEM: Pending submissions/completions in old rings are lost
           New ring FDs don't have old submissions
           App code expecting completions never gets them
```

### Concrete failure scenario:

1. Before checkpoint: App submits 10 filesystem reads via io_uring
2. CRIU checkpoints process (submissions frozen in old ring)
3. Restore happens: `uv_loop_fork()` creates NEW rings
4. Old submissions never executed (lost)
5. App waits forever for completion events that never arrive

---

## 9. The io_uring Ring Lifecycle Summary

```
INITIALIZATION:
  uv__io_uring_setup(entries, &params)
  │
  ├─ Syscall #425: Returns ringfd
  ├─ Check params.features for required bits
  │
  mmap(ringfd, 0, maxlen)           → sq (SQ/CQ combined ring)
  mmap(ringfd, 0x10000000, sqelen)  → sqe (SQE entries)
  │
  └─ Store pointers: sqhead, sqtail, sqarray, cqhead, cqtail, sqflags, cqe, sqe

SUBMISSION:
  uv__iou_get_sqe(iou, loop, req)
  │
  ├─ Check SQ not full: (head & mask) != ((tail + 1) & mask)
  ├─ Get next slot: tail & mask
  ├─ Get SQE: sqe = &iou->sqe[slot]
  └─ Populate user_data with req pointer

  uv__iou_submit(iou)
  │
  ├─ Increment sqtail (atomically)
  ├─ Check sqflags for NEED_WAKEUP
  └─ If set, syscall #426: io_uring_enter(ringfd, 0, 0, IORING_ENTER_SQ_WAKEUP)

POLLING:
  epoll_pwait(epollfd, events, 1024, timeout)
  │
  ├─ If ringfd event:
  │   └─ Read CQ ring: *iou->cqhead to *iou->cqtail
  │       Process completions, increment cqhead
  │
  └─ If CQ overflow: io_uring_enter(ringfd, 0, 0, IORING_ENTER_GETEVENTS)

TEARDOWN:
  uv__iou_delete(iou)
  │
  ├─ munmap(iou->sq, maxlen)
  ├─ munmap(iou->sqe, sqelen)
  └─ close(iou->ringfd)
```

---

## Key Files and Line Numbers Reference

| Component | File | Lines | Function |
|-----------|------|-------|----------|
| Syscall wrappers | `linux.c` | 432-451 | `uv__io_uring_setup/enter/register` |
| Ring init | `linux.c` | 502-621 | `uv__iou_init()` |
| Ring delete | `linux.c` | 624-631 | `uv__iou_delete()` |
| Platform init | `linux.c` | 634-652 | `uv__platform_loop_init()` |
| Platform delete | `linux.c` | 675-687 | `uv__platform_loop_delete()` |
| Fork handler | `linux.c` | 655-672 | `uv__io_fork()` |
| SQ array reinit | `linux.c` | 757-783 | `uv__iou_ensure_sqarray()` |
| Get SQE | `linux.c` | 786-824 | `uv__iou_get_sqe()` |
| Submit | `linux.c` | 827-841 | `uv__iou_submit()` |
| Poll CQ | `linux.c` | 1163-1237 | `uv__poll_io_uring()` |
| Epoll ctl prep | `linux.c` | 1240-1298 | `uv__epoll_ctl_prep()` |
| Epoll ctl flush | `linux.c` | 1301-1361 | `uv__epoll_ctl_flush()` |
| Main poll | `linux.c` | 1425-1620 | `uv__io_poll()` |
| Loop fork | `loop.c` | 132-162 | `uv_loop_fork()` |
| CRIU detection | `core.c` | 415-446 | CRIU restore marker check |
| struct uv__iou | `uv-common.h` | 400-419 | Definition |
| struct fields | `uv-common.h` | 422-431 | `uv__loop_internal_fields_s` |

---

## Summary

libuv's io_uring backend uses TWO separate rings:
- **iou ring** (64 entries, SQPOLL): Filesystem operations
- **ctl ring** (256 entries, normal): Epoll control operations via IORING_OP_EPOLL_CTL

After CRIU restore, `uv_loop_fork()` tears down and reinitializes both rings, but:
1. Old mmap'd regions become invalid (old ring FDs from old PID)
2. Pointer fields in `struct uv__iou` must be reinitialized
3. Pending submissions are lost (TODO comment in code)
4. File descriptor watchers must be re-registered

The 128 blocked threads are stuck in `epoll_pwait()` waiting for events from invalid ring FDs.

