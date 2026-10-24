<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Restored clocks through the GPU reload

Status: in use (CRIU `--timens-no-wait`, gpushare `--clock-at`, agent). Section 6, a budget that aims low, is proposed. Scope: the agent (`restore_clock.go`, `restore_v2.go`), CRIU's `--timens-budget`, and
`nvsnap-gpu-suspend resume`.

## 1. The problem

A restored task's clocks are meant to continue from the values its dump recorded, so a task that was
waiting on a timeout when it was dumped does not find it expired. `--timens-budget` does this for CRIU:
the restored clocks start that far behind the dumped values, and CRIU resumes the tasks only once their
CLOCK_MONOTONIC has reached them (`timens_wait_dumped_clocks`). The agent sizes the budget from this
checkpoint's last CRIU restore on the node (`restore_clock.go`).

With gpushare, CRIU's restore is not the end. The workload's processes stay held after CRIU while
`nvsnap-gpu-suspend` loads their GPU memory, remaps the fabric handles and resumes them. All that time,
their clocks run. In a 2-node scale-out of a 2-node GB300 engine:

| Step | Time |
|---|---|
| CRIU restore done, budget 7 s | 00:03:31 |
| GPU memory loaded, handles exchanged, remapped | 00:03:31 → 00:05:08 |
| `resume hold`: the processes run | 00:05:08 |

At 00:05:13, five seconds after it resumed, vLLM's EngineCore logged
`No available shared memory broadcast block found in 60 seconds`. The same line appears after every GPU
restore since. Each restored process sees about 90 s go by at once.

Anything with a timeout under that fires at once. NCCL's RAS thread declares a peer dead after 60 s
without contact (`RAS_PEER_DEAD_TIMEOUT`). In one restore, the dead-peer updates that followed reached a
use-after-free in NCCL 2.30.7: `rasMsgHandlePeersUpdate` frees the connection of a newly dead peer
(`rasDeadPeersUpdate` → `rasConnDisconnect` → `rasConnTerminate`), then enqueues its reply on that freed
connection (`ras/peers.cc:651`). That rank crashed and the restore failed. The NCCL bug is reported
separately. The clock jump is ours.

## 2. What has to hold

At the moment the restored tasks run again, their CLOCK_MONOTONIC reads the dumped value, plus at most a
small overrun. No task ever reads a value earlier than one it read before the dump.

All the tasks of a restore share one time namespace, so they share one offset. So every task has to be
held until the clocks reach the dumped value, including any process gpushare does not hold today (a
follower's health server, for one), not only the GPU processes.

## 3. Proposal

**Budget.** The budget covers the whole restore up to the resume: CRIU, the GPU memory load, remap and
resume. The agent records that span after each restore (from CRIU's start to `resume hold`), not only
CRIU's, and uses it with the same margin next time. Before a checkpoint's first restore it estimates the
span from the CRIU images and the GPU memory to load.

**The wait moves to the resume.** CRIU starts the clocks behind by the budget but does not wait itself. If
it did, a budget of about 100 s would hold back the GPU load, which needs the tasks restored, and add those
100 s to every restore. Instead, just before the resume, the tasks wait until their clocks reach the
dumped values. That wait is where `nvsnap-gpu-suspend resume` releases them: it can read the restored
clock the way CRIU's `timens_wait_dumped_clocks` does (the time namespace's offsets and the dumped value
from the `timens` image).

A restore faster than its budget waits the difference at the resume, before the tasks run. A slower one
leaves the overrun counted, as CRIU's budget does today. The recorded span makes the next one right.

| Part | Change | Owner |
|---|---|---|
| CRIU (our fork) | a restore option that applies `--timens-budget` without waiting; used with `--leave-stopped` | nvsnap |
| `nvsnap-gpu-suspend resume` | `--clock-at <ns>`: wait until the restored clock reaches `<ns>` before the thaw | gpushare |
| agent | record and use the whole span as the budget; read `<ns>` from the `timens` image; resume CRIU's listed tasks after `resume` | nvsnap |

## 4. Agreed with gpushare

- **What `resume` releases.** Only the pids it is given: the GPU processes `stop` left SIGSTOPped, which
  CRIU dumps and restores stopped. It ptrace-freezes every thread of those pids except the driver's
  restore thread and gpushare's control thread, sends SIGCONT, and keeps them frozen through load, remap
  and resume. Every other task CRIU restored (the multiprocessing resource tracker, a health server) runs
  as soon as CRIU finishes. So gpushare alone cannot hold the whole tree.
- **So CRIU restores the whole tree stopped** (`--leave-stopped`, `final_state = TASK_STOPPED`), and
  applies the budget without waiting. `timens_wait_dumped_clocks` runs before the detach whatever the
  final state, so a no-wait option combines with `--leave-stopped` cleanly. That still has to be tested.
- **`nvsnap-gpu-suspend resume --clock-at <ns>`.** Before it thaws the GPU processes, the tool waits until
  the restored CLOCK_MONOTONIC reaches `<ns>`. It reads the restored clock as its own CLOCK_MONOTONIC,
  minus its own time namespace's monotonic offset, plus the one in `/proc/<pid>/timens_offsets` of a
  restored pid, as CRIU's `timens_wait_dumped_clocks` does. It does not enter the time namespace.
- **`<ns>` comes from the agent.** It decodes the checkpoint's `timens` image (`crit decode`, as it
  already does for `pstree.img`). The image holds the dumped namespace's CLOCK_MONOTONIC and
  CLOCK_BOOTTIME at the moment the tasks were seized (`timens_note_seized`), not at the end of the dump.
  The wait uses CLOCK_MONOTONIC; BOOTTIME moves with it.
