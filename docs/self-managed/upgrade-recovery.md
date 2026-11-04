# Upgrade prerequisites and recovery

Use the conditions below to select steps for your version-specific upgrade
guide. Versions refer to individual stacks, NVCA, or the CLI, not umbrella
releases. Manual queue recovery is not a routine upgrade step.

## Profile export CLI

When to use: your upgrade guide requires `control-plane profile export`,
including [Self-Managed 0.6.2 to 1.0.1](../overview/release-notes/0.6.1-to-1.0.1-upgrade.md).
Skip this section if your path does not export a profile.

CLI 1.16.2 can fail export with HTTP 403 when LLM is disabled because it still
queries OpenBao LLM PKI. CLI 1.16.5 fixes that lookup. Use a compatible CLI
containing the fix; check the installed binary with `nvcf-cli version`.
If you already have one, skip the build below. Binary availability is tracked in
[the CLI distribution issue](https://github.com/dsx-ai-factory/nvcf/issues/2785).
Do not change OpenBao permissions or enable the LLM add-on to bypass this error.

If a corrected binary is unavailable, build the 1.16.8 source tag, which also
contains the OpenBao response retry fixes. This requires Git and the Go
toolchain declared by that tag. Choose a new source directory and an absolute
output directory:

```bash
set -euo pipefail
CLI_SOURCE="/absolute/path/to/new-nvcf-cli-source"
CLI_BIN_DIR="/absolute/path/to/upgrade-tools"
git clone --branch src/clis/nvcf-cli/v1.16.8 --single-branch \
  https://github.com/dsx-ai-factory/nvcf.git "$CLI_SOURCE"
CLI_COMMIT=$(git -C "$CLI_SOURCE" rev-parse HEAD)
mkdir -p "$CLI_BIN_DIR"
go build -C "$CLI_SOURCE/src/clis/nvcf-cli" \
  -ldflags "-X main.Version=1.16.8 -X main.GitCommit=$CLI_COMMIT" \
  -o "$CLI_BIN_DIR/nvcf-cli" .
export NVCF_CLI="$CLI_BIN_DIR/nvcf-cli"
"$NVCF_CLI" version
```

Keep the source commit and build output with the upgrade record. Verify the
target export in rehearsal before scheduling maintenance. During the upgrade,
export from the healthy installed control plane at the guide's compute-plane
handoff. Use the explicit `--config` path and control-plane kubeconfig, then
validate the fresh profile. Do not reuse a profile left by a failed export.

## Task worker images

When to use: every upgrade of an installation that runs tasks. Skip this
section if tasks are not used. Check the target images even if the source
already runs tasks successfully.

The affected defaults in Self-Managed 1.0.1 (Tasks chart 1.6.0) select
`nvcf_worker_init:2.107.0` and `nvcf_worker_task:2.107.0`.
These references are absent from that stack's released inventory and cannot
be assumed available in the customer catalog.

Self-Managed 1.0.8 supplies explicit overrides for
`nvcf-worker-init-oss:1.2.4` and `nvcf-worker-task-oss:1.1.6`. Its released
inventory includes both images. For the Self-Managed 1.0.1 to 1.0.8 upgrade,
the matching compute upgrade is Compute Plane 1.0.0 to 1.0.4. A target that renders
the corrected, published images needs verification, not a manual pin change.

For other paths, use the qualified stack combination in that upgrade guide;
1.0.8 is not a required intermediate stop. Do not copy these worker pins into
another stack without checking NVCA compatibility.

Before the maintenance window:

1. Render the target with its site configuration and inspect the Tasks remote
   ConfigMap's `nvct.sidecars` entries, including placeholder expansion through
   `NVCT_SIDECARS_HOSTNAME` and `NVCT_SIDECARS_REPOSITORY`.
1. Compare every resolved worker image with the target's released inventory.
   Preserve any customer mirror overrides.
1. Verify each exact image and required architecture is accessible from the
   registry the compute cluster uses. For example:

   ```bash
   docker buildx imagetools inspect \
     nvcr.io/nvidia/nvcf/nvcf-worker-task-oss:1.1.6
   ```

1. In the rehearsal, create a new container task and check its actual init and
   worker images, completion status, and results. An existing function does
   not exercise task worker defaults.

If the target still renders the legacy names, stop before maintenance and
select a qualified correction. Do not rewrite a historical inventory to imply
that it shipped different images.

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
