# Upgrade prerequisites and recovery

Follow your version-specific upgrade guide. Versions below refer to stacks or
NVCA, not umbrella releases. Inspect queues only for failed drains or stuck
deployments.

## Task worker images

When to use: upgrading from Self-Managed 1.0.1 or earlier to 1.0.8 or later
versions, if you run tasks. Otherwise, skip this section.

Task image names and registry wiring changed on this path. Use the target
stack combination selected by your upgrade guide.

1. Before maintenance, render the target and resolve the Tasks ConfigMap's
   `nvct.sidecars` image references, including `NVCT_SIDECARS_HOSTNAME` and
   `NVCT_SIDECARS_REPOSITORY`.
1. Use the target's released inventory and the
   [image mirroring guide](../overview/image-mirroring.md) to mirror the exact
   init and worker images, including required architectures, to those paths.
   Verify pulls on every affected compute cluster and record each result.

## Deployments stuck after a NATS restart

When to use: NATS will restart while any compute cluster runs NVCA 3.10.0
through 3.10.2, or deployments hang after a NATS restart.

1. Before rolling NATS, upgrade every affected compute cluster to NVCA 3.10.3
   or a later compatible patch. Preserve registration. Skip clusters already
   fixed; for other release trains, use your guide's corrected selection.
   Compute Plane 1.0.0 selects NVCA 3.10.0; 1.0.4 selects 3.10.3.
1. Allow the consumer's `ack_wait` interval for redelivery after the agent
   upgrade. Restarting the old agent alone does not fix reused message IDs.
1. Repeat the guide's complete drain gate. If creation messages remain or
   deployments stay stuck, inspect them below. Keep pending work intact.

### Inspect retained creation messages

When to use: creation messages or stuck deployments remain after the drain
wait. Keep deployments, scaling, invocations, and tasks paused at their callers.

1. Hold the NATS rollout. Select the control-plane context and the NATS context
   owning the stream (`default` unless customized). Use the chart's `nats-box`
   pod and a new private evidence directory. Run these steps in one shell.

   ```bash
   set -euo pipefail
   CONTROL_KUBECONFIG="/absolute/path/to/control-plane-kubeconfig"
   CONTROL_CONTEXT="control-plane-context"
   NVCF_CLI="/absolute/path/to/nvcf-cli"
   NVCF_CLI_CONFIG="/absolute/path/to/account-config.yaml"
   NATS_CONTEXT="${NATS_CONTEXT:-default}"
   RECOVERY_RECORD="/absolute/path/to/new-recovery-record"
   umask 077
   mkdir "$RECOVERY_RECORD"
   kcp() {
     kubectl --kubeconfig "$CONTROL_KUBECONFIG" \
       --context "$CONTROL_CONTEXT" "$@"
   }
   NATS_BOX_POD=$(kcp -n nats-system get pod \
     -l app.kubernetes.io/component=nats-box -o json | jq -er '
       [.items[] | select(.metadata.deletionTimestamp == null) |
        select(any(.status.conditions[]?; .type == "Ready" and .status == "True")) |
        .metadata.name][0] // empty')
   nats_read() {
     kcp -n nats-system exec "$NATS_BOX_POD" -- nats --context "$NATS_CONTEXT" "$@"
   }
   STREAM=CreateNvcaFunctionTaskStream
   nats_read stream info "$STREAM" --json > "$RECOVERY_RECORD/stream-before.json"
   nats_read consumer list "$STREAM"
   ```

2. Check `state.messages` in `stream-before.json`. If zero, return to the
   complete drain gate; a stuck deployment needs another diagnosis. Otherwise,
   record each consumer's `ack_wait`, pending, and acknowledgement-pending counts
   with `nats_read consumer info "$STREAM" <consumer> --json`.
3. Inspect retained sequences, starting at `state.first_seq`. Gaps can represent
   deleted or acknowledged messages. Keep message contents private.

   ```bash
   SEQ="<retained-message-sequence>"
   nats_read stream get "$STREAM" "$SEQ" --json \
     > "$RECOVERY_RECORD/message-$SEQ.json"
   jq -er '.data | @base64d | fromjson |
     {requestId, ncaId, action, deploymentId, functionDetails, taskDetails}' \
     "$RECOVERY_RECORD/message-$SEQ.json"
   ```

4. Capture requests from every affected compute cluster. Set a unique cluster
   label for the filename; each capture gets a new file, including repeat runs.

   ```bash
   COMPUTE_CLUSTER="compute-cluster-name"
   COMPUTE_KUBECONFIG="/absolute/path/to/compute-kubeconfig"
   COMPUTE_CONTEXT="compute-context"
   COMPUTE_RECORD=$(mktemp "$RECOVERY_RECORD/compute-$COMPUTE_CLUSTER.XXXXXX")
   kubectl --kubeconfig "$COMPUTE_KUBECONFIG" --context "$COMPUTE_CONTEXT" \
     get icmsrequests.nvca.nvcf.nvidia.io -A -o json \
     > "$COMPUTE_RECORD"
   jq '.items[] | {name: .metadata.name, namespace: .metadata.namespace,
     messageId: .metadata.labels.SQSMessageID, requestId: .spec.requestId,
     ncaId: .spec.ncaId, function: .spec.functionDetails,
     task: .spec.taskDetails, status: .status}' \
     "$COMPUTE_RECORD"
   ```

5. Match `requestId`, `ncaId`, deployment and function/version or task IDs
   against `ICMSRequest.spec` and the owning account's API. Do not rely on
   `SQSMessageID`, which can collide. For a function:

   ```bash
   "$NVCF_CLI" --config "$NVCF_CLI_CONFIG" function deploy get \
     --function-id "<message-function-id>" \
     --version-id "<message-function-version-id>" --json
   ```

   For a task, use `task get <message-task-id> --json` with the same CLI and
   account configuration. A failed read or unverified 404 does not prove the
   request is obsolete.

### If messages remain

When to use: corrected agents have had time to redeliver work, but the drain
gate still fails, or any consuming cluster cannot be checked.

1. Stop the upgrade, keep callers paused, and share the private evidence with
   NVIDIA support. Do not purge the stream, delete messages by sequence, or
   remove active `ICMSRequest` objects. A restarted stream can reuse a sequence;
   comparing a message before deletion cannot prevent that race.
1. Resume the NATS rollout only after the complete drain gate passes on every
   server and affected queue. After upgrading, verify the original function
   still invokes and a new deployment succeeds.
