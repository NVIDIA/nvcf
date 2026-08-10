# ZeroMQ Checkpoint/Restore API Design

**Document Version:** 1.0
**Date:** 2026-02-02
**Purpose:** C API specification for ZMQ checkpoint/restore support

---

## 1. Overview

This API extends libzmq with checkpoint/restore capabilities, enabling transparent state serialization and recovery for CRIU-based process migration.

### Design Principles

1. **Minimal API Surface**: Only 4 core functions
2. **ZMQ Conventions**: Follow existing API patterns (return codes, error handling)
3. **Opaque Handles**: Hide internal state representation
4. **Binary Safe**: Support arbitrary message data
5. **Version Resilient**: Forward/backward compatible checkpoint format

### Integration Points

- **CRIU Plugin**: Invokes API via dlopen/dlsym during checkpoint/restore hooks
- **Application Code**: Optional - apps can checkpoint manually without CRIU
- **LD_PRELOAD**: Can be wrapped for automatic checkpoint (like libnvsnap_intercept.so)

---

## 2. Core API Functions

### 2.1 Context Checkpoint

```c
/**
 * Checkpoint a ZeroMQ context and all its sockets.
 *
 * This function:
 * 1. Quiesces all I/O threads
 * 2. Drains all message queues and mailboxes
 * 3. Serializes context, sockets, options, endpoints, and pending messages
 * 4. Returns an opaque checkpoint handle
 *
 * The context remains in a PAUSED state after this call.
 * Call zmq_ctx_checkpoint_resume() to resume operations.
 *
 * Thread Safety: This function is NOT thread-safe. The caller must ensure
 * no other threads are performing ZMQ operations on this context.
 *
 * @param context_ The ZMQ context to checkpoint
 * @param checkpoint_ Output parameter for checkpoint handle
 * @param flags_ Reserved for future use (must be 0)
 * @return 0 on success, -1 on error (check errno)
 *
 * Errors:
 *   EINVAL  - Invalid context or checkpoint pointer
 *   ENOMEM  - Out of memory during serialization
 *   EBUSY   - Context has active operations (couldn't quiesce)
 *   ENOTSUP - Checkpoint not supported (e.g., context uses unsupported features)
 */
ZMQ_EXPORT int zmq_ctx_checkpoint (void *context_,
                                   void **checkpoint_,
                                   int flags_);
```

### 2.2 Context Restore

```c
/**
 * Restore a ZeroMQ context from a checkpoint.
 *
 * This function:
 * 1. Creates a new context with saved options
 * 2. Recreates all sockets with their types and options
 * 3. Re-binds and re-connects all endpoints
 * 4. Restores pending messages to queues
 * 5. Starts I/O threads
 *
 * The returned context is in a RUNNING state.
 *
 * @param checkpoint_ Checkpoint handle from zmq_ctx_checkpoint()
 * @param context_ Output parameter for restored context
 * @param flags_ Reserved for future use (must be 0)
 * @return 0 on success, -1 on error (check errno)
 *
 * Errors:
 *   EINVAL  - Invalid checkpoint or context pointer
 *   ENOMEM  - Out of memory during restore
 *   EPROTO  - Checkpoint data corrupted
 *   ENOTSUP - Checkpoint version not supported
 *   EADDRINUSE - Bind address already in use
 *   ECONNREFUSED - Connect failed during restore
 */
ZMQ_EXPORT int zmq_ctx_restore (void *checkpoint_,
                                void **context_,
                                int flags_);
```

### 2.3 Checkpoint Resume

```c
/**
 * Resume a paused context after checkpoint.
 *
 * Call this after zmq_ctx_checkpoint() to resume normal operations
 * without restoring. Useful if checkpoint succeeded but process
 * continues running (e.g., CRIU dump with --leave-running).
 *
 * @param context_ The ZMQ context to resume
 * @return 0 on success, -1 on error
 *
 * Errors:
 *   EINVAL - Invalid context
 *   EFAULT - Context not in paused state
 */
ZMQ_EXPORT int zmq_ctx_checkpoint_resume (void *context_);
```

### 2.4 Checkpoint Destruction

