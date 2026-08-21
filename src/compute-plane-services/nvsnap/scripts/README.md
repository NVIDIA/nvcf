<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# nvsnap test suite

Two entry points:

| Script | Answers | Writes |
|---|---|---|
| `test-e2e.sh <workload>` | does capture/restore work for this workload | pass/fail + step timings to stdout |
| `test-bench.sh <workload>` | how long does cold vs warm vs restore take | a row in `docs/PDF-BENCH-RESULTS.md` |

Use `test-e2e.sh` to check correctness, `test-bench.sh` to produce numbers.

## Before you run anything

```sh
export KUBECONFIG=<your cluster kubeconfig>
kubectl get nodes                     # must work; expired credentials are the #1 cause of confusing failures
./scripts/test-e2e.sh                 # no args: prints the workload list
```

The suite refuses to start unless the deployed agent matches
`scripts/versions.sh`. That is deliberate: a run against a different build
produces numbers attributed to the wrong version. To test a build that is
already deployed, override rather than editing the file:

```sh
NVSNAP_APP_VERSION=v0.2.46 ./scripts/test-e2e.sh vllm-small
```

## Running

```sh
./scripts/test-e2e.sh vllm-small      # single GPU, CRIU path, ~5 min
./scripts/test-e2e.sh vllm-70b        # 4 GPUs, cachedir path, ~30 min
./scripts/test-bench.sh gpt-oss-120b  # benchmark row instead of pass/fail
```

Both leave the source and restored pods in place on failure so you can inspect
them. On success they clean up.

## Capture paths

Which path runs is decided by GPU count, not by the manifest:

- 1 GPU -> `criu-v2`: CRIU + cuda-checkpoint of the live process, GPU state
  included.
- 2+ GPUs -> `cachedir`: capture the pod's cache mount (model weights,
  compiled kernels). No process state. Multi-GPU CRIU does not work.

Override with `CAPTURE_PATH=criu-v2` or `CAPTURE_PATH=rootfs` when you need the
other one.

The `nvsnap.io/path` annotation in a workload manifest documents the intent; it
does not select the path. The agent's `--pod-cache-dir` flag is what decides
whether a `rootfs`-family capture is really cachedir. If those two disagree,
believe the flag.

## Guards, and why a run may refuse to start

These exist because each one has silently produced a wrong result before.
If a guard fires, it is telling you the run would have measured something other
than what you asked for.

| Guard | Refuses when | Why |
|---|---|---|
| agent version | deployed image != `versions.sh` | numbers would be attributed to the wrong build |
| image exists | tag missing from the registry | catches a failed push before a 30 min run |
| placeholder | any `__NAME__` token survived substitution, not just `__CAPTURE_HASH__` | the webhook ignores the pod and it cold-starts |
| restore admitted | cache dir not mounted, or cache env points outside it | the pod cold-starts while looking like a restore |

The placeholder guard rejects any unresolved `__[A-Z_]+__` token, not only
`__CAPTURE_HASH__`, because a manifest that still carries `__NODE_NAME__` or
`__CHECKPOINT_ID__` is just as unusable. Tokens named inside comments are
ignored deliberately: templates explain their own placeholders, and a guard
that fails a correctly substituted manifest is worse than the problem it
was added for.

The restore-admitted guard is the one worth understanding. A pod the webhook
declined still starts, still serves, and passes every functional check -- it
just fetches its model again. Without the guard the run reports a restore time
that is really a cold start, and in `test-bench.sh` that number is published.

Shared implementation: `scripts/lib/restore-guard.sh`, sourced by both scripts
so the contract cannot drift between them.

## When a run fails

```sh
kubectl get pods -n nvsnap-system                       # both pods are left behind
kubectl logs <pod> -n nvsnap-system --tail=100
kubectl logs -n nvsnap-system -l app=nvsnap-agent -c agent --since=30m | grep -i capture
```

Capture and restore logs land next to the checkpoint on the node:

```sh
# Read the cache dir from the deployed agent rather than assuming it, and use
# the node the workload actually ran on -- a different agent pod sees a
# different disk.
. scripts/lib/restore-guard.sh
CACHE=$(agent_pod_cache_dir)
NODE=$(kubectl get pod <workload-pod> -n nvsnap-system -o jsonpath='{.spec.nodeName}')
AGENT=$(kubectl get pods -n nvsnap-system -l app=nvsnap-agent -o wide \
    | awk -v n="$NODE" '$7==n {print $1}' | head -1)
kubectl exec -n nvsnap-system "$AGENT" -c agent -- \
    sh -c "ls -1dt $CACHE/*/ | head -3"
```

Copy anything you need out before re-running: a second run may reuse or replace
the capture, and the evidence goes with it.

## Re-capturing

Captures are content-addressed. A second run with the same pod identity reuses
the existing capture rather than making a new one -- normally what you want, and
confusing when you are trying to test the capture path itself.

To force a fresh capture, remove what claims the hash:

```sh
kubectl delete cm -n nvsnap-system nvsnap-capture-<hash-prefix>   # manifest tier
kubectl delete pvc -n nvsnap-system rox-<hash>                    # L2 tier
```

Deleting only one tier is not enough: the agent skips the capture if any tier
claims the hash. A schema change bumps `CaptureFormatVersion`, which changes the
hash and re-captures everything automatically.

## Adding a workload

1. `deploy/k8s/workloads/<id>.yaml` plus `<id>-restore.yaml`.
2. Restore manifest carries `nvsnap.io/restore-from: "__CAPTURE_HASH__"`; the
   scripts substitute it.
3. Add a `case` arm in both scripts with the pod names, port, model, and
   inference payloads.
4. Annotate `nvsnap.io/gpus` accurately -- it selects the capture path.
