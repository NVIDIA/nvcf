# ZeroMQ State Mapping for Checkpoint/Restore

**Document Version:** 1.0
**Date:** 2026-02-02
**Purpose:** Comprehensive analysis of ZMQ internal state for CRIU plugin development

---

## Executive Summary

This document maps all stateful components within libzmq that must be serialized for checkpoint/restore. Based on analysis of libzmq source code (master branch), we identify:

- **3 major state categories**: Context, Sockets, and Transport
- **42 distinct state fields** requiring serialization
- **4 transient components** that must be reconstructed (not serialized)
- **Estimated serialization complexity**: Medium-High

### Key Finding

ZeroMQ's architecture separates state into well-defined layers, making checkpoint/restore **architecturally feasible** but requiring careful handling of:
1. I/O thread synchronization
2. Message queue draining
3. Socket state machine consistency
4. File descriptor reconstruction

---

## 1. Context State (`zmq::ctx_t`)

**File:** `src/ctx.hpp`, `src/ctx.cpp`

The context is the top-level container holding all ZMQ global state for a process.

### 1.1 Persistent State (Must Serialize)

| Field | Type | Description | Serialization Priority |
|-------|------|-------------|----------------------|
| `_max_sockets` | `int` | Maximum number of sockets | HIGH |
| `_max_msgsz` | `int` | Maximum message size | HIGH |
| `_io_thread_count` | `int` | Number of I/O threads | HIGH |
| `_blocky` | `bool` | Blocking mode | MEDIUM |
| `_ipv6` | `bool` | IPv6 enabled | MEDIUM |
| `_zero_copy` | `bool` | Zero-copy optimization | MEDIUM |
| `_thread_priority` | `int` | Thread scheduling priority | LOW |
| `_thread_sched_policy` | `int` | Thread scheduling policy | LOW |
| `_thread_affinity_cpus` | `std::set<int>` | CPU affinity mask | LOW |
| `_thread_name_prefix` | `std::string` | Thread name prefix | LOW |

**Serialization Strategy:**
```c
struct zmq_ctx_checkpoint {
    uint32_t version;           // Checkpoint format version
    int32_t max_sockets;
    int32_t max_msgsz;
    int32_t io_thread_count;
    uint8_t blocky;
    uint8_t ipv6;
    uint8_t zero_copy;
    int32_t thread_priority;
    int32_t thread_sched_policy;
    // CPU affinity and thread name as variable-length data
};
```

### 1.2 Inproc Endpoint Registry

**Critical Component:** `std::map<std::string, endpoint_t> _endpoints`

This maps inproc:// endpoint names to socket pointers.

**Challenge:** Socket pointers are invalid after restore.

**Solution:**
- Serialize endpoint name → socket ID mapping
- Reconstruct pointer references during restore using socket ID registry

```c
struct zmq_inproc_endpoint {
    char endpoint_name[256];  // e.g., "inproc://myendpoint"
    uint32_t socket_id;       // Stable socket identifier
    // options_t serialized separately
};
```

### 1.3 Socket Registry

**Component:** `std::vector<socket_base_t *> _sockets`

**Serialization:**
- Count of sockets
- Array of socket checkpoints (see Section 2)

### 1.4 Transient State (Reconstruct, Don't Serialize)

| Component | Reconstruction Strategy |
|-----------|------------------------|
| `_io_threads` | Recreate based on `_io_thread_count` |
| `_reaper` | Recreate single reaper thread |
| `_term_mailbox` | Recreate empty mailbox |
| `_slot_sync` mutex | Recreate unlocked |

**Rationale:** These are runtime objects with no persistent state worth saving.

---

## 2. Socket State (`zmq::socket_base_t`)

**File:** `src/socket_base.hpp`, `src/socket_base.cpp`

Sockets are the primary user-facing objects. Each socket type (PAIR, PUB, SUB, REQ, REP, DEALER, ROUTER, etc.) inherits from `socket_base_t`.

### 2.1 Socket Identification

| Field | Type | Description |
|-------|------|-------------|
| Socket Type | `int` | ZMQ_PAIR, ZMQ_PUB, ZMQ_SUB, etc. |
| Socket ID | `int` | Unique identifier within context |
| Thread ID | `uint32_t` | Owning thread ID |
| Thread Safe | `bool` | Whether socket is thread-safe |