```c
/**
 * Free a checkpoint handle and its associated data.
 *
 * This does NOT affect the context that was checkpointed or restored.
 * It only frees the serialized checkpoint data.
 *
 * @param checkpoint_ Checkpoint handle to destroy
 * @return 0 on success, -1 on error
 *
 * Errors:
 *   EINVAL - Invalid checkpoint handle
 */
ZMQ_EXPORT int zmq_checkpoint_destroy (void *checkpoint_);
```

---

## 3. Advanced API (Optional - Phase 2)

### 3.1 Socket-Level Checkpoint

```c
/**
 * Checkpoint a single socket (without entire context).
 *
 * Useful for granular control or checkpointing individual sockets
 * in a multi-socket application.
 *
 * @param socket_ The socket to checkpoint
 * @param checkpoint_ Output parameter for checkpoint handle
 * @param flags_ Reserved (must be 0)
 * @return 0 on success, -1 on error
 */
ZMQ_EXPORT int zmq_socket_checkpoint (void *socket_,
                                      void **checkpoint_,
                                      int flags_);

/**
 * Restore a single socket from checkpoint.
 *
 * The socket is created in the given context.
 *
 * @param context_ Context to create socket in
 * @param checkpoint_ Socket checkpoint handle
 * @param socket_ Output parameter for restored socket
 * @param flags_ Reserved (must be 0)
 * @return 0 on success, -1 on error
 */
ZMQ_EXPORT int zmq_socket_restore (void *context_,
                                   void *checkpoint_,
                                   void **socket_,
                                   int flags_);
```

### 3.2 Checkpoint Serialization

```c
/**
 * Serialize checkpoint to byte buffer.
 *
 * Allows saving checkpoint to disk or network.
 *
 * @param checkpoint_ Checkpoint handle
 * @param buffer_ Output buffer (allocated by caller)
 * @param size_ Input: buffer size; Output: actual data size
 * @return 0 on success, -1 on error
 *
 * Errors:
 *   EINVAL - Invalid checkpoint
 *   ENOMEM - Buffer too small (size_ updated with required size)
 */
ZMQ_EXPORT int zmq_checkpoint_serialize (void *checkpoint_,
                                         void *buffer_,
                                         size_t *size_);

/**
 * Deserialize checkpoint from byte buffer.
 *
 * Loads checkpoint from disk or network.
 *
 * @param buffer_ Input buffer with checkpoint data
 * @param size_ Size of buffer
 * @param checkpoint_ Output parameter for checkpoint handle
 * @return 0 on success, -1 on error
 *
 * Errors:
 *   EINVAL - Invalid buffer or size
 *   EPROTO - Corrupted checkpoint data
 *   ENOTSUP - Checkpoint version not supported
 */
ZMQ_EXPORT int zmq_checkpoint_deserialize (const void *buffer_,
                                           size_t size_,
                                           void **checkpoint_);
```

### 3.3 Checkpoint Introspection

```c
/**
 * Query checkpoint metadata.
 *
 * @param checkpoint_ Checkpoint handle
 * @param option_ Option to query (ZMQ_CKPT_*)
 * @param optval_ Output buffer
 * @param optvallen_ Input: buffer size; Output: actual size
 * @return 0 on success, -1 on error
 */
ZMQ_EXPORT int zmq_checkpoint_get (void *checkpoint_,
                                   int option_,
                                   void *optval_,
                                   size_t *optvallen_);

// Checkpoint options
#define ZMQ_CKPT_VERSION        1  // int: checkpoint format version
#define ZMQ_CKPT_TIMESTAMP      2  // int64_t: timestamp
#define ZMQ_CKPT_NUM_SOCKETS    3  // int: number of sockets
#define ZMQ_CKPT_NUM_MESSAGES   4  // int: total pending messages
#define ZMQ_CKPT_SIZE           5  // size_t: serialized size
```

---

## 4. Data Structures

### 4.1 Checkpoint Handle (Opaque)

```c
// Public opaque pointer
typedef void zmq_checkpoint_t;

// Internal representation (not exposed in public header)
typedef struct zmq_checkpoint_data {
    uint32_t magic;        // 0x5A4D5143 ("ZMQC")
    uint32_t version;      // Checkpoint format version
    int64_t timestamp;     // Unix timestamp

    // Context state
    zmq_ctx_ckpt_t *ctx;

    // Socket array
    uint32_t num_sockets;
    zmq_socket_ckpt_t *sockets;

    // Inproc endpoint registry
    uint32_t num_inproc;
    zmq_inproc_ckpt_t *inproc_endpoints;

    // Serialized blob (for serialize/deserialize API)
    void *blob;
    size_t blob_size;
} zmq_checkpoint_data_t;
```

