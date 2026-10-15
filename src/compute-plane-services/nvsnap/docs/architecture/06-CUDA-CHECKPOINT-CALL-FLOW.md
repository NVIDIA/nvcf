# cuda-checkpoint call flow

How a GPU checkpoint actually gets created from `kubectl` → cuda-checkpoint subprocess. Useful when debugging anything related to GPU state capture (missing GPU dump, libcuda discovery, CRIU plugin errors).

## End-to-end flow

```
test-e2e.sh (host)
  │
  ▼
kubectl exec into nvsnap-agent pod (or POST /v1/checkpoint via API)
  │
  ▼
internal/agent/checkpoint.go            ┌─── Pre-checkpoint
  HandleCheckpoint() / Checkpoint()     │    Agent → cuda-checkpoint via nsenter
   ├──► internal/cuda/checkpoint.go     │    (DIRECT path, host mntns).
   │     runCudaCheckpoint(             │    Used for LOCK / GET-RESTORE-TID /
   │       action=lock/checkpoint/...,  │    UNLOCK actions where the agent
   │       targetPID)                   │    drives state transitions itself.
   │       │                            │
   │       ▼                            │
   │     nsenter -t 1 -m -- env         │
   │       LD_LIBRARY_PATH=<host paths> │
   │       /var/lib/.../cuda-checkpoint │  ← wrapper, copied to hostPath
   │         (--action lock --pid N)    │     by the agent at startup
   │       │                            │
   │       ▼                            │
   │     cuda-checkpoint.real           │  ← real NVIDIA binary
   │       (talks to libcuda.so.1       │
   │        and the workload's          │
   │        CUDA driver state)          │
   │                                    └───
   │
   │  Once GPU state is locked, agent kicks off CRIU dump:
   ▼
internal/criu/rpc_dump.go               ┌─── Dump via go-criu RPC
  DumpRPC()                             │
   ├──► go-criu (vendored)              │
   │     spawns "criu swrk"             │  ← separate child process
   │     (inherits agent's env)         │     inside the agent container
   │       │                            │
   │       ▼                            │
   │     criu/cr-dump.c                 │
   │       walks /proc, freezes,        │
   │       opens images, dlopens        │
   │       plugins                      │
   │       │                            │
   │       ▼                            │
   │     criu/plugins/cuda/             │  ← our CRIU fork carries this
   │       cuda_plugin.c                │     (NVIDIA upstream + our patches)
   │       │                            │
   │       ▼                            │
   │     cuda_plugin.c:110              │
   │       child_pid = fork();          │
   │     cuda_plugin.c:131              │
   │       execvp("cuda-checkpoint",    │  ← CRIU-SIDE invocation. PATH lookup.
   │              args);                │     PATH=/criu-bundle:... → resolves to
   │       │                            │     /criu-bundle/cuda-checkpoint = wrapper
   │       ▼                            │
   │     /criu-bundle/cuda-checkpoint   │  ← bash wrapper, baked into agent image
   │       (wrapper script)             │
   │       unset LD_PRELOAD             │     (so libnvsnap_intercept.so doesn't
   │       export LD_LIBRARY_PATH=...   │      pollute cuda-checkpoint's stderr,
   │       exec cuda-checkpoint.real    │      which CRIU parses for restore-tid)
   │       │                            │
   │       ▼                            │
   │     cuda-checkpoint.real           │  ← same binary as DIRECT path, but
   │       loads libcuda.so.1 via       │     this invocation runs INSIDE the
   │       ld.so + LD_LIBRARY_PATH      │     agent container's mount namespace,
   │       set by wrapper               │     not nsenter'd into host. So
   │                                    │     LD_LIBRARY_PATH paths must be
   │                                    │     /host/* — the in-container view
   │                                    │     of the host filesystem.
   │                                    └───
   │  CRIU dump finishes; agent finalizes checkpoint metadata, uploads to blob
   │  store, etc.
```

