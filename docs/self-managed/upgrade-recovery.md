# Upgrade prerequisites and recovery

Check these release-specific issues before starting an upgrade from an older
self-managed installation. Keep the selected stack versions, CLI version,
registry paths, and any overrides in the upgrade record. A source fix, a
published artifact, and a stack that consumes it are separate requirements.

## Profile export CLI

`nvcf-cli` 1.16.2 requests the OpenBao LLM root certificate even when the LLM
add-on is disabled. On a default installation, `control-plane profile export`
can fail with HTTP 403 and leave no profile for compute-plane registration.
The fix is in CLI 1.16.5. It skips the lookup when LLM PKI is disabled and
retains certificate validation when it is enabled.

Use a compatible CLI release containing that fix. Check the binary with
`nvcf-cli version`; a source tag does not establish that NGC or GitHub has a
downloadable binary. Publication is tracked in
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

Keep the source commit and build output with the upgrade record. Use the
explicit `--config` path and control-plane kubeconfig in the upgrade guide's
export command. Otherwise a default CLI configuration can select endpoints
from another installation. Export and validate the profile before the
maintenance window and repeat after the target control plane is healthy.
Do not reuse a profile left by a failed export.

## Task worker images

The historical Self-Managed 1.0.1 stack selects Tasks chart 1.6.0. Its remote
configuration uses `nvcf_worker_init:2.107.0` and `nvcf_worker_task:2.107.0`.
These references are absent from that stack's released inventory and cannot
be assumed available in the customer catalog. A task passing against a mirror
that contains them does not qualify the public installation path.

Self-Managed 1.0.8 supplies explicit overrides for
`nvcf-worker-init-oss:1.2.4` and `nvcf-worker-task-oss:1.1.6`. Its released
inventory includes both images. Later chart lines own these defaults and
expose their resolved references to inventory generation.

Use the corrected stack combination selected by the applicable upgrade guide.
Do not copy newer worker pins into an older stack without checking NVCA
compatibility. In particular, worker security-context changes and the agent
that creates those containers must be qualified together.

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

NVCA 3.10.0 deduplicates queue messages using their transport message ID.
A restarted NATS memory stream can reuse a sequence number, so a new request
can match an older `ICMSRequest` and remain unacknowledged. Restarting the
agent does not remove that persisted request. NVCA 3.10.3 distinguishes
publisher request IDs as well; Compute Plane 1.0.4 selects that version.

Upgrade every affected compute cluster to the compatible corrected agent
before rolling NATS. Preserve its cluster registration. The identity fix does
not make a memory stream durable and does not prove that messages already
stuck for removed deployments have drained.

An in-flight message can remain pending until the consumer's acknowledgement
timeout expires and NATS redelivers it. Check `ack_wait` in the consumer
configuration and allow that interval before judging recovery. Do not remove
a pending request for a deployment that still needs to start.

### Inspect retained creation messages

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

First allow the corrected agent to process valid work. For a message that
remains, retain evidence for all of these conditions:

- It is a creation request for the exact deployment or task being inspected.
- The owning account's API and the recorded deletion or terminal transition
  confirm that this request is no longer desired. A timeout, missing pod,
  empty CLI output, authentication failure, or unverified 404 is insufficient.
- No current deployment, task, or in-progress `ICMSRequest` still needs it.
  Check all compute clusters that can consume the message, including offline
  clusters. If any cluster cannot be checked, stop.
- New submissions and deployment mutations remain paused. No NATS restart or
  stream recreation occurred during this investigation.

If any condition is uncertain, retain the message and stop the upgrade.
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
