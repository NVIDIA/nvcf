# NVSNAP Python Compatibility Guide

This document catalogs Python libraries that can cause issues with CRIU checkpoint/restore and provides mitigation strategies.

## Root Cause

CRIU restores process memory at the same virtual addresses, but **C extensions holding pointers to kernel-backed resources** (epoll fds, sockets, io_uring rings, GPU contexts) will have stale state after restore. The kernel resources are recreated by CRIU, but the C-level bookkeeping in libraries like uvloop/libuv is not updated.

## Quick Reference: Risk Levels

| Risk | Libraries | Action Required |
|------|-----------|-----------------|
| 🔴 HIGH | uvloop, grpcio, gevent, eventlet, pyzmq, torch, ray, multiprocessing | Must configure/disable |
| 🟠 MEDIUM | asyncpg, psycopg2, redis, pymongo, kafka, sqlalchemy, httpx | May need reconnect logic |
| 🟡 LOW | cryptography, h5py, numpy, pillow, lxml | Usually OK |

---

## 🔴 HIGH RISK - Definite Issues

### uvloop

**Problem**: uvloop statically links libuv, which holds C pointers to epoll/kqueue file descriptors and internal process/signal handlers. After CRIU restore, these pointers reference stale or invalid memory.

**Symptoms**:
- Segfault in `loop.cpython-*.so`
- Crash on first async operation after restore
- `UVProcess.__init__` segfault when spawning subprocesses

**Mitigation**:
```python
import asyncio
asyncio.set_event_loop_policy(asyncio.DefaultEventLoopPolicy())
```

**Environment Variable**:
```bash
UVICORN_LOOP=asyncio  # For uvicorn
```

**Why LD_PRELOAD doesn't work**: uvloop vendors libuv statically - there's no dynamic `libuv.so` to intercept.

---

### grpcio

**Problem**: gRPC C core maintains connection state, completion queues, and thread pools that don't survive restore.

**Symptoms**:
- `grpc._channel._InactiveRpcError`
- Connection timeouts after restore
- Hangs on first RPC call

**Mitigation**:
```python
os.environ['GRPC_ENABLE_FORK_SUPPORT'] = '0'
os.environ['GRPC_POLL_STRATEGY'] = 'epoll1'
os.environ['GRPC_DNS_RESOLVER'] = 'native'
```

**Best Practice**: Close channels before checkpoint, recreate after restore.

---

### gevent

**Problem**: gevent uses greenlets (C extension) + libev for I/O. Both hold kernel-dependent state.

**Symptoms**:
- Greenlet crashes
- Event loop hangs
- Socket operations fail

**Mitigation**:
```python
os.environ['GEVENT_NO_MONKEY'] = '1'  # Disable monkey-patching
```

**Alternative**: Don't use gevent in checkpointed processes.

---

### eventlet

**Problem**: Similar to gevent - greenlets + epoll state.

**Symptoms**:
- Greenlet crashes
- I/O hangs

**Mitigation**:
```python
os.environ['EVENTLET_NO_GREENDNS'] = '1'
```

**Alternative**: Use threading or asyncio instead.

---

### pyzmq (ZeroMQ)

**Problem**: ZeroMQ contexts hold socket state, I/O threads, and connection pools.

**Symptoms**:
- `zmq.error.ZMQError: Context was terminated`
- Socket operations fail

**Mitigation**:
```python
os.environ['ZMQ_IO_THREADS'] = '1'
```

**Best Practice**: Close sockets and terminate context before checkpoint.

---

### torch / CUDA

**Problem**: CUDA contexts, GPU memory allocations, NCCL communicators, and cuBLAS handles are GPU kernel state.

**Symptoms**:
- `CUDA error: invalid device ordinal`
- GPU memory access violations
- NCCL timeout/hang

**Mitigation**: Handled by `cuda-checkpoint` tool which:
1. Quiesces CUDA operations
2. Saves GPU memory to host
3. Restores CUDA context on resume

**Note**: This is the primary reason NVSNAP exists!

---

### ray

**Problem**: Ray has multiple subsystems with state:
- Plasma object store (shared memory)
- GCS (gRPC connections)
- Temp directories for worker communication
- raylet connections

