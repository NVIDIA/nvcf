# uvloop 0.21.0 Post-Restore Analysis

## Version Info
- **uvloop**: 0.21.0
- **Python**: 3.12
- **Location**: `/usr/local/lib/python3.12/dist-packages/uvloop/`

## Key Files
- `handles/process.pyx` - UVProcess class (crash location)
- `handles/handle.pyx` - UVHandle base class
- `loop.pyx` - Main event loop

## The Crash Site

The crash is in `UVProcess.__init` (or more precisely, in the `_init` method). 

### Key Code Path

```cython
# From process.pyx
@cython.no_gc_clear
cdef class UVProcess(UVHandle):
    def __cinit__(self):
        self.uv_opt_env = NULL
        self.uv_opt_args = NULL
        self._returncode = None
        self._pid = None
        ...

    cdef _init(self, Loop loop, list args, dict env, ...):
        self._start_init(loop)
        
        # HERE: allocates a fresh uv_process_t
        self._handle = <uv.uv_handle_t*>PyMem_RawMalloc(sizeof(uv.uv_process_t))
        ...
        
        # HERE: spawns process via libuv
        err = uv.uv_spawn(loop.uvloop, <uv.uv_process_t*>self._handle, &self.options)
```

### The Stale Pointer Problem

After CRIU restore, `self._handle` may point to:
1. An old `uv_process_t` that was freed/invalid
2. Memory that was reallocated for something else
3. A valid-looking but semantically stale handle

The offset `0x18` from the disassembly is likely a field within `UVProcess` or `UVHandle`:

```cython
# UVHandle base class fields (approximate layout):
cdef class UVHandle:
    # offset 0x00: PyObject header
    # offset 0x10: _closed (int)
    # offset 0x14: _inited (int)
    # offset 0x18: _has_handle (int) or _handle (pointer)
    cdef int _closed
    cdef int _inited
    cdef int _has_handle
    cdef uv.uv_handle_t* _handle   # <-- likely 0x18-0x20
    cdef Loop _loop
```

## Safe Fix Pattern (Per Boss's Guidance)

### Pattern A: Generation Counter + Lazy Rebuild

```cython
# Add to uvloop/__init__.py or a new _restore.py
import os
_restore_gen = 1 if os.environ.get('NVSNAP_RESTORED') == '1' else 0

# In UVHandle base class
cdef class UVHandle:
    cdef int _gen  # generation when this handle was created
    
    cdef _ensure_valid(self):
        global _restore_gen
        if _restore_gen != self._gen:
            self._invalidate_and_rebuild()
            self._gen = _restore_gen
```

### Pattern B: Disable Subprocess Integration Only

Since crash is in UVProcess, and vLLM likely doesn't spawn subprocesses on hot path:

```python
# In sitecustomize.py
import os
if os.environ.get('NVSNAP_RESTORED') == '1':
    # Force asyncio's default subprocess handling instead of uvloop's
    import asyncio
    import uvloop
    
    # Patch uvloop to use stdlib subprocess
    original_loop_class = uvloop.Loop
    
    class PatchedLoop(original_loop_class):
        async def subprocess_exec(self, *args, **kwargs):
            # Delegate to stdlib asyncio subprocess
            loop = asyncio.new_event_loop()
            try:
                return await loop.subprocess_exec(*args, **kwargs)
            finally:
                loop.close()
    
    uvloop.Loop = PatchedLoop
```

## What NOT To Do

1. ❌ Don't manually `PyMem_RawMalloc` a `uv_process_t` - libuv manages this via `uv_spawn`
2. ❌ Don't guess struct offsets from disassembly - use source
3. ❌ Don't try to rebuild all handles at once - too many invariants

## Minimal Patch Target

The safest minimal patch targets `_ensure_alive()` in `handle.pyx`:

```cython
cdef inline _ensure_alive(self):
    global _restore_gen
    if _restore_gen != 0 and self._gen != _restore_gen:
        # Handle was created before restore - it's stale
        # Close it gracefully and let caller recreate
        self._close()
        raise RuntimeError('handle invalidated by process restore')
```

This will cause graceful failures instead of segfaults, allowing the application to retry with fresh handles.

## Files for Reference

- `process.pyx` - Full UVProcess implementation
- `handle.pyx` - UVHandle base class
- `loop-header.pyx` - Loop class definition (first 200 lines)
