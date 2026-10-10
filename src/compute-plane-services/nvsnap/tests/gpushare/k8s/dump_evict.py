# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Evict every file under a dump directory from the page cache, then report
# how much of it is still resident, so a restore reads it from disk.
#   python3 dump_evict.py <dir>
import ctypes, mmap, os, sys

libc = ctypes.CDLL(None, use_errno=True)
libc.mmap.restype = ctypes.c_void_p
libc.mmap.argtypes = [ctypes.c_void_p, ctypes.c_size_t, ctypes.c_int, ctypes.c_int, ctypes.c_int, ctypes.c_long]
libc.munmap.argtypes = [ctypes.c_void_p, ctypes.c_size_t]
libc.mincore.argtypes = [ctypes.c_void_p, ctypes.c_size_t, ctypes.c_void_p]

total = resident = 0
for d, _, files in os.walk(sys.argv[1]):
    for f in files:
        path = os.path.join(d, f)
        if not os.path.isfile(path) or os.path.islink(path):  # sockets, fifos (vLLM's TMPDIR)
            continue
        fd = os.open(path, os.O_RDONLY)
        os.posix_fadvise(fd, 0, 0, os.POSIX_FADV_DONTNEED)
        n = os.fstat(fd).st_size
        if n:
            p = libc.mmap(None, n, mmap.PROT_READ, mmap.MAP_SHARED, fd, 0)
            vec = (ctypes.c_ubyte * ((n + mmap.PAGESIZE - 1) // mmap.PAGESIZE))()
            if p not in (None, ctypes.c_void_p(-1).value) and libc.mincore(p, n, vec) == 0:
                resident += sum(v & 1 for v in vec) * mmap.PAGESIZE
            if p not in (None, ctypes.c_void_p(-1).value):
                libc.munmap(p, n)
        total += n
        os.close(fd)
print("dump %.1f GB, resident in page cache after eviction: %.2f%%" % (total / 1e9, 100.0 * resident / max(total, 1)))
