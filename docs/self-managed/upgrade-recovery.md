# Upgrade prerequisites and recovery

Use the conditions below to select steps for your version-specific upgrade
guide. Versions refer to individual stacks or NVCA, not umbrella releases.
Manual queue recovery is not a routine upgrade step.

## Task worker images

When to use: every upgrade if you run tasks. Otherwise, skip this section.

Task init and worker image names and registry wiring changed between
Self-Managed 1.0.1 and 1.0.8. Mirror the target version's images to the locations
selected by your site configuration, then verify them:

1. Before maintenance, render the target and resolve the Tasks ConfigMap's
   `nvct.sidecars` image references, including `NVCT_SIDECARS_HOSTNAME` and
   `NVCT_SIDECARS_REPOSITORY`.
1. Use the target's released inventory and the
   [image mirroring guide](../overview/image-mirroring.md) to mirror the exact
   init and worker images, including required architectures, to those paths.
   Verify the compute cluster can pull them.
1. In the upgrade rehearsal, run a new container task. Confirm it uses the
   expected init and worker images, completes successfully, and returns results.

If the target still selects `nvcf_worker_init:2.107.0` or
`nvcf_worker_task:2.107.0`, stop before maintenance and use a corrected stack
combination from your upgrade guide.

## Deployments stuck after a NATS restart

When to use: NATS will restart with an agent that lacks the request-identity
fix, or deployments hang after a NATS restart. NVCA 3.10.0 through 3.10.2 is
affected regardless of the control-plane version.

On the NVCA 3.10 line, upgrade to 3.10.3 or a later compatible patch before
rolling NATS. Compute Plane 1.0.0 selects NVCA 3.10.0; Compute Plane 1.0.4
selects the fixed 3.10.3. Preserve cluster registration. If the agent already
contains the fix, skip this correction but still follow the guide's drain
gate. Other release trains must use their guide's corrected NVCA selection.

The affected agents deduplicate by transport message ID. A restarted memory
stream can reuse that ID and collide with an older `ICMSRequest`. The fix also
checks publisher request IDs; it does not make memory streams durable or
remove obsolete work. Restarting the agent alone does not fix the collision.

An in-flight message can remain pending until the consumer's acknowledgement
timeout expires and NATS redelivers it. Check `ack_wait` in the consumer
configuration and allow that interval before judging recovery. Do not remove
a pending request for a deployment that still needs to start.

### Inspect retained creation messages

When to use: creation messages remain after the normal drain wait, or new
deployments remain stuck after a NATS restart. This is diagnosis, not a step
for every upgrade.

Keep new deployments, scaling changes, invocations, and task submissions
paused at their callers. Use the control-plane kubeconfig and context, plus
the `nats-box` pod installed by the NATS chart. The server container need not
contain the NATS CLI. Set a new private evidence directory:

```bash
set -euo pipefail
CONTROL_KUBECONFIG="/absolute/path/to/control-plane-kubeconfig"
CONTROL_CONTEXT="control-plane-context"
RECOVERY_RECORD="/absolute/path/to/new-recovery-record"
umask 077
mkdir "$RECOVERY_RECORD"
kcp() {
  kubectl --kubeconfig "$CONTROL_KUBECONFIG" \
    --context "$CONTROL_CONTEXT" "$@"
}
NATS_BOX_POD=$(kcp -n nats-system get pod \
  -l app.kubernetes.io/component=nats-box \
  -o jsonpath='{.items[0].metadata.name}')
test -n "$NATS_BOX_POD"
nats_read() {
  kcp -n nats-system exec "$NATS_BOX_POD" -- nats --context default "$@"
}
STREAM=CreateNvcaFunctionTaskStream
nats_read stream info "$STREAM" --json > "$RECOVERY_RECORD/stream-before.json"
nats_read consumer list "$STREAM"
```

If the stream has zero messages, skip the remaining inspection and removal
steps. Continue the guide's complete drain gate; a deployment still stuck
with an empty creation stream needs a different diagnosis.

Use the NATS context that owns the creation stream if the installation does
not use `default`. Record each consumer's pending and acknowledgement-pending
counts with `nats_read consumer info "$STREAM" <consumer> --json`. A count
alone does not identify an obsolete message.