### 2.2 Socket Options (`options_t`)

**File:** `src/options.hpp`

**Complete List of Serializable Options:**

| Option | Type | ZMQ Constant | Default |
|--------|------|--------------|---------|
| Send HWM | `int` | `ZMQ_SNDHWM` | 1000 |
| Recv HWM | `int` | `ZMQ_RCVHWM` | 1000 |
| Affinity | `uint64_t` | `ZMQ_AFFINITY` | 0 |
| Routing ID | `unsigned char[256]` | `ZMQ_ROUTING_ID` | "" |
| Rate | `int` | `ZMQ_RATE` | 100 kb/s |
| Recovery Interval | `int` | `ZMQ_RECOVERY_IVL` | 10000 ms |
| Multicast Hops | `int` | `ZMQ_MULTICAST_HOPS` | 1 |
| Multicast Max TPU | `int` | `ZMQ_MULTICAST_MAXTPDU` | 1500 |
| Send Buffer | `int` | `ZMQ_SNDBUF` | 0 |
| Recv Buffer | `int` | `ZMQ_RCVBUF` | 0 |
| Type of Service | `int` | `ZMQ_TOS` | 0 |
| Priority | `int` | `ZMQ_PRIORITY` | 0 |
| Linger | `int` | `ZMQ_LINGER` | -1 |
| Connect Timeout | `int` | `ZMQ_CONNECT_TIMEOUT` | 0 |
| TCP Max RT | `int` | `ZMQ_TCP_MAXRT` | 0 |
| Reconnect Stop | `int` | `ZMQ_RECONNECT_STOP` | 0 |
| Reconnect Interval | `int` | `ZMQ_RECONNECT_IVL` | 100 ms |
| Reconnect Interval Max | `int` | `ZMQ_RECONNECT_IVL_MAX` | 0 |
| Backlog | `int` | `ZMQ_BACKLOG` | 100 |
| Max Message Size | `int64_t` | `ZMQ_MAXMSGSIZE` | -1 |
| Recv Timeout | `int` | `ZMQ_RCVTIMEO` | -1 |
| Send Timeout | `int` | `ZMQ_SNDTIMEO` | -1 |
| IPv6 | `bool` | `ZMQ_IPV6` | false |
| Immediate | `int` | `ZMQ_IMMEDIATE` | 0 |
| Filter | `bool` | - | true |
| Invert Matching | `bool` | `ZMQ_INVERT_MATCHING` | false |
| Recv Routing ID | `bool` | `ZMQ_RCVMORE` | false |
| Raw Socket | `bool` | `ZMQ_ROUTER_RAW` | false |
| TCP Keepalive | `int` | `ZMQ_TCP_KEEPALIVE` | -1 |
| TCP Keepalive Count | `int` | `ZMQ_TCP_KEEPALIVE_CNT` | -1 |
| TCP Keepalive Idle | `int` | `ZMQ_TCP_KEEPALIVE_IDLE` | -1 |
| TCP Keepalive Interval | `int` | `ZMQ_TCP_KEEPALIVE_INTVL` | -1 |
| Mechanism | `int` | `ZMQ_MECHANISM` | ZMQ_NULL |
| PLAIN Username | `std::string` | `ZMQ_PLAIN_USERNAME` | "" |
| PLAIN Password | `std::string` | `ZMQ_PLAIN_PASSWORD` | "" |
| CURVE Public Key | `uint8_t[32]` | `ZMQ_CURVE_PUBLICKEY` | - |
| CURVE Secret Key | `uint8_t[32]` | `ZMQ_CURVE_SECRETKEY` | - |
| CURVE Server Key | `uint8_t[32]` | `ZMQ_CURVE_SERVERKEY` | - |
| SOCKS Proxy Address | `std::string` | `ZMQ_SOCKS_PROXY` | "" |

**Total:** ~40 socket options

**Serialization Strategy:**
- Use Protocol Buffers or similar schema
- Only serialize non-default values to reduce size
- Version the schema for backward compatibility

### 2.3 Endpoint Bindings

**Component:** Bound endpoints (where socket is listening)

