<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Rails fetch: concurrent transfers without pods queueing for a node's NICs

Status: proposal. Scope: `internal/agent/rails_fetch.go` and the agent chart's `rails.*` values.

## 1. What a scale-out restore spends its time on

A 2-node GB300 instance (one engine across 2 nodes, 4 GPUs each) scaled from one instance to two. Both
pods of the new instance landed on nodes other than their checkpoints' capture nodes, so every
checkpoint of the group (2 engine ranks of about 840 GB each, 4 companions of 12–154 MB) was fetched
over the rails. Timeline from the agents' logs and the cluster's events:

| Window | Time | What |
|---|---|---|
| pods created → group restore starts | **3 min 15 s** | waiting for the slowest fetch |
| of which: fetch pod unschedulable | 108 s | its node was serving another checkpoint (`Insufficient memory`, then `cannot allocate all claims`) |
| of which: sandbox retry | 17 s | the CNI could not find a network device the previous rails pod had just released |
| of which: data | 59 s | 838 GB at 13.25 GiB/s |
| CRIU restore, GPU memory load, remap, resume, release | 2 min 05 s | not in this proposal (§6) |

Each companion moved its data in 0.3 s, yet took 11–57 s from start to finish. Each engine rank moved
its data in 59 s, yet took 1 min 53 s and 3 min 12 s. The rails are fast; the transfers wait for each
other.

## 2. Why the transfers wait

1. **A rails pod holds every RDMA NIC of its node.** Each pod claims them through one DRA claim
   (`ClaimTemplate`). The devices move into that pod's network namespace, so a second rails pod on the
   node cannot schedule until the first has ended. A node serves *or* fetches, one transfer at a time.
2. **Every transfer creates its own pods.** One fetch creates a serve pod on each source node and a
   fetch pod on its own node. Two fetches from the same source need two serve pods on that node, which
   then queue. A 6-member group restore makes up to 6 fetch pods and 12 serve pods within seconds.
3. **Small checkpoints pay the full pod cost.** Pod scheduling, sandbox setup and readiness take 10 s or
   more; the data takes 0.3 s. While they run, they hold NICs a large transfer is waiting for.
4. **Source choice ignores what a node is doing.** In the timeline above, the worker rank's checkpoint
   was on two nodes. One of them was itself waiting to fetch the leader rank's checkpoint, and choosing
   it as a source blocked that node's own fetch for 108 s. The other node was free.

## 3. Proposal

### 3.1 Small checkpoints: the peer tier, on clusters with no L2

The clusters being built have no L2 volumes and no shared filesystem. A small checkpoint (a group's
etcd, NATS, frontends: 12–154 MB here) then has two things to get right: how it moves, and where it
lives.

**How it moves.** A checkpoint smaller than `rails.minBytes` goes to the HTTP peer tier and never starts
a rails pod. That tier already exists and needs no shared storage. The fetching agent reads the
checkpoint's files from the agents that hold it, over the node network, authenticated, with several
sources at once (`fetchFromPeer`). It needs no pod, no NIC claim and no scheduling, so it starts at once
and holds nothing a large transfer needs. Large checkpoints keep the rails, and the peer tier stays the
fall-back for both.

The size is already known from the peer listing. The default for `rails.minBytes` is set from
measurement, not guessed (§5, step 1): the size at which a peer copy takes as long as a rails pod's
start, about 10 s. A companion of 150 MB is far below any value that measurement can give.

**Where it lives.** With no L2, a checkpoint is only on the node that captured it and on the nodes that
restored it since. A group restore needs every member's checkpoint, so losing the one node that holds a
companion loses the whole group's capture, even when the engine ranks' checkpoints are still on other
nodes.

So, right after a group capture is recorded, every checkpoint of the group smaller than `rails.minBytes`
is copied to the capture nodes of the group's other members. The capture node's agent asks each of those
agents to fetch it, through the existing `ensure-local` endpoint, which runs the same peer tier. That
costs a few hundred MB per group and no shared storage. After it, any node that holds an engine rank's
checkpoint also holds every companion. A fetch then has more than one source for them, and losing one
node no longer loses a companion. A failed copy is logged and retried by the next restore's own fetch;
it never fails the capture.

