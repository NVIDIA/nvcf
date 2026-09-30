<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# Validating checkpoint/restore of a Dynamo workload

Status: proposed. No rung has been run.

How we establish whether nvsnap can snapshot and restore Dynamo workers, in
aggregated and disaggregated mode, without producing a result that looks green
and means nothing.

## The trap that shapes the whole design

Dynamo is built to survive worker loss. The router re-routes, workers
re-register. So a restore that fails completely can still produce a successful
inference request, served by a different worker, and a naive test passes.

This is the same failure we have already been caught by: a restore test that
silently measured a cold start, because a pod the webhook declined still starts,
still serves, and still passes every functional check. Here the masking is
stronger, because the system is designed to hide exactly this.

Two rules follow, and no result counts without both:

1. Run exactly one worker of the type under test, so there is nothing to route
   around.
2. Assert the request was served by the restored worker specifically, by worker
   identity, not that a request succeeded.

## Preconditions

- Dynamo platform installed by Helm per `tools/ncp-local-cluster/docs/dynamo-operator.md`
  (`dynamo-platform`, namespace `dynamo-system`). KAI Scheduler is already an
  NVCF cluster prerequisite; Grove is the likely new dependency.
- The sample topology at
  `examples/function-samples/helmchart-samples/dynamo-operator-sample` deploys
  unchanged and serves inference. Record what healthy looks like before
  attempting any capture.
- Agent build and chart values recorded for every run. A result attributed to
  the wrong build is worse than no result.

## Established from source

Each of these was read from the Dynamo operator and runtime, not assumed.

### Worker identity is addressable per request

`lib/llm/src/protocols/common/extensions.rs` maps request headers onto routing
extensions:

    x-dynamo-worker-instance-id   -> backend_instance_id, decode_worker_id
    x-dynamo-prefill-instance-id  -> prefill_worker_id

This is stronger than being able to observe which worker served a request. It
lets us pin a request to a specific instance, so a request aimed at a restored
worker either succeeds through that worker or fails. It cannot be quietly
served by a healthy peer.

That defeats the masking problem directly, and it is the single most important
finding for this plan. Every rung below uses pinning rather than post-hoc
attribution.

### Discovery on Kubernetes is readiness-driven, not lease-driven

The operator sets `DYN_DISCOVERY_BACKEND=kubernetes` for pods, so the etcd lease
path (10 second TTL, endpoints deleted on expiry) does not apply to our
deployments. Instead, per the operator's service-discovery documentation:

- Each pod runs a discovery daemon watching EndpointSlices and
  `DynamoWorkerMetadata` CRs.
- A pod is discoverable only when it is ready in an EndpointSlice AND has a
  corresponding CR.
- The CR is named after the pod and carries an owner reference, so it is
  garbage collected when the pod is deleted.
- Readiness for a worker means its `generate` endpoint is healthy.

The consequences for checkpoint/restore are favourable and specific:

- Restore in place keeps the pod, so its CR survives and no re-registration is
  required.
- A frozen process fails its readiness probe, leaves the EndpointSlice, and
  traffic reroutes. That is orderly rather than an error path.
- On restore the probe passes again and the worker returns to the EndpointSlice.

So the recovery mechanism we depend on already exists and is the same one
Dynamo uses for ordinary pod churn. Deleting the pod, by contrast, destroys the
CR by garbage collection, which is a second reason not to use our harness's
delete-and-replace model.

### Transport and capture constraints

- The sample's prefill worker publishes KV events over ZMQ and transfers KV via
  the NIXL connector. NIXL stages transfer metadata through the pod's
  `/dev/shm`, which nvsnap already captures and replays.