**Symptoms**:
- `RaySystemError: Plasma store not available`
- Worker communication failures
- Missing `/tmp/ray/session_*` directories

**Mitigation**:
```python
os.environ['RAY_OBJECT_STORE_MEMORY'] = '100000000'  # Reduce plasma usage
os.environ['RAY_ENABLE_RECORD_ACTOR_TASK_LOGGING'] = '0'
```

**NVSNAP Handling**: Capture and restore `/tmp` directories from checkpoint.

---

### multiprocessing

**Problem**: Uses shared memory (`/dev/shm`), semaphores, pipes, and Unix sockets for IPC.

**Symptoms**:
- `FileNotFoundError: /dev/shm/...`
- `BrokenPipeError` to workers
- Semaphore errors

**Mitigation**:
```python
import multiprocessing
multiprocessing.set_start_method('spawn', force=True)
```

**NVSNAP Handling**: 
- Relink ghost shm files on restore
- Preserve pipe IDs with `inherit-fd`
- Restore `/dev/shm` entries

---

## 🟠 MEDIUM RISK - Connection/FD State

### Database Drivers

| Library | Problem | Mitigation |
|---------|---------|------------|
| psycopg2/psycopg3 | libpq connections stale | Reconnect on `OperationalError` |
| asyncpg | Connection pool dead | Uses asyncio (OK if uvloop blocked) |
| pymysql | Socket connections | Auto-reconnect usually works |
| aiomysql | Async connections | Uses asyncio |
| pymongo | Topology monitoring thread | Auto-reconnect |
| motor | Async MongoDB | Uses asyncio |

**General Strategy**: Most drivers have auto-reconnect. Test after restore.

---

### Redis

**Problem**: Connection pool, pub/sub subscriptions, Lua script cache.

**Symptoms**:
- `ConnectionError: Connection closed`
- Pub/sub stops receiving

**Mitigation**: redis-py has auto-reconnect. For pub/sub, resubscribe after restore.

---

### Message Queues

| Library | Problem | Mitigation |
|---------|---------|------------|
| kafka-python | Broker connections, consumer group | Rejoin group, may lose position |
| pika (RabbitMQ) | Channel state | Reconnect, reopen channels |
| aio-pika | Async RabbitMQ | Same as pika |
| celery | Broker + result backend | Reconnect both |

---

### HTTP Clients

| Library | Problem | Mitigation |
|---------|---------|------------|
| requests | urllib3 connection pool | Usually auto-reconnects |
| httpx | Connection pool | Close client before checkpoint |
| aiohttp | Connector pool | Close session before checkpoint |

---

## 🟡 LOWER RISK - Usually OK

| Library | Notes |
|---------|-------|
| numpy | BLAS thread pool may need reinit, usually OK |
| pandas | Uses numpy, usually OK |
| scikit-learn | Uses numpy/scipy, usually OK |
| cryptography | OpenSSL RNG reseeded by CRIU |
| pillow | Stateless per-image |
| lxml | Stateless per-parse |
| h5py | Close files before checkpoint |
| zarr | Close stores before checkpoint |

---

## 🔵 Async/Event Loop Libraries

### asyncio (stdlib)

**Status**: ✅ Generally OK with `DefaultEventLoopPolicy`

**Notes**: 
- epoll fd is recreated by CRIU
- Signal handlers may need re-registration
- Running tasks resume normally

---

### trio

**Status**: ⚠️ Needs testing

**Mitigation**: Force specific I/O backend if issues arise.

---

### twisted

**Status**: ⚠️ Complex reactor state

**Mitigation**: Stop reactor before checkpoint, restart after.

---

### tornado

**Status**: ✅ Usually OK in asyncio mode

**Mitigation**: Use asyncio IOLoop.

---

## 🟣 Web Frameworks

### FastAPI + Uvicorn

**Problem**: Uvicorn defaults to uvloop + httptools (C extension).

**Mitigation**:
```bash
UVICORN_LOOP=asyncio
UVICORN_HTTP=h11
```

Or in code:
```python
uvicorn.run(app, loop="asyncio", http="h11")
```

