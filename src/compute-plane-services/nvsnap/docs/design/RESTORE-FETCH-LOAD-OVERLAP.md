<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Overlapping the checkpoint fetch with the GPU load

Status: proposed. Scope: the agent's restore (`restore_v2.go`, `cascade_fetch.go`, `rails_fetch.go`), the
rails fetch tool, and `nvsnap-gpu-suspend resume` (gpushare). Goes with the clock budget proposal,
[RESTORE-CLOCK-GPU-RELOAD.md](RESTORE-CLOCK-GPU-RELOAD.md) section 6.

## 1. The problem

A restore on a node that does not hold the checkpoint fetches all of it to the node's NVMe, then restores
from there. The two steps run one after the other. A 2-node Kimi-K3 scale-out (mv194):

| Step | Time |
|---|---|
| Rails fetch, both nodes in parallel, 781 GiB per rank | 66 s (16:34:17 to 16:35:23) |
| CRIU restore | 5 s |
| GPU load, from NVMe into GPU memory | 42 s |
| Remap, resume (and the clock wait, section 6 of the clock doc) | the rest |

Of the 781 GiB, 747 GiB is the gpushare chunk store, the GPU memory; the CRIU images are 34 GiB. The store
is content-addressed (a chunk's name is its hash), so zeros and repeated data are already stored once: 15,705
chunk references, 14,403 chunks. There is little to shrink without changing what is restored.

So the fetch and the load each move the whole GPU memory, one after the other: fetch at about 12 GiB/s per
node, load at about 18 GiB/s.

## 2. What has to hold

- The restored bytes are exactly the captured bytes, as today. Nothing is skipped, recomputed or reloaded
  from elsewhere.
- A process never reads a partly written chunk.
- A fetch that fails fails the restore, as today, and quickly: the load must not wait out a chunk that will
  never arrive.
- A restore from a checkpoint already on the node works as today.

## 3. Proposal

The restore starts as soon as what each step needs has arrived, not when the whole checkpoint has.

**Fetch order.** The fetch sends the checkpoint in the order the restore uses it:
1. Everything but the chunks: CRIU's images, the metadata, the per-GPU chunk lists (`gpu-<pid>.chunks`), the
   recorded clock spans.
2. The chunks, in the order the load reads them: the per-GPU lists interleaved, so that every GPU's load
   progresses at once.

Each file is written under a temporary name and renamed into place when complete, so a file that exists is
whole. When the fetch ends it writes a marker: `fetch-done`, or `fetch-failed` with the reason.

**CRIU** starts once part 1 has arrived. The agent waits for that point, not for the whole fetch.

**The GPU load** starts once CRIU has restored the processes, as today, but may find chunks that have not
arrived yet. For each chunk it needs, `nvsnap-gpu-suspend resume` waits until the chunk's file exists, then
reads it. It stops waiting, and fails the restore, when the store holds `fetch-failed`, or when `fetch-done`
is there and the chunk still is not (a checkpoint missing a chunk). A new option names the marker directory
(`--wait-for-store DIR`); without it, the load behaves as today.

**What changes for each party:**

| Part | Change | Owner |
|---|---|---|
| Rails fetch tool | the fetch order; temporary names and rename; the done and failed markers | nvsnap |
| Peer HTTP fetch (`cascade_fetch.go`) | the same order, renames and markers | nvsnap |
| Agent restore | start CRIU after part 1; pass `--wait-for-store` to the resume; keep the fetch's failure as the restore's | nvsnap |
| `nvsnap-gpu-suspend resume` | `--wait-for-store DIR`: wait for each chunk, stop on the markers | gpushare |

### Expected effect

The load becomes bound by the fetch: it reads each chunk shortly after it lands, at the fetch's 12 GiB/s
rather than its own 18. On the scale-out above:

| | Today | Overlapped |
|---|---|---|
| Fetch | 66 s | 66 s |
| CRIU | 5 s, after the fetch | 5 s, starting a few seconds into the fetch |
| GPU load | 42 s, after CRIU | runs during the fetch, ends shortly after it |
| Fetch start to load done | about 113 s | about 70 s |

About 40 s off a fetched restore. A restore from a checkpoint on the node is unchanged.

## 4. The clock budget

The clock budget covers CRIU's start to the GPU resume (the clock doc, section 6). Overlapped, a fetched
restore's CRIU starts during the fetch, so its span includes the rest of the fetch, and a fetched restore
takes longer from CRIU's start than a local one. One history for both would make the budget wait on local
restores or jump on fetched ones. So the history keeps the two apart: each recorded span is marked `local` or
`fetched`, and a restore budgets from the spans of its own kind, falling back to the other kind's (then to the
estimate) when it has none.

## 5. Risks

- **Chunk files written in place.** If the fetch tool or the peer fetch writes a chunk under its final name
  before it is whole, the load could read a partial chunk. The rename is the guarantee; both writers need a
  test that a reader never sees a partial file.
- **A stalled fetch.** The load waits on a chunk that is coming, so a fetch that stalls without failing
  stalls the restore. The fetch's own timeout (`railsFetchTimeout`) writes `fetch-failed`, which stops the
  load. The restore already waits on the fetch today, so this is not a new kind of wait.
- **Disk space.** Unchanged: the whole checkpoint still lands on the node, so later restores there are
  local.

## 6. Tests

- Fetch tool and peer fetch: the order (part 1 first, chunks in load order); no reader sees a partial file;
  `fetch-done` after the last file; `fetch-failed` on an error and on the timeout.
- Agent: CRIU starts after part 1, not after the whole fetch; a fetch failure during the load fails the
  restore with the fetch's error; a local restore passes no `--wait-for-store`.
- gpushare (the GPU tool owners): a load against a store filled slowly by a writer gives the same GPU memory as a load
  from a full store; it fails at once on `fetch-failed`; it fails at `fetch-done` with a chunk missing.
- On the cluster: a 2-node Kimi-K3 scale-out to nodes without the checkpoint; the restored instance serves
  and answers as before; fetch start to load done drops from about 113 s toward 70 s.

## 7. Later

The same chunk order makes the next step possible: chunks received over the NICs straight into host or GPU
memory, skipping the NVMe write and read, and chunks from several sources at once (every node holding the
checkpoint). Both keep the restored bytes identical.