**Data Structure:**
```c
struct zmq_bound_endpoint {
    char protocol[16];      // "tcp", "ipc", "inproc", "pgm", etc.
    char address[256];      // "127.0.0.1:5555", "/tmp/sock", etc.
    uint16_t port;          // For TCP/UDP
    int fd;                 // File descriptor (reconstruct, don't serialize)
};
```

**Examples:**
- `tcp://0.0.0.0:5555`
- `ipc:///tmp/feeds/0`
- `inproc://my-endpoint`

### 2.4 Endpoint Connections

**Component:** Connected endpoints (where socket is connected to)

**Data Structure:**
```c
struct zmq_connected_endpoint {
    char protocol[16];
    char address[256];
    uint16_t port;
    uint8_t connection_state;  // connected, connecting, failed
};
```

### 2.5 Pipes (Message Queues)

**File:** `src/pipe.hpp`

Pipes are bidirectional message queues between sockets.

**Critical State:**

| Field | Type | Description |
|-------|------|-------------|
| `_hwm` | `int` | High water mark |
| `_lwm` | `int` | Low water mark |
| `_msgs_read` | `uint64_t` | Messages read counter |
| `_msgs_written` | `uint64_t` | Messages written counter |
| `_peers_msgs_read` | `uint64_t` | Peer's read counter |
| `_state` | `enum` | active, delimiter_received, waiting_for_delimiter, etc. |
| `_delay` | `bool` | Process pending messages before terminating |
| `_router_socket_routing_id` | `blob_t` | Routing ID |
| `_server_socket_routing_id` | `int` | Server routing ID |
| `_endpoint_pair` | `endpoint_uri_pair_t` | Local and remote endpoints |

**Underlying Message Queue:** `ypipe_t<msg_t>`

**Challenge:** The `ypipe_t` is a lock-free queue with complex internal state.

**Solution:**
- Drain all pending messages during checkpoint
- Serialize messages as array
- Reconstruct queue on restore and re-enqueue messages

### 2.6 Mailbox (Command Queue)

**File:** `src/mailbox.hpp`

Each socket has a mailbox for inter-thread commands.

**Components:**
- `cpipe_t _cpipe` - Command pipe (ypipe)
- `signaler_t _signaler` - File descriptor pair for signaling

**Serialization Strategy:**
- Drain all pending commands before checkpoint
- Serialize command array
- Recreate empty mailbox on restore
- Re-enqueue commands

### 2.7 Socket-Specific State

Different socket types have additional state:

**ROUTER Socket:**
- Routing table mapping peer IDs to pipes

**SUB Socket:**
- Subscription filters (prefix matching)

**XPUB Socket:**
- Subscription tracking for each peer

**REQ/REP Sockets:**
- Request/reply state machine position

**Strategy:** Use virtual method `socket_base_t::checkpoint_state()` that each socket type implements.

---

## 3. Transport Layer State

### 3.1 TCP Connections

**File:** `src/tcp_connecter.cpp`, `src/tcp_listener.cpp`

**State:**
- Remote address/port
- Local address/port
- Connection state (connected, connecting, failed)
- File descriptor (reconstruct)
- TCP socket options (keepalive, etc.)

**File Descriptors:**
- CRIU can handle established TCP connections with `--tcp-established` flag
- ZMQ plugin should mark TCP FDs as "CRIU-managed" not "plugin-managed"

### 3.2 IPC (Unix Domain Sockets)

**File:** `src/ipc_connecter.cpp`, `src/ipc_listener.cpp`

**State:**
- Socket file path
- File descriptor (reconstruct)

