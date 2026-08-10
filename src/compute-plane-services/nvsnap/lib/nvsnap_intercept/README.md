# NVSNAP Interception Library

An LD_PRELOAD library for intercepting io_uring and libuv to enable CRIU checkpoint/restore of modern async applications.

## Purpose

CRIU (Checkpoint/Restore In Userspace) has limited support for:
- **io_uring**: Kernel-side state, SQPOLL threads, registered buffers
- **libuv/uvloop**: Internal C pointers that become stale after restore

This library intercepts these subsystems to:
1. Track io_uring instances and drain them before checkpoint
2. Track libuv loops and reinitialize them after restore
3. Enable checkpoint/restore of applications using uvloop (vLLM, SGLang, etc.)

**Note**: GPU/CUDA state is handled separately by `cuda-checkpoint` (NVIDIA's tool). This library only handles io_uring and libuv.

## Building

```bash
make
```

## Usage

```bash
LD_PRELOAD=/path/to/libnvsnap_intercept.so your_application
```

### Environment Variables

| Variable | Values | Description |
|----------|--------|-------------|
| `NVSNAP_LOG_LEVEL` | 0-5 | 0=off, 1=error, 2=warn, 3=info, 4=debug, 5=trace |
| `NVSNAP_LOG_FILE` | path or "stderr" | Where to write logs |
| `NVSNAP_ENABLED` | 0 or 1 | Disable interception entirely |

## How It Works

### io_uring Interception

```
Application → io_uring_setup() syscall
                    ↓
         Our intercept (via syscall hook)
                    ↓
         Track: fd, sq_entries, cq_entries, flags
                    ↓
         Before checkpoint: drain all pending I/O
         After restore: recreate rings with same params
```

### libuv Interception

```
Application (uvloop) → uv_loop_init()
                            ↓
                    Our intercept (via dlsym)
                            ↓
                    Track loop pointer
                            ↓
         After restore detected: call uv_loop_fork()
         to reinitialize kernel-side handles
```

### Restore Detection

The library detects restore via a marker file:
- `restore-entrypoint` creates `/var/run/nvsnap/.restored`
- Library checks for this file and triggers reinitialization

## Testing

```bash
# Quick test - verify library loads
make test-quick

# Test io_uring interception
make test-uring

# Test uvloop interception
make test-uvloop

# Test quiescence signal handling
make test-quiesce
```

## Integration with NVSNAP

This library is bundled in the NVSNAP agent image and injected into containers via:

1. Init container copies `libnvsnap_intercept.so` to a shared volume
2. Source pod sets `LD_PRELOAD` to load the library
3. On checkpoint: CRIU dumps the process, io_uring is drained
4. On restore: Library detects restore marker and reinits libuv loops

## Files

```
lib/nvsnap_intercept/
├── include/
│   └── nvsnap_intercept.h    # Public API
├── src/
│   ├── init.c               # Initialization, logging
│   ├── quiesce.c            # io_uring + libuv tracking
│   ├── io_uring_intercept.c # io_uring syscall hooks
│   └── libuv_intercept.c    # libuv function hooks
├── tests/
│   ├── test_uring_simple.py # io_uring test
│   ├── test_uvloop_simple.py# uvloop test
│   └── ...
├── Makefile
└── README.md
```
