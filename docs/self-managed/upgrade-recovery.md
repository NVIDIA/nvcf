# Upgrade prerequisites and recovery

Follow your version-specific upgrade guide. Versions below refer to stacks or
NVCA, not umbrella releases. Inspect queues only for failed drains or stuck
deployments.

## Task worker images

When to use: upgrading from Self-Managed 1.0.1 or earlier to 1.0.8 or later
versions, if you run tasks. Otherwise, skip this section.

Task image names and registry wiring changed on this path. Before maintenance:

1. Select the images from the target's [manifest](../overview/manifest.md).
   The [Self-Managed 1.0.10 inventory](https://github.com/dsx-ai-factory/nvcf/releases/download/deploy/stacks/self-managed/v1.0.10/nvcf-self-managed-stack-inventory.json)
   selects `nvcr.io/nvidia/nvcf/nvcf-worker-init-oss:1.2.4` and
   `nvcr.io/nvidia/nvcf/nvcf-worker-task-oss:1.1.6`. For another target, use
   that release's inventory.
1. Render the target with your registry settings. Resolve the paths below,
   then [mirror both images](../overview/image-mirroring.md#pulling-images)
   to those paths, preserving each required architecture.
1. Run the pull check below on every affected compute cluster and node pool.
   Continue only when both images succeed. Package availability is a separate
   prerequisite; an image or inventory does not provide the stack archive.

<Accordion title="Resolve the target image paths">

Use Bash, `jq` and Mike Farah's `yq` v4. Set `TASK_RENDER` to the target render's
`helm-nvcf-nvct-api/templates` directory. Its `configmap-remote-config.yaml` contains
`nvct-api-remote-config`; `configmap-env.yaml` contains `nvct-api-env` by default.
These commands use the rendered files, including any configured name overrides.

```bash
set -euo pipefail
TASK_RENDER="/absolute/path/to/target-render/helm-nvcf-nvct-api/templates"
TASK_HOST=$(yq -r '.data.NVCT_SIDECARS_HOSTNAME // ""' "$TASK_RENDER/configmap-env.yaml")
TASK_REPOSITORY=$(yq -r '.data.NVCT_SIDECARS_REPOSITORY // ""' "$TASK_RENDER/configmap-env.yaml")
TASK_IMAGES=$(yq -er '.data["nvct-api.yaml"]' "$TASK_RENDER/configmap-remote-config.yaml" |
  yq -o=json '.nvct.sidecars | [."init-container", ."utils-container"]' |
  jq -er --arg host "$TASK_HOST" --arg repo "$TASK_REPOSITORY" '
    def resolve($key; $value):
      if contains($key) then
        if $value == "" then error("Missing registry setting: " + $key)
        else split($key) | join($value) end
      else . end;
    map(if type != "string" or length == 0 then error("Missing task image") else . end |
      resolve("${nvct.sidecars.hostname}"; $host) |
      resolve("${nvct.sidecars.repository}"; $repo) |
      if contains("${") then error("Unresolved task image: " + .) else . end) | .[]')
printf '%s\n' "$TASK_IMAGES"
```

The two output lines are the init and task worker destinations. Compare their
names and tags or digests with the selected inventory before copying images.

</Accordion>

<Accordion title="Verify pulls from a compute node">

Keep `TASK_IMAGES` from the previous commands. Select an existing task namespace,
node, service account and the pull secrets intended for the target registry.
Repeat for each node pool and architecture in each compute cluster. Add the
task's tolerations to the probe spec if the node has `NoExecute` taints.

```bash
set -euo pipefail
COMPUTE_KUBECONFIG="/absolute/path/to/compute-kubeconfig"
COMPUTE_CONTEXT="compute-context"
TASK_NAMESPACE="existing-task-namespace"
TASK_NODE="existing-task-node"
TASK_SERVICE_ACCOUNT="task-service-account"
TASK_PULL_SECRETS='[{"name":"task-registry-secret"}]'
kt() {
  kubectl --kubeconfig "$COMPUTE_KUBECONFIG" --context "$COMPUTE_CONTEXT" \
    -n "$TASK_NAMESPACE" "$@"
}
while IFS= read -r image; do
  pod=$(jq -cn --arg image "$image" --arg node "$TASK_NODE" \
    --arg sa "$TASK_SERVICE_ACCOUNT" --argjson secrets "$TASK_PULL_SECRETS" '{
      apiVersion: "v1", kind: "Pod", metadata: {generateName: "task-image-check-"},
      spec: {nodeName: $node, serviceAccountName: $sa, imagePullSecrets: $secrets,
        automountServiceAccountToken: false, restartPolicy: "Never", activeDeadlineSeconds: 120,
        securityContext: {runAsNonRoot: true, seccompProfile: {type: "RuntimeDefault"}},
        containers: [{name: "check", image: $image, imagePullPolicy: "Always", args: ["--help"],
          securityContext: {allowPrivilegeEscalation: false, capabilities: {drop: ["ALL"]}}}]}}
    ' | kt create -f - -o jsonpath='{.metadata.name}')
  printf 'Checking %s with pod %s\n' "$image" "$pod"
  kt wait --for=jsonpath='{.status.containerStatuses[0].imageID}' "pod/$pod" --timeout=150s
  kt get "pod/$pod" -o jsonpath='{.spec.nodeName}{" "}{.status.containerStatuses[0].imageID}{"\n"}'
  kt delete "pod/$pod"
done <<< "$TASK_IMAGES"
```

The probe uses `--help` because these images have no shell. Record the cluster,
node and image digests. On failure, inspect `kt describe "pod/$pod"`, correct
access or image selection, and repeat. Delete that probe after collecting its
evidence. A nonempty `imageID` proves the runtime resolved the image. The container may
still be running or exit unsuccessfully; neither outcome invalidates the pull
check. This does not verify task execution.

</Accordion>

## Deployments stuck after a NATS restart

When to use: NATS will restart while any compute cluster runs NVCA 3.10.0
through 3.10.2, or deployments hang after a NATS restart.

1. Before rolling NATS, upgrade every affected compute cluster to NVCA 3.10.3
   or a later compatible patch. Preserve registration. Skip clusters already
   fixed; for other release trains, use your guide's corrected selection.
   Compute Plane 1.0.0 selects NVCA 3.10.0; 1.0.5 selects 3.10.3.
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