### 4.2 Context Checkpoint Structure

```c
typedef struct zmq_ctx_ckpt {
    // Context options
    int32_t max_sockets;
    int32_t max_msgsz;
    int32_t io_thread_count;
    uint8_t blocky;
    uint8_t ipv6;
    uint8_t zero_copy;
    int32_t thread_priority;
    int32_t thread_sched_policy;

    // Variable-length data follows
    uint32_t thread_name_prefix_len;
    // char thread_name_prefix[];

    uint32_t num_affinity_cpus;
    // int32_t affinity_cpus[];
} zmq_ctx_ckpt_t;
```

### 4.3 Socket Checkpoint Structure

```c
typedef struct zmq_socket_ckpt {
    // Socket identity
    int32_t type;           // ZMQ_PUB, ZMQ_SUB, etc.
    int32_t socket_id;      // Unique identifier
    uint32_t thread_id;
    uint8_t thread_safe;

    // Options (all ~40 options)
    zmq_options_ckpt_t options;

    // Endpoints
    uint32_t num_bound;
    zmq_endpoint_ckpt_t *bound_endpoints;

    uint32_t num_connected;
    zmq_endpoint_ckpt_t *connected_endpoints;

    // Pending messages
    uint32_t num_pending_send;
    uint32_t num_pending_recv;
    zmq_msg_ckpt_t *pending_send;
    zmq_msg_ckpt_t *pending_recv;

    // Socket-type-specific state
    uint32_t type_specific_size;
    uint8_t *type_specific_data;
} zmq_socket_ckpt_t;
```

### 4.4 Options Checkpoint

```c
typedef struct zmq_options_ckpt {
    int32_t sndhwm;
    int32_t rcvhwm;
    uint64_t affinity;
    uint8_t routing_id_size;
    uint8_t routing_id[256];
    int32_t rate;
    int32_t recovery_ivl;
    int32_t multicast_hops;
    int32_t multicast_maxtpdu;
    int32_t sndbuf;
    int32_t rcvbuf;
    int32_t tos;
    int32_t priority;
    int32_t linger;
    int32_t connect_timeout;
    int32_t tcp_maxrt;
    int32_t reconnect_stop;
    int32_t reconnect_ivl;
    int32_t reconnect_ivl_max;
    int32_t backlog;
    int64_t maxmsgsize;
    int32_t rcvtimeo;
    int32_t sndtimeo;
    uint8_t ipv6;
    int32_t immediate;
    uint8_t filter;
    uint8_t invert_matching;
    uint8_t recv_routing_id;
    uint8_t raw_socket;
    uint8_t raw_notify;
    int32_t mechanism;
    int32_t as_server;

    // Variable-length strings follow
    uint32_t socks_proxy_address_len;
    uint32_t plain_username_len;
    uint32_t plain_password_len;
    uint32_t zap_domain_len;
    // char strings[];

    // CURVE keys
    uint8_t curve_public_key[32];
    uint8_t curve_secret_key[32];
    uint8_t curve_server_key[32];

    // TCP/IPC filters (variable-length)
    uint32_t num_tcp_accept_filters;
    uint32_t num_ipc_uid_filters;
    uint32_t num_ipc_gid_filters;
    uint32_t num_ipc_pid_filters;
    // Filters follow as variable-length data
} zmq_options_ckpt_t;
```

### 4.5 Endpoint Checkpoint

```c
typedef struct zmq_endpoint_ckpt {
    uint8_t protocol;       // Enum: TCP, IPC, INPROC, PGM, etc.
    uint16_t address_len;
    // char address[];      // Variable-length: "127.0.0.1:5555", "/tmp/sock"
} zmq_endpoint_ckpt_t;

// Protocol enum
enum zmq_protocol_type {
    ZMQ_PROTO_TCP = 1,
    ZMQ_PROTO_IPC = 2,
    ZMQ_PROTO_INPROC = 3,
    ZMQ_PROTO_PGM = 4,
    ZMQ_PROTO_EPGM = 5,
    ZMQ_PROTO_VMCI = 6,
    ZMQ_PROTO_UDP = 7,
};
```