- CRIU cannot dump processes using RDMA (checkpoint-restore/criu#267). NIXL runs
  over UCX, which selects a transport at runtime, so whether a worker is
  capturable at all depends on what UCX picks on the target hardware. This is a
  runtime check, not a source question, and it must be answered at rung 0.

## Still assumed

- That a worker can reach ready as a standalone pod rather than requiring
  operator launch and Grove gang membership. `DYN_DISCOVERY_BACKEND` is
  configurable (kubernetes, etcd, memory, nats, file), so a standalone worker
  against a memory or etcd backend is plausible, but unproven. This decides
  whether rungs 1 and 2 can use plain pods or must drive the CRD.
- That restoring in place into an operator-owned pod does not trip
  reconciliation. The discovery mechanism above suggests it should not, since
  nothing is deleted, but the operator may still react to a pod going
  NotReady for the duration of a capture.
- That a restored process re-establishes its ZMQ KV-events publisher and NIXL
  agent state. Discovery recovering does not imply these do.

## Why restore must happen in place

Our test harness deletes the source pod and creates its own placeholder. Against
an operator-managed workload that is wrong: the operator sees its worker missing
and creates a fresh cold one, leaving two pods with the router likely favouring
the operator's. The test then passes while proving nothing.

Use the production path instead. Annotate the component so the operator's own
pod carries `nvsnap.io/restore-from`; the webhook rewrites that container's
command to `restore-entrypoint` and stashes the original in
`NVSNAP_ORIG_COMMAND`; the agent restores into the pod in place. Nothing is
deleted, so the operator has nothing to reconcile.

This also means rungs 4 and 5 exercise the production path rather than a
test-only convention, which is worth more than the convenience of the harness.

## Instrumentation

Use request pinning, established above. For each rung:

- Send the verification request with `x-dynamo-worker-instance-id` set to the
  restored worker's instance id (and `x-dynamo-prefill-instance-id` for
  disaggregated rungs).
- A pinned request that succeeds proves the restored worker served it. A pinned
  request that fails is a real failure rather than a reroute.
- Record the instance id before capture and confirm it after restore. An
  instance id that changed is itself a finding: it means the worker
  re-registered as a new instance rather than resuming.

Verify pinning works against a healthy deployment at rung 0, before any capture.
If a pinned request can still be served by another worker, this plan's central
assumption is wrong and the design must change.

## Ladder

Each rung is a stop-and-decide point. Each is run repeatedly, not once: the
restore failures we have already debugged were probabilistic, and a single pass
cannot distinguish working from lucky.

### Rung 0: baseline

Deploy the sample unchanged. Record cold-start time to first token, worker
identities, and healthy coordination state. Everything later is compared to
this.

### Rung 1: aggregated, TP=1, cache-directory capture

Weights and compiled kernels only, no process state. Lowest risk and it proves
the plumbing end to end.

Pass: the restored worker serves a correct response, identified as the restored
worker, with a measurable improvement over rung 0's cold start.

### Rung 2: aggregated, TP=1, criu-v2 process capture

The first real test. Expected work: coordination re-registration, the ZMQ KV
events publisher, and NIXL agent metadata in `/dev/shm`.

Pass: as rung 1, plus the restored process is the captured process, not a cold
start wearing its name. Verify by process start time or a capture-time marker,
not by readiness.

### Rung 3: aggregated, TP above 1

Expected to fail. Multi-GPU is blocked by peer state: `cuda-checkpoint
--launch-job` addresses CUDA IPC and needs driver 610, while NCCL communicators
and CUDA graphs holding `ncclComm_t` are unsolved upstream. Run it to confirm
the failure mode matches that prediction rather than something else.

Pass: the failure is the predicted one. A different failure is a finding.

### Rung 4: disaggregated, decode worker only

Prefill left live, decode captured and restored in place.

Pass: the restored decode worker re-registers, and a request requiring a
prefill-to-decode KV transfer completes through it, verified by worker identity.

### Rung 5: disaggregated, both workers

A genuine distributed snapshot. In-flight KV transfers now matter, and there is
no multi-pod capture primitive in nvsnap: each pod is captured independently
with no consistency guarantee across them.

Do not design this rung until rung 4 has run. Its result determines whether
coordinated capture is needed at all.

## Checks that must pass at every rung

- The restored worker appears in coordination state, not merely Running in
  Kubernetes.
- A request is served by the restored worker, by identity.
- Output is correct, not merely present.
- Rungs 4 and 5: a prefill-to-decode transfer completes end to end.
- Timings compared against rung 0 on the same hardware.
- The run is repeated. Report the rate, not the best result.

## What we will not conclude

- That a rung passes because a request succeeded. See the trap above.
- That multi-GPU is blocked only by driver version. The NCCL and CUDA-graph
  layers are unsolved independently of the driver.
- That disaggregated works because rung 4 passed. Rung 4 restores one worker
  into a live cluster; rung 5 is a different problem.
- Anything from a single run.
