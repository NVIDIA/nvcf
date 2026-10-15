# uvloop 0.21.0 CRIU Restore Patch Specification

## Version & Location
- **uvloop**: 0.21.0
- **Python**: 3.12  
- **Container**: vllm/vllm-openai
- **Path**: `/usr/local/lib/python3.12/dist-packages/uvloop/`

## Source Files Extracted
All source files are in: `docs/uvloop-analysis/`
- `process.pyx` - UVProcess class (crash location)
- `handle.pyx` - UVHandle base class

---

## Class Hierarchy

```
UVHandle (handle.pyx)
    └── UVProcess (process.pyx)
            └── UVProcessTransport (process.pyx)
```

---

## Key Fields in UVHandle (handle.pyx lines 14-20)

```cython
def __cinit__(self):
    self._closed = 0        # int
    self._inited = 0        # int  
    self._has_handle = 1    # int
    self._handle = NULL     # uv.uv_handle_t*  <-- THIS IS THE STALE POINTER
    self._loop = None       # Loop
    self._source_traceback = None
```

---

## Where _handle is First Dereferenced

### 1. `_ensure_alive()` (handle.pyx:157-161)
Called before any libuv operation. Checks `_closed` and `_inited` but doesn't validate `_handle` pointer.

```cython
cdef inline _ensure_alive(self):
    if not self._is_alive():
        raise RuntimeError(...)
```

### 2. `_is_alive()` (handle.pyx:135-155)  
This is where UVLOOP_DEBUG validates `_handle.loop` and `_handle.data`:

```cython
cdef inline bint _is_alive(self):
    res = self._closed != 1 and self._inited == 1
    if UVLOOP_DEBUG:
        if res and self._has_handle == 1:
            if self._handle is NULL:         # <-- Only checked in DEBUG
                raise RuntimeError(...)
            if self._handle.loop is not ...: # <-- Dereference here
                raise RuntimeError(...)
            if self._handle.data is not ...: # <-- Dereference here
                raise RuntimeError(...)
    return res
```

### 3. `_close()` (handle.pyx:184-212)
Dereferences `_handle` to call `uv_close()`:

```cython
cdef _close(self):
    if self._closed == 1:
        return
    self._closed = 1
    if self._handle is NULL:
        return
    # ... UVLOOP_DEBUG checks ...
    Py_INCREF(self)
    uv.uv_close(self._handle, __uv_close_handle_cb)  # <-- Dereference
```

### 4. UVProcess._kill() (process.pyx:334-339)
Calls into libuv with stale handle:

```cython
cdef _kill(self, int signum):
    self._ensure_alive()
    err = uv.uv_process_kill(<uv.uv_process_t*>self._handle, signum)  # <-- Crash
```

### 5. UVProcess._close_process_handle() (process.pyx:15-23)
```cython
cdef _close_process_handle(self):
    if self._handle is NULL:
        return
    self._handle.data = NULL                                   # <-- Dereference
    uv.uv_close(self._handle, __uv_close_process_handle_cb)    # <-- Crash
```

---

## Exact Patch Locations

### File 1: `handle.pyx`

**Add generation tracking (after line 13 imports, or at module level):**

```cython
from libc.stdlib cimport getenv

# Module-level restore generation flag
cdef int _RESTORE_GEN = 0
if getenv("NVSNAP_RESTORED") != NULL:
    _RESTORE_GEN = 1
```

**Add `_gen` field to UVHandle.__cinit__ (line 14-20):**

```cython
def __cinit__(self):
    self._closed = 0
    self._inited = 0
    self._has_handle = 1
    self._handle = NULL
    self._loop = None
    self._source_traceback = None
    self._gen = _RESTORE_GEN  # NEW: track generation
```

**Modify `_is_alive()` to detect stale handles (line 135):**

```cython
cdef inline bint _is_alive(self):
    cdef bint res
    
    # NEW: Check for stale (pre-restore) handle
    if self._gen != _RESTORE_GEN:
        self._invalidate_after_restore()
        return False  # Force caller to recreate
    
    res = self._closed != 1 and self._inited == 1
    # ... rest unchanged ...
```

**Add invalidation method:**

```cython
cdef void _invalidate_after_restore(self):
    """Called when handle was created before CRIU restore.
    
    Sets _handle to NULL without dereferencing it (it's stale memory).
    Sets _closed = 1 to prevent future use.
    Updates _gen so this is only called once.
    """
    self._handle = NULL   # Don't dereference, just clear
    self._closed = 1      # Mark as closed
    self._inited = 0      # Mark as not initialized
    self._gen = _RESTORE_GEN
```

---

### File 2: `process.pyx`

**No changes required** - the fix in UVHandle base class will propagate.

The key insight: `_is_alive()` is called via `_ensure_alive()` before any operation that would dereference `_handle`. By intercepting there, we prevent the stale pointer from ever being used.

---

## Expected Behavior After Patch

1. **Source container (no NVSNAP_RESTORED):**
   - `_RESTORE_GEN = 0`
   - All handles created with `_gen = 0`
   - No overhead, normal operation

2. **Restored container (NVSNAP_RESTORED=1):**
   - `_RESTORE_GEN = 1`
   - Pre-existing handles have `_gen = 0`
   - On first access via `_is_alive()`:
     - Detects `_gen != _RESTORE_GEN`
     - Calls `_invalidate_after_restore()`
     - Sets `_handle = NULL`, `_closed = 1`
     - Returns `False`
   - Caller receives "handler is closed" error
   - Application-level retry creates new handle with `_gen = 1`
   - Normal operation continues

---

## What This Does NOT Do

- ❌ Does not allocate libuv structs
- ❌ Does not call libuv init functions
- ❌ Does not guess struct offsets
- ❌ Does not touch Loop internals
- ❌ Does not modify signal handlers

---

## Testing Plan

1. Build patched uvloop wheel
2. Inject into vLLM container (via ConfigMap/init container or custom image)
3. Checkpoint with `NVSNAP_RESTORED` not set (normal source)
4. Restore with `NVSNAP_RESTORED=1`
5. Make inference request that triggers subprocess (if any) or signal handling
6. Verify no segfault

---

## Alternative: Python-Level Shim (No Cython Rebuild)

If rebuilding uvloop is too complex, we can use a Python shim that replaces uvloop entirely after restore:

```python
# sitecustomize.py
import os
if os.environ.get('NVSNAP_RESTORED') == '1':
    import sys
    import asyncio
    
    # Prevent uvloop from being used
    class _NoUvloop:
        def install(self): pass
        def new_event_loop(self): return asyncio.new_event_loop()
        Loop = asyncio.SelectorEventLoop
    
    sys.modules['uvloop'] = _NoUvloop()
    asyncio.set_event_loop_policy(asyncio.DefaultEventLoopPolicy())
```

This is less performant (loses uvloop's speed) but avoids patching Cython code.