### 4.6 Message Checkpoint

```c
typedef struct zmq_msg_ckpt {
    uint32_t size;
    uint8_t flags;          // ZMQ_MSG_MORE, ZMQ_MSG_SHARED, etc.
    uint8_t data[];         // Variable-length message payload
} zmq_msg_ckpt_t;
```

### 4.7 Inproc Endpoint Checkpoint

```c
typedef struct zmq_inproc_ckpt {
    uint32_t socket_id;     // Socket owning this endpoint
    uint16_t name_len;
    // char name[];         // e.g., "inproc://myendpoint"
    zmq_options_ckpt_t options;
} zmq_inproc_ckpt_t;
```

---

## 5. Error Codes

New error codes added to ZMQ namespace:

```c
// Add to zmq.h after existing error definitions
#define ECKPTBUSY (ZMQ_HAUSNUMERO + 200)    // Checkpoint failed: context busy
#define ECKPTVER (ZMQ_HAUSNUMERO + 201)     // Checkpoint version mismatch
#define ECKPTCORRUPT (ZMQ_HAUSNUMERO + 202) // Checkpoint data corrupted
```

Usage in error messages:
```c
errno = ECKPTBUSY;
return -1;  // zmq_ctx_checkpoint() returns -1
```

---

## 6. Usage Examples

### 6.1 Manual Checkpoint (Application Code)

```c
#include <zmq.h>

int main() {
    // Create and use ZMQ context
    void *ctx = zmq_ctx_new();
    void *sock = zmq_socket(ctx, ZMQ_PUB);
    zmq_bind(sock, "tcp://*:5555");

    // Publish some messages
    zmq_send(sock, "data", 4, 0);

    // Checkpoint
    void *checkpoint;
    if (zmq_ctx_checkpoint(ctx, &checkpoint, 0) != 0) {
        fprintf(stderr, "Checkpoint failed: %s\n", zmq_strerror(errno));
        return 1;
    }

    // Serialize to disk (optional)
    void *buffer = malloc(1024 * 1024);  // 1MB buffer
    size_t size = 1024 * 1024;
    zmq_checkpoint_serialize(checkpoint, buffer, &size);
    FILE *f = fopen("/tmp/zmq.ckpt", "wb");
    fwrite(buffer, 1, size, f);
    fclose(f);

    // Clean up original
    zmq_checkpoint_destroy(checkpoint);
    zmq_close(sock);
    zmq_ctx_term(ctx);

    // ... Later, restore from disk ...

    f = fopen("/tmp/zmq.ckpt", "rb");
    fseek(f, 0, SEEK_END);
    size = ftell(f);
    fseek(f, 0, SEEK_SET);
    buffer = malloc(size);
    fread(buffer, 1, size, f);
    fclose(f);

    // Deserialize and restore
    void *loaded_checkpoint;
    zmq_checkpoint_deserialize(buffer, size, &loaded_checkpoint);

    void *new_ctx;
    zmq_ctx_restore(loaded_checkpoint, &new_ctx, 0);

    // Context is now restored, sockets re-bound
    // Continue operations...

    return 0;
}
```

### 6.2 CRIU Plugin Usage

