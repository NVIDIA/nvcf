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

## Established, and what is still assumed

Established from source:

- Component-level `annotations` on a `DynamoGraphDeployment` reach pod metadata.
  The operator merges them in `GetDCDKubeAnnotations`
  (`deploy/operator/internal/dynamo/v1beta1_helpers.go`), whose comments state
  the destination is generated pod metadata. So `nvsnap.io/restore-from` on a
  component will reach the pod and trigger our webhook.
- The sample's prefill worker publishes KV events over ZMQ and transfers KV via
  the NIXL connector. NIXL stages transfer metadata through the pod's
  `/dev/shm`, which nvsnap already captures and replays.
- CRIU cannot dump processes using RDMA (checkpoint-restore/criu#267). If NIXL
  is on an RDMA transport, process capture of that worker is not possible.

Still assumed, and each is a gate rather than a detail:

- That a Dynamo worker rejoins cleanly after an abrupt restart. If it does, we
  can drop NIXL, ZMQ and coordination state at capture and let it re-establish,
  as we already do for external TCP. If it does not, rungs 2 and beyond need a
  different design. Answer this from the Dynamo source before designing rung 2.
- That a worker can reach ready as a standalone pod against a shared etcd and
  NATS, rather than requiring operator launch and Grove gang membership. This
  decides whether rungs 1 and 2 can use plain pods or must drive the CRD.
- That restoring in place into an operator-owned pod does not trip
  reconciliation.

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

## Instrumentation required before rung 1

None of the rungs are meaningful without a way to answer "which worker served
this request". Establish one and verify it against a healthy deployment first.
Candidates, in order of preference:

1. Worker instance identity from Dynamo's coordination state (etcd), correlated
   with the pod that served the request.
2. A response header or field naming the worker.
3. Worker logs, correlated by request id.

If none of these can distinguish workers, the whole plan is unsound and must be
reworked before any capture is attempted.

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