Inspect a retained message by its stream sequence. Start with the `first_seq`
in the stream state and inspect later retained sequences individually; gaps
can be deleted or acknowledged messages. Do not guess a sequence from another
installation:

```bash
SEQ="<retained-message-sequence>"
nats_read stream get "$STREAM" "$SEQ" --json \
  > "$RECOVERY_RECORD/message-$SEQ.json"
jq -er '.data | @base64d | fromjson |
  {requestId, ncaId, action, deploymentId, functionDetails, taskDetails}' \
  "$RECOVERY_RECORD/message-$SEQ.json"
```

Keep the full message private: its launch specification can contain workload
configuration. Correlate `requestId`, `ncaId`, and `deploymentId` with the
function/version or task ID in the payload and with `ICMSRequest.spec` on
every affected compute cluster. Do not infer identity from `SQSMessageID`
alone. That label is the value that can collide.

For each compute cluster, save the requests using its explicit context. Use a
distinct output filename for each cluster:

```bash
COMPUTE_KUBECONFIG="/absolute/path/to/compute-kubeconfig"
COMPUTE_CONTEXT="compute-context"
kubectl --kubeconfig "$COMPUTE_KUBECONFIG" --context "$COMPUTE_CONTEXT" \
  get icmsrequests.nvca.nvcf.nvidia.io -A -o json \
  > "$RECOVERY_RECORD/compute-requests.json"
jq '.items[] | {name: .metadata.name, namespace: .metadata.namespace,
  messageId: .metadata.labels.SQSMessageID, requestId: .spec.requestId,
  ncaId: .spec.ncaId, function: .spec.functionDetails,
  task: .spec.taskDetails, status: .status}' \
  "$RECOVERY_RECORD/compute-requests.json"
```

Use a CLI configuration for the owning account when checking the workload.
For a function message, use its `functionId` and `functionVersionId`:

```bash
"$NVCF_CLI" --config "$NVCF_CLI_CONFIG" function deploy get \
  --function-id "<message-function-id>" \
  --version-id "<message-function-version-id>" --json
```

For a task message, use the same CLI and configuration with
`task get <message-task-id> --json`. Compare
the returned deployment or task identity with the message and the recorded
deletion or terminal transition. Treat a failed read as an investigation
result, not as permission to remove the message.

### Remove only a confirmed obsolete message

When to use: inspection confirms an obsolete creation request remains after
the corrected agent has had time to process redelivered work. Otherwise skip
removal. Retain evidence for all of these conditions:

- It is a creation request for the exact deployment or task being inspected.
- The owning account's API and the recorded deletion or terminal transition
  confirm that this request is no longer desired. A timeout, missing pod,
  empty CLI output, authentication failure, or unverified 404 is insufficient.
- No current deployment, task, or in-progress `ICMSRequest` still needs it.
  Check all compute clusters that can consume the message, including offline
  clusters. If any cluster cannot be checked, stop.
- New submissions and deployment mutations remain paused. No NATS restart or
  stream recreation occurred during this investigation.

If any condition is uncertain, retain the message and stop at the drain gate.
Do not remove live work to satisfy a drain gate. Do not delete the stream,
purge it, or remove an older active `ICMSRequest` whose label collided.

After the operator has reviewed the evidence for this one message, re-read it
and require identical content before using the interactive removal command:

```bash
nats_read stream get "$STREAM" "$SEQ" --json \
  > "$RECOVERY_RECORD/message-$SEQ-current.json"
if ! cmp -s "$RECOVERY_RECORD/message-$SEQ.json" \
    "$RECOVERY_RECORD/message-$SEQ-current.json"; then
  printf 'Message changed; stop and investigate again.\n' >&2
  exit 1
fi
kcp -n nats-system exec -it "$NATS_BOX_POD" -- \
  nats --context default stream rmm "$STREAM" "$SEQ"
nats_read stream info "$STREAM" --json > "$RECOVERY_RECORD/stream-after.json"
```

Record the removed sequence, request identity, reason, and operator approval.
Repeat the inspection for each remaining message. Then repeat the upgrade
guide's complete drain gate across every server and affected queue. Only
after it passes may the NATS rollout continue. Verify the original function
still invokes and a new deployment succeeds after the upgrade.