### 3.2 One serve pod per source node, shared by every fetch

- The serve pod serves the node's whole checkpoint directory, read-only:
  `bedrock-peer serve --root <checkpoint dir> --max-fetchers N`. A fetch names its checkpoint with
  `--prefix <checkpoint id>`.
- Its name is fixed per node (`nvsnap-rails-serve-<node label value>`). A fetcher that needs a source
  creates it; `AlreadyExists` means it is there, and the fetcher uses it.
- **Lifetime.** Before using it, a fetcher patches the annotation `nvsnap.io/rails-wanted-until` on the
  serve pod to the end of its fetch's budget. The source node's agent deletes its serve pod once that
  time has passed and no fetch pod still lists the node as a source. The delete carries the pod's
  resourceVersion as a precondition, so a fetcher's patch that lands after the agent read the pod
  cancels the delete. A pod that is already terminating is never reused; the fetcher waits for it to go,
  then creates a new one.
- It is owned by the source node's agent pod, so it goes with that agent. The agent's start-up sweep
  (`sweepRailsPods`) also removes this node's serve pod.

### 3.3 Sources that are not busy fetching

`fetchOverRails` ranks the nodes that list the checkpoint:

1. Nodes with no rails fetch pod of their own, pending or running, come first. The agent knows these
   from the fetch pods' `nvsnap.io/rails-for` label.
2. Among those, a node whose serve pod is already Ready comes first.
3. A node that is fetching is used only when no other node holds the checkpoint. Then the transfers
   still queue, as today (§4).

### 3.4 What stays as it is

- The fetch pod: one per fetch, on the fetching node, created only once a source is Ready.
- The NIC claim, the security context, the copy into the checkpoint directory under the fetch marker,
  the free-space check, and the fall-through to the next tier on any failure.

## 4. Not solved here

**A node that must fetch while it is the only source of another checkpoint.** One process would have to
serve and fetch at once, because only one pod on a node can hold its NICs. `bedrock-peer` has
`fetch --then-serve`, which serves after a fetch, not during it. Asking its owners whether one process
can do both is better than nvsnap running its own job supervisor in a rails pod. Until then this case
queues, and 3.3 keeps it to nodes that really are the only source.

## 5. Plan and tests

**Step 0. Check bedrock-peer's behaviour first**, with two rails pods on the cluster and no nvsnap
change. The design rests on all three answers:

- `--prefix`: does the fetch write the checkpoint's files at the top of `--dest`, or under the prefix?
- `serve --root`: does it see checkpoints captured after it started (manifest per request, not built
  once at start)?
- `--max-fetchers` and `--slots`: memory per fetcher, to size `rails.serveMemory`.

If `serve` builds its manifest only at start, 3.2 changes: the serve pod is replaced when this node
gains a checkpoint and no fetch is using it. That is the same lifetime rule with one more trigger.

**Result (bedrock-peer 0.1.0, fd09ff3; one serve pod, two fetch pods on other GB300 nodes):**

| Question | Answer |
|---|---|
| `--prefix` layout | The checkpoint's files land at the top of `--dest`, with bedrock-peer's own `.bedrock-peer-*` entries beside them, which the agent already removes. Mode, mtime and owner are kept. |
| New checkpoints | A directory created under `--root` after `serve` started was fetched. The listing is per request, so 3.2 needs no extra trigger. |
| Concurrency | Two fetches of 32 GiB overlapped (`active=2`, at most 4), each in about 3 s, with the same content digest. |
| Memory | The serve pod used 9.05 GB idle and peaked at 9.29 GB with two fetchers: a fixed ~9 GB plus about 115 MB per fetcher. The current 32 Gi limit covers `--max-fetchers 4`. |

The 32 GiB test files had just been written, so they were in the source's page cache. The rates are
upper bounds for a read from disk; the concurrency and memory results don't depend on that.

**Step 1. Measure the HTTP peer tier** for 10 MB, 150 MB, 1 GiB and 4 GiB checkpoints between two GPU
nodes, to set `rails.minBytes`.