---

### Starlette

**Problem**: Uses uvloop if available via uvicorn.

**Mitigation**: Same as FastAPI.

---

### Sanic

**Problem**: Deeply integrated with uvloop.

**Mitigation**: May need fork or significant configuration. Consider alternative.

---

### Quart

**Problem**: Uses uvloop via Hypercorn.

**Mitigation**:
```bash
HYPERCORN_WORKER_CLASS=asyncio
```

---

### Django

**Problem**: Database connections, cache backends.

**Mitigation**: 
- Close DB connections: `django.db.connections.close_all()`
- Reconnect cache: Usually automatic

---

### Flask

**Status**: ✅ Generally safer (less async)

**Watch**: Connection pools in extensions.

---

## 🤖 ML/AI Inference

### vLLM

**Problems**:
1. uvloop (via FastAPI/uvicorn)
2. CUDA contexts
3. Ray workers
4. Temp directories
5. NCCL communicators

**NVSNAP Handling**: Full stack - this is the primary target.

---

### Text Generation Inference (TGI)

**Problem**: Rust async runtime (tokio), CUDA.

**Status**: Needs investigation - Rust async is different from Python.

---

### Triton Inference Server

**Problem**: C++ server, CUDA, gRPC.

**Status**: Complex - may need container-level approach.

---

### transformers / sentence-transformers

**Status**: ✅ Usually OK - just model weights in memory.

**Watch**: CUDA handling.

---

## 📊 Observability Libraries

### prometheus_client

**Problem**: HTTP server thread.

**Mitigation**: Stop/restart metrics server.

---

### opentelemetry

**Problem**: Batch span processors, exporters.

**Mitigation**: Flush before checkpoint:
```python
from opentelemetry.sdk.trace import TracerProvider
provider.force_flush()
```

---

### sentry-sdk

**Problem**: Background worker, transport.

**Mitigation**:
```python
import sentry_sdk
sentry_sdk.flush()
```

---

## ⏰ Scheduling/Background Tasks

### APScheduler

**Problem**: Timer threads, job stores.

**Mitigation**: Pause scheduler before checkpoint.

---

### Celery

**Problem**: Broker connection, worker state.

**Mitigation**: Reconnect on startup. Beat needs restart.

---

## Universal sitecustomize.py

Deploy this to handle most common cases:

```python
#!/usr/bin/env python3
"""
NVSNAP Python Compatibility Layer

Deploy to a directory in PYTHONPATH before app code.
Enabled via NVSNAP_ENABLED=1 environment variable.
"""

import os
import sys

if os.environ.get('NVSNAP_ENABLED'):
    
    # ==================== EVENT LOOPS ====================
    # Force asyncio's default event loop (not uvloop)
    import asyncio
    asyncio.set_event_loop_policy(asyncio.DefaultEventLoopPolicy())
    
    # Block uvloop import - replace with shim
    class _UvloopShim:
        """Shim that redirects uvloop calls to asyncio."""
        def install(self): 
            pass  # No-op
        def run(self, coro, **kw): 
            return asyncio.run(coro, **kw)
        def new_event_loop(self): 
            return asyncio.new_event_loop()
        def __getattr__(self, name): 
            return getattr(asyncio, name, None)
    
    sys.modules['uvloop'] = _UvloopShim()
    
    # ==================== WEB SERVERS ====================
    os.environ.setdefault('UVICORN_LOOP', 'asyncio')
    os.environ.setdefault('UVICORN_HTTP', 'h11')
    os.environ.setdefault('HYPERCORN_WORKER_CLASS', 'asyncio')
    
    # ==================== GRPC ====================
    os.environ.setdefault('GRPC_ENABLE_FORK_SUPPORT', '0')
    os.environ.setdefault('GRPC_POLL_STRATEGY', 'epoll1')
    os.environ.setdefault('GRPC_DNS_RESOLVER', 'native')
    
    # ==================== MULTIPROCESSING ====================
    try:
        import multiprocessing
        if hasattr(multiprocessing, 'set_start_method'):
            try:
                multiprocessing.set_start_method('spawn', force=True)
            except RuntimeError:
                pass  # Already set
    except ImportError:
        pass
    
    # ==================== RAY ====================
    os.environ.setdefault('RAY_OBJECT_STORE_MEMORY', '100000000')
    os.environ.setdefault('RAY_ENABLE_RECORD_ACTOR_TASK_LOGGING', '0')
    
    # ==================== PYZMQ ====================
    os.environ.setdefault('ZMQ_IO_THREADS', '1')
    
    # ==================== GEVENT/EVENTLET ====================
    os.environ.setdefault('GEVENT_NO_MONKEY', '1')
    os.environ.setdefault('EVENTLET_NO_GREENDNS', '1')
    
    # ==================== DEBUG ====================
    os.environ.setdefault('PYTHONFAULTHANDLER', '1')
    
    if os.environ.get('NVSNAP_DEBUG'):
        print('[NVSNAP] Python compatibility layer loaded', file=sys.stderr)
        print(f'[NVSNAP] Event loop policy: {asyncio.get_event_loop_policy()}', file=sys.stderr)
```