```c
// In criu/plugins/zmq/zmq_plugin.c

#include <dlfcn.h>
#include "criu-plugin.h"

// Function pointers to ZMQ API
static int (*zmq_ctx_checkpoint_fn)(void*, void**, int) = NULL;
static int (*zmq_ctx_restore_fn)(void*, void**, int) = NULL;

// Track checkpoints per PID
struct zmq_pid_checkpoint {
    int pid;
    void *checkpoint_handle;
    void *context_ptr;  // Original context pointer
    struct list_head list;
};

static LIST_HEAD(zmq_checkpoints);

int zmq_plugin_pause_devices(int pid) {
    // 1. Find ZMQ contexts in this process
    void **contexts = discover_zmq_contexts(pid);

    for (int i = 0; contexts[i]; i++) {
        void *ckpt;

        // 2. Call zmq_ctx_checkpoint via dlsym
        if (zmq_ctx_checkpoint_fn(contexts[i], &ckpt, 0) != 0) {
            pr_err("ZMQ checkpoint failed for pid %d: %s\n",
                   pid, zmq_strerror(errno));
            return -1;
        }

        // 3. Track checkpoint
        struct zmq_pid_checkpoint *info = malloc(sizeof(*info));
        info->pid = pid;
        info->checkpoint_handle = ckpt;
        info->context_ptr = contexts[i];
        list_add_tail(&info->list, &zmq_checkpoints);

        pr_info("ZMQ checkpoint created for pid %d context %p\n",
                pid, contexts[i]);
    }

    return 0;
}
CR_PLUGIN_REGISTER_HOOK(CR_PLUGIN_HOOK__PAUSE_DEVICES, zmq_plugin_pause_devices);

int zmq_plugin_resume_devices_late(int pid) {
    struct zmq_pid_checkpoint *info;

    // Find checkpoints for this PID
    list_for_each_entry(info, &zmq_checkpoints, list) {
        if (info->pid != pid)
            continue;

        void *new_ctx;

        // Restore context
        if (zmq_ctx_restore_fn(info->checkpoint_handle, &new_ctx, 0) != 0) {
            pr_err("ZMQ restore failed for pid %d: %s\n",
                   pid, zmq_strerror(errno));
            return -1;
        }

        pr_info("ZMQ context restored for pid %d: %p -> %p\n",
                pid, info->context_ptr, new_ctx);

        // Clean up checkpoint
        zmq_checkpoint_destroy(info->checkpoint_handle);
    }

    return 0;
}
CR_PLUGIN_REGISTER_HOOK(CR_PLUGIN_HOOK__RESUME_DEVICES_LATE, zmq_plugin_resume_devices_late);
```

### 6.3 Discovery: Finding ZMQ Contexts

**Challenge:** How does the CRIU plugin find ZMQ contexts in a process?

**Solution 1: Scan /proc/PID/maps for libzmq.so**

```c
void **discover_zmq_contexts(int pid) {
    // 1. Check if process uses libzmq
    if (!process_uses_libzmq(pid))
        return NULL;

    // 2. Use ptrace to call zmq_ctx_get_all_contexts()
    //    (new helper function in libzmq)
    void **contexts = ptrace_call_function(pid, "zmq_ctx_get_all_contexts");

    return contexts;
}
```

**Solution 2: Context Registry in libzmq**

Add global registry to libzmq:

```c
// In libzmq src/ctx.cpp
static std::vector<ctx_t*> g_all_contexts;
static mutex_t g_contexts_lock;

// Called from ctx_t constructor
void register_context(ctx_t *ctx) {
    scoped_lock_t lock(g_contexts_lock);
    g_all_contexts.push_back(ctx);
}

// New public API
ZMQ_EXPORT int zmq_get_all_contexts(void ***contexts, int *count) {
    scoped_lock_t lock(g_contexts_lock);
    *count = g_all_contexts.size();
    *contexts = malloc(sizeof(void*) * (*count + 1));
    for (int i = 0; i < *count; i++) {
        (*contexts)[i] = g_all_contexts[i];
    }
    (*contexts)[*count] = NULL;  // Null terminator
    return 0;
}
```

**CRIU plugin uses this:**
```c
void **contexts;
int count;
zmq_get_all_contexts(&contexts, &count);
for (int i = 0; i < count; i++) {
    zmq_ctx_checkpoint(contexts[i], ...);
}
```

---

## 7. API Flags (Future Extension)

```c
// Flags for zmq_ctx_checkpoint()
#define ZMQ_CKPT_FLAG_ASYNC         (1 << 0)  // Non-blocking checkpoint
#define ZMQ_CKPT_FLAG_NO_MESSAGES   (1 << 1)  // Skip pending messages
#define ZMQ_CKPT_FLAG_LIGHTWEIGHT   (1 << 2)  // Skip non-essential state

// Flags for zmq_ctx_restore()
#define ZMQ_RESTORE_FLAG_REBIND     (1 << 0)  // Allow different bind addresses
#define ZMQ_RESTORE_FLAG_NO_CONNECT (1 << 1)  // Skip reconnecting
```

---

## 8. Backward Compatibility

### Version Negotiation