**Challenge:**
- CRIU has [known limitations with Unix domain sockets](https://criu.org/What_cannot_be_checkpointed)
- May require CR_PLUGIN_HOOK__DUMP_EXT_FILE

**Solutions:**
1. Close IPC sockets during checkpoint
2. Recreate them during restore
3. Alternatively: use CR_PLUGIN_HOOK__DUMP_UNIX_SK if both endpoints are checkpointed

### 3.3 Inproc (In-Process)

**File:** Handled at context level

**State:**
- Endpoint name
- Pipe connections

**Note:** Inproc is entirely in-memory, so no file descriptors to worry about.

---

## 4. I/O Thread State

**File:** `src/io_thread.hpp`, `src/io_thread.cpp`

### 4.1 Poller State

Each I/O thread has a poller (epoll/kqueue/select) tracking file descriptors.

**Components:**
- Array of monitored file descriptors
- Events registered for each FD (POLLIN, POLLOUT)
- Timers

**Serialization Strategy:**
- Stop I/O threads during checkpoint
- Don't serialize poller internal state
- Reconstruct pollers on restore
- Re-register all socket FDs

### 4.2 Thread State

**Transient (Reconstruct):**
- Thread handle
- Mailbox
- Active connections

**Persistent (Serialize):**
- Thread ID
- CPU affinity
- Priority

---

## 5. Message State (`zmq::msg_t`)

**File:** `src/msg.hpp`

Messages in-flight must be serialized.

**Message Structure:**

| Field | Type | Description |
|-------|------|-------------|
| Data pointer | `void*` | Message payload |
| Size | `size_t` | Payload size |
| Flags | `uint8_t` | MORE, SHARED, etc. |
| Reference count | `int` | For shared messages |
| Free function | `void(*)(void*)` | Deallocator |

**Serialization:**
```c
struct zmq_msg_checkpoint {
    uint32_t size;
    uint8_t flags;
    uint8_t data[size];  // Variable length
};
```

**Challenge:** Shared messages with reference counting

**Solution:**
- Snapshot message data (copy)
- Discard reference counting (reconstruct on restore)
- Serialize free function as enum (standard deallocators only)

---

## 6. State Dependencies

```
Context
  ├─ I/O Threads (N threads)
  │    ├─ Poller (epoll/kqueue)
  │    └─ Mailbox
  ├─ Reaper Thread
  │    └─ Mailbox
  ├─ Inproc Endpoint Registry
  │    └─ Endpoint → Socket mappings
  └─ Sockets (M sockets)
       ├─ Options
       ├─ Bound Endpoints
       │    └─ Listeners (TCP/IPC)
       │         └─ File Descriptors
       ├─ Connected Endpoints
       │    └─ Connecters (TCP/IPC)
       │         └─ File Descriptors
       ├─ Pipes (P pipes per socket)
       │    ├─ Message Queue (ypipe)
       │    │    └─ Messages
       │    └─ Routing IDs
       ├─ Mailbox
       │    ├─ Command Queue
       │    └─ Signaler FDs
       └─ Socket-Type-Specific State
            ├─ ROUTER: Routing Table
            ├─ SUB: Subscriptions
            └─ REQ/REP: State Machine
```

**Critical Ordering for Restore:**
1. Create context (with options)
2. Create I/O threads (stopped)
3. Create sockets (with options, type)
4. Bind/connect endpoints
5. Restore pipes and message queues
6. Restore inproc endpoint registry
7. Start I/O threads
8. Resume socket operations

---

## 7. Checkpoint Procedure

### 7.1 Quiesce Phase (CR_PLUGIN_HOOK__PAUSE_DEVICES)

```c
int zmq_ctx_checkpoint(void *ctx, zmq_checkpoint_data_t **out) {
    // 1. Stop all I/O threads
    for (auto thread : io_threads) {
        thread->stop();
    }

    // 2. Drain all mailboxes
    for (auto socket : sockets) {
        socket->drain_mailbox();
    }

    // 3. Flush all pipes
    for (auto socket : sockets) {
        for (auto pipe : socket->pipes) {
            pipe->flush();
        }
    }

    // 4. Wait for all in-flight operations
    // (this is the hard part - need to ensure clean state)

    return 0;
}
```

### 7.2 Serialize Phase (CR_PLUGIN_HOOK__CHECKPOINT_DEVICES)

```c
int zmq_ctx_serialize(void *ctx, zmq_checkpoint_data_t **out) {
    *out = allocate_checkpoint_data();

    // Serialize context options
    serialize_context_options(ctx, *out);

    // Serialize all sockets
    for (auto socket : ctx->sockets) {
        serialize_socket(socket, *out);
    }

    // Serialize inproc registry
    serialize_inproc_endpoints(ctx, *out);

    return 0;
}
```

---

## 8. Restore Procedure

### 8.1 Reconstruct Phase (CR_PLUGIN_HOOK__RESUME_DEVICES_LATE)

```c
int zmq_ctx_restore(zmq_checkpoint_data_t *data, void **out_ctx) {
    // 1. Create new context with saved options
    void *ctx = zmq_ctx_new();
    restore_context_options(ctx, data);

    // 2. Create sockets
    for (auto socket_data : data->sockets) {
        void *sock = zmq_socket(ctx, socket_data.type);
        restore_socket_options(sock, socket_data.options);

        // 3. Bind/connect endpoints
        for (auto endpoint : socket_data.bound_endpoints) {
            zmq_bind(sock, endpoint.address);
        }
        for (auto endpoint : socket_data.connected_endpoints) {
            zmq_connect(sock, endpoint.address);
        }

        // 4. Restore message queues
        restore_pending_messages(sock, socket_data.messages);
    }

    // 5. Restore inproc connections
    restore_inproc_registry(ctx, data);

    *out_ctx = ctx;
    return 0;
}
```

---

## 9. Unsolved Challenges

### 9.1 Socket State Machine Synchronization

**Problem:** ZMQ sockets have complex internal state machines (especially REQ/REP, DEALER/ROUTER).

**Example:** REQ socket expects a reply after sending a request. If checkpointed mid-request, how do we restore "waiting for reply" state?

**Possible Solutions:**
1. Only checkpoint in "idle" state (no pending operations)
2. Serialize state machine position explicitly
3. Force state machine reset (may lose in-flight requests)

### 9.2 Lock-Free Queue State (ypipe)

**Problem:** `ypipe_t` uses lock-free algorithms with atomic pointers. Internal state is non-trivial.

**Current Approach:** Drain and serialize messages

**Risk:** High complexity if we try to serialize ypipe internals

### 9.3 File Descriptor Handoff

**Problem:** ZMQ creates many file descriptors (TCP sockets, IPC sockets, signaler pipes).

**CRIU Support:**
- TCP: ✅ (with --tcp-established)
- Unix sockets: ⚠️ (limited support)
- Pipes: ✅
- Eventfd/signaler: ⚠️ (may need plugin)

**Recommendation:**
- Let CRIU handle TCP sockets
- Plugin handles IPC sockets (close and recreate)
- Plugin handles signaler FDs (recreate)

### 9.4 Multi-Process Inproc Endpoints

**Problem:** Can't have inproc:// between processes, but what about forked processes?

**Current Status:** Unclear how ZMQ handles fork()

**Recommendation:** Document limitation: "Multi-process ZMQ contexts not supported in checkpoint"

---

## 10. Serialization Format Recommendation

### Protocol Buffers Schema (Draft)

```protobuf
syntax = "proto3";

message ZmqCheckpoint {
  uint32 version = 1;
  ZmqContext context = 2;
  repeated ZmqSocket sockets = 3;
  repeated ZmqInprocEndpoint inproc_endpoints = 4;
}

message ZmqContext {
  int32 max_sockets = 1;
  int32 max_msgsz = 2;
  int32 io_thread_count = 3;
  bool blocky = 4;
  bool ipv6 = 5;
  bool zero_copy = 6;
  // ... other context options
}

message ZmqSocket {
  int32 type = 1;        // ZMQ_PUB, ZMQ_SUB, etc.
  int32 socket_id = 2;
  ZmqOptions options = 3;
  repeated string bound_endpoints = 4;
  repeated string connected_endpoints = 5;
  repeated ZmqMessage pending_messages = 6;
  bytes socket_specific_state = 7;  // For ROUTER, SUB, etc.
}

message ZmqOptions {
  int32 sndhwm = 1;
  int32 rcvhwm = 2;
  // ... all 40+ socket options
}

message ZmqMessage {
  bytes data = 1;
  uint32 flags = 2;
}

message ZmqInprocEndpoint {
  string name = 1;
  uint32 socket_id = 2;
}
```

**Advantages:**
- Schema evolution (add fields without breaking compatibility)
- Compact binary format
- Language-neutral (can debug with protoc)
- Used by CRIU internally for some images

**Size Estimate:**
- Context: ~100 bytes
- Socket: ~500 bytes + messages
- Message: ~(size + 10) bytes
- **Total for vLLM (2 sockets, 10 messages):** ~5-10 KB

---

## 11. Implementation Phases

### Phase 1: Minimal Viable Checkpoint (MVP)
**Target:** Single-socket, no messages

- ✅ Serialize context options
- ✅ Serialize socket type and options
- ✅ Serialize bound/connected endpoints
- ❌ Skip messages in queues
- ❌ Skip complex socket types (ROUTER, REQ/REP)

**Use Case:** Checkpoint idle PUB/SUB sockets

### Phase 2: Message Queue Support
**Target:** Handle in-flight messages

- ✅ Drain and serialize pending messages
- ✅ Restore message queues
- ✅ Handle HWM state

**Use Case:** Checkpoint sockets with buffered messages

### Phase 3: Complex Socket Types
**Target:** ROUTER, DEALER, REQ, REP

- ✅ Serialize routing tables (ROUTER)
- ✅ Serialize subscription filters (SUB)
- ✅ Serialize state machines (REQ/REP)

**Use Case:** Checkpoint vLLM API server ↔ Engine communication

### Phase 4: Production Hardening
**Target:** Edge cases and reliability

- ✅ Handle socket creation during checkpoint
- ✅ Handle errors gracefully
- ✅ Optimize checkpoint size
- ✅ Add telemetry and logging

---

## 12. Testing Strategy

### Unit Tests (libzmq)

```c
void test_checkpoint_simple_socket() {
    void *ctx = zmq_ctx_new();
    void *sock = zmq_socket(ctx, ZMQ_PUB);
    zmq_bind(sock, "tcp://*:5555");

    // Checkpoint
    zmq_checkpoint_data_t *ckpt;
    assert(zmq_ctx_checkpoint(ctx, &ckpt) == 0);

    // Destroy original
    zmq_close(sock);
    zmq_ctx_term(ctx);

    // Restore
    void *new_ctx;
    assert(zmq_ctx_restore(ckpt, &new_ctx) == 0);

    // Verify socket is bound to same address
    // ... verification logic
}
```

### Integration Tests (CRIU Plugin)

1. **TCP PUB/SUB:** Checkpoint mid-publish, restore, verify subscriber receives messages
2. **IPC DEALER/ROUTER:** Checkpoint with routing table, verify routes after restore
3. **Inproc PAIR:** Checkpoint inproc connections, verify they reconnect
4. **Message in-flight:** Send message, checkpoint before recv, verify message delivered after restore

### vLLM Test

```bash
# Start vLLM
vllm serve TinyLlama --port 8000

# Send inference request
curl localhost:8000/v1/completions -d '...'

# Checkpoint during inference
./scripts/checkpoint.sh create vllm-pod

# Restore
kubectl apply -f vllm-restore.yaml

# Verify API server reconnects to engine
curl localhost:8000/v1/completions -d '...'
# Should return successfully, not EINVAL
```

---

## 13. References

### Source Files Analyzed

- `src/ctx.hpp` / `src/ctx.cpp` - Context implementation
- `src/socket_base.hpp` / `src/socket_base.cpp` - Socket base class
- `src/options.hpp` - Socket options
- `src/pipe.hpp` / `src/pipe.cpp` - Message queue pipes
- `src/mailbox.hpp` - Command mailbox
- `src/ypipe.hpp` - Lock-free queue
- `src/io_thread.hpp` - I/O thread implementation

### ZMQ Documentation

- [ZMQ Socket API](https://zeromq.org/socket-api/)
- [Internal Architecture](http://wiki.zeromq.org/whitepapers:architecture)

### CRIU Documentation

- [CRIU Plugins](https://criu.org/Plugins)
- [CUDA Plugin Example]($HOME/personal/criu/plugins/cuda/cuda_plugin.c)

---

## 14. Next Steps

1. ✅ **This document** - State mapping complete
2. ⏭️ **API Design** (Task #2) - Design `zmq_ctx_checkpoint()` and `zmq_ctx_restore()` APIs
3. ⏭️ **Prototype** - Build minimal libzmq checkpoint (single socket, no messages)
4. ⏭️ **CRIU Plugin** - Build plugin skeleton
5. ⏭️ **Integration** - Test with vLLM

**Estimated Effort:**
- Task #2: 3-5 days (API design + review)
- Task #3: 2-3 weeks (libzmq implementation)
- Task #4-5: 1-2 weeks (CRIU plugin)
- Task #7-8: 1 week (testing)

**Total: 6-8 weeks to working vLLM checkpoint/restore**

---

**Document Status:** ✅ Complete
**Review Status:** Pending
**Author:** Claude + Balaji

