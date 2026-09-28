<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Capture the PID namespace, so restore can create a fresh one

Status: proposed. Not implemented -- the trial implementation was removed
(see "First attempt hung" below), and the immediate failure it targeted has since
been fixed another way (the placeholder pid reservation).

## The failure

CRIU restore dies with one of:

```
Error (criu/cr-restore.c:1242): Can't fork for 364: File exists
Error (criu/pie/restorer.c:2878): Unable to create a thread: -17
```

Both are `EEXIST` from `clone3(set_tid=N)`: the PID the restore needs is
already taken in the target namespace.

Measured on a full suite run, 7 single-GPU workloads on one agent build: 6
failed this way, 1 passed. Earlier runs of the same build reported different
pass counts, which is the tell -- see "Why it looks flaky".

## Why it happens

Capture targets the workload's session leader, not the container's init:

```go
// internal/agent/checkpoint_v2.go
targetHostPID := hostPID
if len(gpuPIDs) > 0 {
    if sid, err := sessionID(procBase, gpuPIDs[0]); err == nil && sid > 1 {
        targetHostPID = sid   // <- a subtree, not the namespace root
    }
}
```

That choice propagates:

1. CRIU dumps a **subtree** rooted at the session leader.
2. CRIU writes a pidns image only when the dump target is the namespace root.
   A subtree dump has none -- restore logs `No pidns-1.img image`.
3. Without that image restore cannot create a namespace, so it recreates the
   original PIDs inside the placeholder pod's **existing** namespace.
4. PIDs cannot be renumbered. They are baked into the memory image: cached
   `getpid()` values, pthread TCBs, robust futex lists, `sempid`. CRIU must
   reproduce them exactly.
5. If the placeholder has already consumed one of those PIDs, `clone3(set_tid)`
   returns `EEXIST` and the restore fails.

## Why it looks flaky

Whether step 5 fires depends on where the placeholder's PID counter happens to
sit when restore runs -- a function of how many processes the pod started, how
busy the node is, and timing. The same workload on the same build passes or
fails run to run.

This matters for how past results should be read: a green suite was not
evidence of correctness, only of a lucky PID counter. Any historical pass rate
for the CRIU path should be treated as unverified.

## The fix

Dump the container's PID-namespace init instead of the session leader. CRIU
then records the namespace, and restore creates a fresh one where every PID is
free by construction. The collision becomes impossible rather than unlikely.

Cost: the dump includes the container's init process (typically the `bash` the
workload was launched under). That is cheap -- a shell, no GPU state -- and is
what a container checkpoint normally contains.

## First attempt hung, and why

Measured, not theorised. With the gate on, the dump ran as:

```
nsenter -t <hostPID> -m -p -n -i -u -r -w -- criu dump -t 1 ...
```

It never returned. No image files, no `dump.log`, and the agent log stops at
the invocation. The harness gave up at 10m13s, well inside CRIU's own 1200s
timeout, so nothing failed -- it hung.

The likely mechanism is the `-p` in that nsenter. It places CRIU *inside* the
container's PID namespace, which is harmless when the target is a subtree
(CRIU is not a descendant of the session leader) and self-defeating when the
target is the namespace root: CRIU is then a member of the very tree it is
freezing, so it stalls on itself.

Stock container checkpoint does not do this. `runc checkpoint` runs CRIU in the
host PID namespace and names the container init by its *host* pid, letting CRIU
discover and record the namespace from the target. So the next attempt should
drop `-p` and pass the host pid rather than `-t 1`, which is both the fix and a
further step onto the standard path.

This is unverified. It is the leading hypothesis, not a conclusion.

## Alternatives rejected

**Bump `ns_last_pid` before restore.** This is what actually shipped, and the
reasoning that first rejected it here was wrong on the facts.

The claim was that the in-pod write returns `EPERM` even when privileged. It
does not. `/proc` is mounted `rw` in these pods and the write succeeds --
measured in a live placeholder, the next child landed at pid 100003. That false
premise is what removed the reservation in the first place and produced a 79%
restore failure rate that read as flakiness.

It is prevention rather than impossibility: it works because nothing else in a
restore pod allocates pids between the bump and CRIU's forks. That assumption
holds for the pods we control and is enforced -- the agent refuses to restore
into a pod whose pid range was never pushed up. Dumping the namespace root, as
proposed here, would remove the requirement rather than satisfy it, which is why
this document is still worth keeping.

**Restore into a freshly unshared PID namespace.** Keeps the dump unchanged and
guarantees free PIDs, but leaves the workload in a nested namespace. Needs
proof that readiness probes, `kubectl exec`, and the GPU driver's view still
behave. Worth revisiting if dumping the init turns out to have its own problems.

**Make the placeholder consume fewer PIDs.** Reduces the odds. Same objection
as `ns_last_pid`: it tunes a race rather than removing it.

## Rollout

If this is picked up again, it needs a real configuration surface -- an agent
flag plumbed through chart values -- not an environment variable. The trial used
one and it was removed rather than merged: an env switch that changes what a
capture contains is invisible in the pod spec, untyped, and easy to leave
behind.

Validation before it becomes the default:

1. Confirm the dump now writes a pidns image, and restore logs a namespace
   creation rather than `No pidns-1.img image`.
2. Full suite green across single-GPU workloads, repeated -- one green run
   proves nothing here, given the failure is probabilistic.
3. Confirm the restored process still passes inference, not merely starts.
4. Bump `CaptureFormatVersion`: captures taken before this contain no pidns
   image and must not be replayed by an agent that expects one.

Step 4 is not optional. Without it an upgraded agent silently reuses old
captures and the fix appears not to work -- the same trap that made the
runtime-directory fix look ineffective until the version was bumped.