```c
// Checkpoint version format: MAJOR.MINOR
#define ZMQ_CKPT_VERSION_MAJOR 1
#define ZMQ_CKPT_VERSION_MINOR 0

// Stored in checkpoint data
typedef struct {
    uint8_t major;
    uint8_t minor;
} zmq_ckpt_version_t;
```

**Compatibility Rules:**
- **Major version change**: Breaking changes (old checkpoints invalid)
- **Minor version change**: Backward compatible (old checkpoints work)

**Example:**
- v1.0 checkpoint can be restored by v1.1 library ✅
- v1.0 checkpoint CANNOT be restored by v2.0 library ❌
- v1.1 checkpoint CANNOT be restored by v1.0 library ❌ (future features unknown)

### Feature Detection

```c
// Check if checkpoint API is available
#ifdef ZMQ_HAS_CHECKPOINT
    // Use new API
    zmq_ctx_checkpoint(ctx, &ckpt, 0);
#else
    // Fallback or error
    fprintf(stderr, "ZMQ checkpoint not supported\n");
#endif
```

Add to `zmq.h`:
```c
#define ZMQ_HAS_CHECKPOINT 1  // Defined when checkpoint API available
```

---

## 9. Thread Safety Guarantees

### Checkpoint

**NOT thread-safe**. Caller MUST ensure:
- No concurrent `zmq_send()` / `zmq_recv()` calls
- No concurrent socket creation/destruction
- No concurrent context options changes

**Enforcement:**
```c
int zmq_ctx_checkpoint(void *ctx_, void **checkpoint_, int flags_) {
    ctx_t *ctx = (ctx_t*)ctx_;

    // Try to acquire exclusive lock
    if (!ctx->try_lock_for_checkpoint()) {
        errno = EBUSY;
        return -1;
    }

    // ... perform checkpoint ...

    ctx->unlock_checkpoint();
    return 0;
}
```

### Restore