- **After `resume` returns,** the agent resumes the other tasks. CRIU writes the pids of the tasks that
  ran at the dump to `timens-resume-pids` in the images directory before it stops them all, so a task
  that was stopped at the dump stays stopped. The agent finds the listed pids in CRIU's pid namespace
  and sends each SIGCONT, leaving out the GPU processes, which `resume` thawed. No task then runs
  before the clocks reach the dumped values.
- **The agent uses this only when both halves are there:** the bundled CRIU lists `--timens-no-wait`
  and the bundled `nvsnap-gpu-suspend` lists `--clock-at` in its usage. Otherwise CRIU's own budget
  applies, as before.
- **The `open` reply timeout** is done (gpushare 38fa2235f): `open` goes to every pid at once, each with
  at most 10 s to reply.

**To watch in the first run:**
- The driver's restore thread and gpushare's control thread run during load and remap while the
  clock is behind the dumped value. The control thread uses CLOCK_REALTIME only, which is not
  namespaced. Whether the driver's restore thread has a CLOCK_MONOTONIC timeout that a clock behind the
  dump could trip is not known.
- With `--hold-gate`, the CPU side runs from the thaw and only GPU launches wait for `open`. That gap
  (about 6 s here) still counts as run time, well under RAS's 60 s.

## 5. Tests

- **Agent:** the budget is the recorded whole span with its margin, falls back to the estimate, and is
  bounded. The span is recorded only for a restore that resumed.
- **CRIU:** with the new option, the restored clock starts behind by the budget and CRIU returns without
  waiting.
- **On the cluster:** a Kimi-K3 2-node restore logs no `No available shared memory broadcast block`
  after the resume. A small probe in the workload that reads CLOCK_MONOTONIC before the dump and after
  the restore sees the gap within the budget's margin.

## 6. Proposed: a budget that aims low

### What the first runs showed

Kimi-K3, 2 nodes, mv194 (gpushare with the host-buffer prefault). Both ranks agree within 0.1 s.

| Restore | Budget | Work (CRIU start to GPU resume) | Waited for the clock |
|---|---|---|---|
| Verify restore, the checkpoint's first | 2 m 3 s (estimate) | 72.8 s | 50 s |
| Scale-out on two other nodes | 1 m 25 s (72.8 s x 1.1 + 5 s) | 55.5 s | 30 s |

The clock fix works: no restored process saw time jump, and nothing timed out. But the wait cost 50 s and 30 s,
a third of each restore. Two things make the budget too large:

- **The first-restore estimate** adds 60 s for the GPU resume (`restoreGPUSlack`). It was sized for the
  30-48 s first remap per node that the prefault has since removed: remaps now take 3-8 s.
- **The margin over the last restore.** Restores of one checkpoint vary: 72.8 s, then 55.5 s, the second with
  every remap at 3 s instead of up to 7.6 s. Budgeting the last restore plus 10 % and 5 s turns that
  variance, and the margin, into waiting.

### What each kind of error costs

The budget has to be set before the restore runs: a time namespace's offsets can be written only before its
first task enters it, which is when CRIU restores the tasks. So the budget is a prediction, and it misses one
way or the other:

- **Too large:** every restore waits the difference before it serves.
- **Too small:** the restored tasks see their clocks jump forward by the overrun when they resume. Only a jump
  that reaches a timeout does harm. The one we hit is NCCL RAS's 60 s (`RAS_PEER_DEAD_TIMEOUT`); vLLM's shared
  memory broadcast warns at 60 s. A jump of a few seconds is what any process sees from a busy host.

So the budget should aim low, and bound the jump instead of the wait.

### Proposal

- **History.** `restore-span-ms` keeps the work of the last 5 restores of the checkpoint, one per line, newest
  last. A file with one value, as written today, reads as a history of one. The file travels with the
  checkpoint when another node fetches it (seen on the scale-out above), so a fetched checkpoint starts with
  its source's history.
- **Budget from history:** `max(min(spans), max(spans) - J)`, no margin. `J`, the largest jump a restore
  within the history's range can see, is 20 s, a third of the 60 s timeout.
  - Spans that agree within `J`: the budget is the fastest. A restore as fast as the fastest waits nothing;
    a slower one jumps by at most `J`.
  - Spans that spread wider: the budget rises to keep the slowest one's jump at `J`, and the others wait.
- **First restore:** the estimate's GPU allowance drops from 60 s to 15 s. On the restore above, that makes the
  estimate about 78 s against 72.8 s of work: 5 s of waiting.
- **Unchanged:** the 15 min bound; the recorded span excludes the wait; the agent uses this only when CRIU has
  `--timens-no-wait` and the tool has `--clock-at`.
- **Log** the budget, the history it came from, and the outcome: `waited Ns` or `overran by Ns`. An overrun past
  `J` logs a warning naming the 60 s timeouts.

On the scale-out above, with the history (72.8 s), the budget would have been 72.8 - 20 = 52.8 s raised to the
minimum 72.8 s: still 17 s of waiting, since one value is both the minimum and the maximum. From the second
restore on (55.5, 72.8): `max(55.5, 52.8)` = 55.5 s, so a restore like the scale-out waits nothing, and one
like the verify restore jumps 17 s.

### Tests

- Budget: one value; several within `J` (the minimum); spread wider than `J` (the maximum minus `J`); the
  15 min bound; an unreadable or empty history falls back to the estimate.
- History: keeps the last 5, newest last; reads the one-value file written today; a bad line is skipped,
  not fatal.
- Estimate: the 15 s GPU allowance on a checkpoint of known CRIU image and GPU store sizes.
- On the cluster: three restores of one Kimi-K3 checkpoint, each logging budget, work and wait or overrun,
  no `No available shared memory broadcast block` after the resume, and every jump under `J`.