**Result.** A fetching node's agent read real checkpoints' files from another node's agent through the
peer endpoints (`/manifest`, `/file`), authenticated, 8 files at a time as `fetchFromPeer` does, to
`/dev/null`:

| Data | Files | Time | Rate |
|---|---|---|---|
| 12 MB (a NATS checkpoint) | 58 | 0.06 s | 0.20 GB/s |
| 153 MB (a frontend checkpoint) | 62 | 0.15 s | 1.0 GB/s |
| 1.1 GB (part of an engine rank's checkpoint) | 451 | 0.59 s | 1.9 GB/s |
| 4.3 GB | 515 | 1.0 s | 4.3 GB/s |
| 17.2 GB | 743 | 3.0 s | 5.8 GB/s |

A rails fetch takes at least 10 s before its first byte (pod scheduling, sandbox, NIC claim, serve pod
ready), then moves about 14 GB/s. At these rates the two take the same time at about 100 GB. Two things
make this an upper bound for the peer path: the reads went to `/dev/null`, not to disk, and the source's
page cache may have held some of the files. So the default is set well below the break-even:
**`rails.minBytes` = 16 GiB.** At that size the peer path takes about 3 s and a rails fetch at least
10 s. The end-to-end run (step 3) records the peer tier's real times, with writes to disk, to confirm
it.

**Step 2. Unit tests**, using the fake clientset like `rails_fetch_test.go`:

- below `minBytes`: the rails tier is not tried, and `EnsureLocal` fetches from the peer tier, with L2
  absent
- after a group capture: each small member is requested on every other member's capture node, and not
  on its own node; a failed request is logged and does not fail the capture; a member at or above
  `minBytes` is not copied
- shared serve pod: created once; reused on `AlreadyExists`; a terminating pod is not reused; the
  `wanted-until` patch is sent before the fetch pod is created
- reaper: deletes only once `wanted-until` has passed and no fetch pod lists the node; a precondition
  conflict leaves the pod in place; a fetch pod that still lists the node keeps it
- source ranking: a node that is fetching is skipped when another source exists, and used when it is
  the only one; a Ready serve pod comes before one that has to be created
- the fetch pod's arguments carry `--prefix`; the serve pod mounts the checkpoint root read-only
- failure paths unchanged: the serve pod never becomes Ready, the fetch pod never starts, or
  bedrock-peer exits non-zero. Each one falls through to the next tier and leaves no pod behind except
  the shared serve pod, which the reaper removes.
- the start-up sweep removes this node's serve pod

**Step 3. On the cluster, through NVCF, with the 2-node instance above:**

- scale 1→2, 2→1→2 and 2→3, recording the per-tier times from the agent logs
- no `Insufficient memory` or `cannot allocate all claims` events for rails pods while another source
  was free
- companions fetched by the peer tier in seconds, with L2 off in the agent's settings
- after a capture, every companion is on each engine rank's capture node; with a companion's copy on its
  capture node deleted, a scale-out still restores
- every instance serves
- no serve pod is left once the reaper's time has passed

**Expected result on the timeline in §1:**

- the companions finish in seconds;
- the worker rank's checkpoint comes from the free node;
- the leader rank's fetch pod schedules at once.

The group restore would start when its last placeholder runs, about 1 min 20 s after the pods are
created instead of 3 min 15 s, which saves about 1 min 50 s.

## 6. Found in the same restores, handled separately

- **Sandbox failures after a rails pod ends.** The CNI reported
  `failed to get network link for device ens160: Link not found`, costing about 15 s on 6 rails pods.
  The cause is in the cluster's networking, so it goes to its owners with the events. Fewer rails pods
  (3.1, 3.2) make it rarer, but do not fix it.
- **The restored clock budget** waited up to 14 s after CRIU had finished, on the critical path.
- **gpushare remap and resume** took about 45 s and 14 s. These numbers go to gpushare's owners.
- **The shared-volume copy after a capture** competes for the node's disk with a restore on the same
  node (an overlay replay took 87 s against 4 s without it). This is part of the move away from shared
  storage.