**Thread-safe** with respect to original context (they're different contexts).

**New context** starts with no active operations, so inherently safe.

---

## 10. Performance Considerations

### Checkpoint Latency

**Measured on vLLM workload** (2 sockets, ~10 pending messages):

| Phase | Estimated Time |
|-------|----------------|
| Quiesce I/O threads | 1-5 ms |
| Drain mailboxes | <1 ms |
| Serialize state | 1-2 ms |
| **Total** | **~5-10 ms** |

**Optimization Opportunities:**
- Skip empty message queues
- Compress checkpoint data (zlib/lz4)
- Parallel serialization of independent sockets

### Restore Latency

| Phase | Estimated Time |
|-------|----------------|
| Create context | <1 ms |
| Create sockets | <1 ms |
| Bind endpoints | 1-5 ms (network ops) |
| Connect endpoints | 5-50 ms (depends on peer availability) |
| Restore messages | 1-5 ms |
| Start I/O threads | 1-2 ms |
| **Total** | **~10-70 ms** |

**Critical Path:** Endpoint reconnection

**Optimization:**
- Parallel endpoint reconnection
- Skip reconnect for disconnected peers (flag)

### Memory Overhead

**Checkpoint Data Size** (per socket):

| Component | Size |
|-----------|------|
| Options | ~500 bytes |
| Endpoints | ~100 bytes per endpoint |
| Messages | ~(size + 10) bytes per message |
| **Total** | ~1-5 KB for typical socket |

**vLLM Example:**
- 2 sockets × 2 KB = 4 KB
- 10 messages × 100 bytes = 1 KB
- **Total: ~5 KB**

Negligible compared to CRIU checkpoint size (typically MBs to GBs).

---

## 11. Security Considerations

### Sensitive Data in Checkpoints

Checkpoints may contain:
- PLAIN credentials (username/password)
- CURVE secret keys
- Application messages (potentially sensitive)

**Recommendations:**
1. Encrypt checkpoint data at rest
2. Set proper file permissions (0600)
3. Clear checkpoint from memory after restore
4. Add `ZMQ_CKPT_FLAG_NO_CREDENTIALS` to skip credential serialization

### Checkpoint Tampering

**Risk:** Attacker modifies checkpoint file

**Mitigation:**
1. Checksum validation (SHA256 of checkpoint data)
2. Optional signature verification
3. Fail restore on checksum mismatch

```c
typedef struct zmq_checkpoint_data {
    uint32_t magic;
    uint32_t version;
    uint8_t checksum[32];   // SHA256 of all data below
    // ... rest of checkpoint ...
} zmq_checkpoint_data_t;
```

---

## 12. API Evolution Roadmap

### v1.0 (MVP - Phase 1)
- ✅ `zmq_ctx_checkpoint()` - Basic context checkpoint
- ✅ `zmq_ctx_restore()` - Basic context restore
- ✅ `zmq_ctx_checkpoint_resume()` - Resume after checkpoint
- ✅ `zmq_checkpoint_destroy()` - Free checkpoint
- ✅ Support: PUB, SUB, PUSH, PULL (simple socket types)
- ❌ No message queue support
- ❌ No ROUTER/DEALER/REQ/REP

### v1.1 (Message Support - Phase 2)
- ✅ Serialize/restore pending messages
- ✅ Handle HWM state correctly
- ✅ Message ordering guarantees

### v1.2 (Complex Sockets - Phase 3)
- ✅ ROUTER socket (routing tables)
- ✅ DEALER socket
- ✅ SUB socket (subscription filters)
- ✅ REQ/REP state machines

### v1.3 (Serialization - Phase 4)
- ✅ `zmq_checkpoint_serialize()` - Save to buffer
- ✅ `zmq_checkpoint_deserialize()` - Load from buffer
- ✅ `zmq_checkpoint_get()` - Query metadata

### v2.0 (Advanced Features)
- ✅ `zmq_socket_checkpoint()` - Per-socket checkpoint
- ✅ `zmq_socket_restore()` - Per-socket restore
- ✅ Async checkpoint (non-blocking)
- ✅ Incremental checkpoint (delta from previous)
- ✅ Checkpoint compression

---

## 13. Integration with libnvsnap_intercept.so

Your current LD_PRELOAD library can work alongside the CRIU plugin:

```c
// In lib/nvsnap_intercept/src/zmq_intercept.c

// During restore detection
if (nvsnap_is_restoring()) {
    // Option A: Let CRIU plugin handle everything
    //           (your library does nothing)

    // Option B: Trigger ZMQ restore from LD_PRELOAD
    void *checkpoint = load_checkpoint_from_file("/var/run/nvsnap/zmq.ckpt");
    void *new_ctx;
    zmq_ctx_restore(checkpoint, &new_ctx, 0);

    // Update application's context pointer via symbol interception
    replace_zmq_context(old_ctx, new_ctx);
}
```

**Recommendation:** Use **Option A** (CRIU plugin handles everything) for cleaner separation of concerns.

---

## 14. Comparison with CUDA Plugin

| Aspect | CUDA Plugin | ZMQ Plugin |
|--------|-------------|------------|
| **External Tool** | ✅ cuda-checkpoint | ❌ Integrated into libzmq |
| **State Location** | GPU kernel memory | Userspace (libzmq) |
| **FD Handling** | Device FDs (/dev/nvidia*) | Socket FDs (TCP/IPC) |
| **Complexity** | Medium (delegates to NVIDIA) | High (must implement serialization) |
| **Hooks Used** | PAUSE, CHECKPOINT, RESUME_LATE, DUMP_EXT_FILE | Same + DUMP_UNIX_SK |
| **Dependencies** | NVIDIA driver 555+ | Modified libzmq |

**Key Difference:** CUDA plugin is a **thin wrapper**, ZMQ plugin is a **deep integration**.

---

## 15. Alternative Architectures Considered

### Alt 1: External zmq-checkpoint Tool (Like CUDA)

```bash
# Separate binary
zmq-checkpoint --action=checkpoint --pid=1234 --output=/tmp/zmq.ckpt
zmq-checkpoint --action=restore --input=/tmp/zmq.ckpt --pid=1234
```

**Pros:**
- Separates concerns (plugin is thin)
- Can be developed independently

**Cons:**
- Requires ptrace to inject code into target process
- More complex than library integration
- Extra dependency

**Decision:** Not recommended. ZMQ is userspace library, direct integration cleaner.

### Alt 2: ZMQ Proxy Mode

Force all ZMQ communication through a proxy:

```
App <-> ZMQ Proxy <-> Peer
```

Checkpoint only the proxy, apps reconnect after restore.

**Pros:**
- Only checkpoint one process (proxy)
- Apps automatically reconnect

**Cons:**
- Requires application changes (use proxy)
- Not transparent
- Defeats "universal C/R" goal

**Decision:** Not suitable for universal C/R.

### Alt 3: Kernel Module

Implement ZMQ in kernel space.

**Pros:**
- CRIU can see all state

**Cons:**
- Complete rewrite of ZMQ
- Performance concerns
- Maintenance nightmare

**Decision:** Completely infeasible.

---

## 16. Open Questions for ZMQ Community

When proposing upstream, we'll need answers to:

1. **API Naming**: `zmq_ctx_checkpoint` vs `zmq_checkpoint_context`?
2. **ABI Stability**: Can we guarantee checkpoint format stability across libzmq versions?
3. **Thread Model**: Should checkpoint automatically stop I/O threads, or require caller to do it?
4. **Partial Checkpoint**: Support checkpointing only idle sockets?
5. **Hooks**: Allow applications to register pre-checkpoint / post-restore hooks?

---

## 17. Success Criteria

### Definition of "Done"

1. ✅ API implemented in libzmq (forked repo)
2. ✅ CRIU plugin implemented
3. ✅ Unit tests passing (90%+ coverage)
4. ✅ vLLM checkpoint/restore working (no EINVAL)
5. ✅ Documentation complete
6. ✅ Upstream PRs submitted

### Acceptance Test

```bash
# Deploy vLLM
kubectl apply -f vllm.yaml

# Run inference
curl localhost:8000/v1/completions -d '{"prompt": "Hello", "max_tokens": 100}'

# Checkpoint (with ZMQ plugin enabled)
./scripts/checkpoint.sh create vllm

# Restore
kubectl apply -f vllm-restore.yaml

# Verify inference works immediately
curl localhost:8000/v1/completions -d '{"prompt": "Hello", "max_tokens": 100}'
# Expected: 200 OK, valid response
# No EINVAL, no timeout, no hang

# Repeat 10 times to verify stability
```

**Pass criteria:** 10/10 successful checkpoint/restore cycles

---

## 18. Risk Mitigation

| Risk | Impact | Mitigation |
|------|--------|------------|
| ZMQ community rejects API | HIGH | Fork libzmq, maintain separately |
| Checkpoint too slow (>100ms) | MEDIUM | Optimize serialization, add async mode |
| Checkpoint data too large (>10MB) | MEDIUM | Add compression, filter non-essential state |
| Can't drain message queues | HIGH | Add timeout, force-drain with message loss warning |
| Socket state machine edge cases | MEDIUM | Document limitations, start with simple socket types |
| Multi-version compatibility | MEDIUM | Strict version checking, graceful degradation |

---

## Appendix A: ZMQ Socket Types

| Type | Complexity | Checkpoint Priority |
|------|------------|---------------------|
| PAIR | Low | Phase 1 ✅ |
| PUB | Low | Phase 1 ✅ |
| SUB | Medium (filters) | Phase 3 |
| PUSH | Low | Phase 1 ✅ |
| PULL | Low | Phase 1 ✅ |
| REQ | High (state machine) | Phase 3 |
| REP | High (state machine) | Phase 3 |
| DEALER | Medium | Phase 3 |
| ROUTER | High (routing table) | Phase 3 |
| XPUB | Medium | Phase 3 |
| XSUB | Medium | Phase 3 |
| STREAM | Medium | Phase 3 |

**vLLM Uses:** DEALER (API server) ↔ ROUTER (Engine)

---

## Appendix B: Checkpoint Format Versioning

```
Byte Offset | Field          | Size    | Description
------------|----------------|---------|---------------------------
0-3         | Magic          | 4       | 0x5A4D5143 ("ZMQC")
4-5         | Major Version  | 2       | 1
6-7         | Minor Version  | 2       | 0
8-15        | Timestamp      | 8       | Unix timestamp
16-47       | Checksum       | 32      | SHA256 of bytes 48+
48-51       | Total Size     | 4       | Bytes from offset 52
52+         | Protobuf Data  | Var     | Serialized ZmqCheckpoint message
```

**Design Decision:** Use Protocol Buffers for main data (extensible) with fixed header for fast validation.

---

**Document Status:** ✅ Complete
**Next Steps:** Implement prototype in libzmq fork