## Two distinct cuda-checkpoint invocation paths

Both eventually run the same `cuda-checkpoint.real` binary. They differ in **who** launches it and **which mount namespace** the launcher lives in.

| | Direct path | CRIU-plugin path |
|---|---|---|
| Launched by | `internal/cuda/checkpoint.go:runCudaCheckpoint()` (Go code in the agent) | `criu/plugins/cuda/cuda_plugin.c:131` (C plugin loaded by CRIU at dump time) |
| Mount namespace | Host mntns (`nsenter -t 1 -m`) | Agent container mntns |
| LD_LIBRARY_PATH paths look like | `/run/nvidia/driver/usr/lib/x86_64-linux-gnu` (host-eye) | `/host/run/nvidia/driver/usr/lib/x86_64-linux-gnu` (container view of host) |
| Set by | Agent's `findHostNvidiaLibs()` builds the env directly | Wrapper script `/criu-bundle/cuda-checkpoint` hardcodes the list |
| Used for | Lock / get-restore-tid / unlock — agent-driven state transitions | The actual GPU state dump during CRIU's freeze stage |

Failure modes diverge by path:
- **Direct path fails** if the agent's path-probing in `findHostNvidiaLibs` doesn't find a matching directory on the host. Logged as `"Could not find NVIDIA libs on host, cuda-checkpoint may fail"`.
- **CRIU-plugin path fails** if the wrapper's hardcoded list doesn't cover the cluster's driver layout. Surfaces as `cuda_plugin: cuda-checkpoint output ===> ... libcuda.so.1: cannot open shared object file` in CRIU's dump.log.

When the WRAPPER list doesn't include the right path AND `cuda-checkpoint.real` runs anyway (because PATH found the wrapper), `ld.so` fails to resolve libcuda.so.1. The cuda_plugin sees nonzero exit and **silently falls back to "no GPU state to dump"** — the checkpoint completes successfully (CPU pages + rootfs diff captured) but contains zero GPU memory. The dump-side error is in cuda_plugin's log lines, not in the agent log.

This silent-fall-through is what we observed on GCP-H100-a 2026-05-23: 4.4 GB checkpoint vs 28 GB baseline.

## Key files referenced

| File | Role |
|---|---|
| `internal/agent/checkpoint.go` | HTTP `/v1/checkpoint` handler; orchestrates lock → dump → unlock |
| `internal/cuda/checkpoint.go` | Direct-path invocation of cuda-checkpoint via nsenter. Contains `findHostNvidiaLibs()` |
| `internal/criu/rpc_dump.go` | go-criu RPC client. Spawns `criu swrk`. Sets `cmd.Env` for the CRIU process. |
| `internal/criu/manager.go` | CRIU process management. Sets LD_LIBRARY_PATH on CRIU itself (not cuda-checkpoint) |
| `criu/plugins/cuda/cuda_plugin.c` (in our CRIU fork) | The dump-side cuda-checkpoint launcher inside CRIU |
| `docker/agent/cuda-checkpoint` (wrapper, baked into image as `/criu-bundle/cuda-checkpoint`) | LD_LIBRARY_PATH setup + LD_PRELOAD unset + exec real binary |

## Environment-variable propagation

For an env var to reach `cuda-checkpoint.real` on the CRIU-plugin path, it must traverse:

```
agent process  --[cmd.Env in rpc_dump.go]-->  criu swrk
criu swrk      --[inherited]-->                cuda_plugin (loaded inside criu)
cuda_plugin    --[execvp inherits env]-->      cuda-checkpoint (wrapper)
wrapper        --[exec inherits env]-->        cuda-checkpoint.real
```

So `NVSNAP_CUDA_LIB_DIR` set in `rpc_dump.go`'s `cmd.Env` propagates all the way down, and the wrapper can consume it. This is the hook for runtime libcuda discovery (issue #40).