---

## Deployment

### Option 1: Volume Mount (Kubernetes)

```yaml
initContainers:
- name: setup-nvsnap-python
  image: busybox
  command: ["sh", "-c", "mkdir -p /nvsnap-python && cat > /nvsnap-python/sitecustomize.py << 'EOF'
import os, sys, asyncio
if os.environ.get('NVSNAP_ENABLED'):
    asyncio.set_event_loop_policy(asyncio.DefaultEventLoopPolicy())
    class _S:
        def install(self): pass
        def run(self, c, **k): return asyncio.run(c, **k)
        def new_event_loop(self): return asyncio.new_event_loop()
        def __getattr__(self, n): return getattr(asyncio, n, None)
    sys.modules['uvloop'] = _S()
    os.environ.setdefault('UVICORN_LOOP', 'asyncio')
EOF"]
  volumeMounts:
  - name: nvsnap-python
    mountPath: /nvsnap-python

containers:
- name: app
  env:
  - name: PYTHONPATH
    value: "/nvsnap-python"
  - name: NVSNAP_ENABLED
    value: "1"
  volumeMounts:
  - name: nvsnap-python
    mountPath: /nvsnap-python

volumes:
- name: nvsnap-python
  emptyDir: {}
```

### Option 2: Mutating Webhook

Automatically inject into all pods in namespace:

1. Webhook intercepts pod creation
2. Adds initContainer + volume mount
3. Sets PYTHONPATH + NVSNAP_ENABLED

### Option 3: Base Image

Include in customer's base image:
```dockerfile
COPY sitecustomize.py /opt/nvsnap/python/
ENV PYTHONPATH=/opt/nvsnap/python
ENV NVSNAP_ENABLED=1
```

---

## Testing Checklist

- [ ] uvloop blocked, asyncio used
- [ ] gRPC connections work after restore
- [ ] Database connections auto-reconnect
- [ ] Redis operations succeed
- [ ] Web server responds to requests
- [ ] Background tasks resume
- [ ] No segfaults on inference
- [ ] Worker processes communicate

---

## Known Limitations

1. **Performance**: asyncio is slower than uvloop (~15-30% for I/O-heavy workloads)
2. **Sanic**: Deeply uvloop-integrated, may need alternative framework
3. **Rust async**: TGI and other Rust services need separate investigation
4. **C++ services**: Triton, custom servers need container-level approach

---

## Future Work

1. **uvloop patch**: Upstream generation-counter pattern for restore compatibility
2. **LD_PRELOAD for grpcio**: Intercept gRPC connection creation
3. **Automatic reconnect hooks**: Register callbacks for post-restore reconnection
4. **Quiescence API**: Standard way for apps to prepare for checkpoint

---

## References

- [CRIU Python Issues](https://criu.org/Python)
- [uvloop source](https://github.com/MagicStack/uvloop)
- [libuv fork handling](http://docs.libuv.org/en/v1.x/guide/processes.html#spawning-child-processes)
- [gRPC fork support](https://grpc.github.io/grpc/core/md_doc_fork_support.html)
