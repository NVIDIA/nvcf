# OpenBao Patch Upgrade

This procedure covers OpenBao 2.6.2 to 2.6.3 with the existing NVCF JWT plugin
and a healthy three-member Raft cluster. Version 2.6.3 addresses the
[upstream security advisories](https://github.com/openbao/openbao/releases/tag/v2.6.3).
Use it only with a published stack release that includes the target server,
agent, and migrations images. It does not announce a stack release or apply
to upgrades from earlier OpenBao minor versions or a different plugin binary.

The default NVCF configuration needs no policy, JWT role, secret schema, or
agent-template changes for this patch. Review the upstream changes separately
if you customize plugin registration, authorization, PKI, or agent listeners.
The server StatefulSet uses `OnDelete`:
a successful Helm upgrade updates its template but leaves existing pods on
their previous images. Do not treat Helm success as a completed server upgrade.

This procedure replaces pods, not the StatefulSet, PVCs, or stored data. Never
delete persistent volume claims, reinitialize OpenBao, or replace multiple
members together. Troubleshooting an unhealthy OpenBao cluster and disaster
recovery are outside this guide. Stop on any failed check.

## Before You Start

Take a Raft snapshot using your approved backup procedure and confirm that
the corresponding unseal material is available securely. Retain your existing
Helmfile environment and secrets. Confirm the Kubernetes context points to
the intended control plane. Commands below use the default `vault-system`
namespace and require `kubectl`, `jq`, and Bash.

Run these checks before and after each pod replacement:

```bash
kubectl config current-context
kubectl -n vault-system get pods \
  -l app.kubernetes.io/name=openbao,component=server

check_openbao() {
  local pod pods
  pods=$(kubectl -n vault-system get pods \
    -l app.kubernetes.io/name=openbao,component=server \
    -o jsonpath='{.items[*].metadata.name}') || return 1
  if [ -z "$pods" ]; then
    echo "No OpenBao server pods found" >&2
    return 1
  fi
  for pod in $pods; do
    echo "== $pod =="
    kubectl -n vault-system exec "$pod" -c openbao -- sh -c \
      'BAO_ADDR=http://127.0.0.1:8200 bao status -format=json' || return 1
  done
}
check_openbao
```

Require all three pods to be Ready, `initialized: true`, and `sealed: false`,
with the same `cluster_id`. Record that ID. Exactly one member must report
`is_self: true`: that is the active member. A standby reports `is_self: false`
or omits the field in the older version. Do not infer leadership from the pod
ordinal. Kubernetes readiness alone is insufficient: the chart's probe can
accept a sealed server.

## Apply the Target Stack

From the target release's control-plane bundle, apply the existing environment:

```bash
make apply HELMFILE_ENV="$HELMFILE_ENV" KUBECONFIG_FILE="$KUBECONFIG"
```

Continue only after the command succeeds, migrations complete, and both
injector replicas are Ready. Do not disable hooks or bypass a failed migration.
For this patch with an unchanged JWT plugin, no extra catalog reload rotation
is required beyond replacing the server pods below.

Inspect the desired images and update strategy:

```bash
kubectl -n vault-system get statefulset openbao-server \
  -o jsonpath='{range .spec.template.spec.containers[*]}{.name}{": "}{.image}{"\n"}{end}{"updateStrategy: "}{.spec.updateStrategy.type}{"\n"}'
```

Compare the images with the target release manifest. The OpenBao binary version
is `2.6.3`; the NVCF image tag also contains its own release version. Neither
is the stack version. If the desired images are wrong or the strategy is not
`OnDelete`, stop and review the release values before deleting any pod.

## Replace Standbys One at a Time

Run `check_openbao` again. Select one standby still using the old image:

```bash
POD=<standby-pod-name>
kubectl -n vault-system delete pod "$POD" --wait=true --timeout=3m
kubectl -n vault-system wait "pod/$POD" --for=condition=Ready --timeout=5m
check_openbao
```

If the replacement pod has not appeared yet, wait for it before repeating
`kubectl wait`. Do not delete another member. If it is Ready but still sealed,
allow auto-unseal to finish and repeat the status check.

Require the replacement to report `version: "2.6.3"`, `sealed: false`, and the
original `cluster_id`. Verify its container images match the StatefulSet's
desired images. Confirm all three members are healthy before replacing the
next standby. Do not proceed if a member fails to recover within five minutes.

## Transfer Leadership and Replace the Former Active

After both standbys are healthy on 2.6.3, rerun `check_openbao` and identify
the active member again. If it is still on the old image, explicitly transfer
leadership before replacing it. The following uses the chart-managed root-token
Secret; use an appropriately authorized operator token instead if your
installation manages that credential differently. Do not enable shell tracing
or print the token.

```bash
ACTIVE_POD=<current-active-pod-name>
set -o pipefail
kubectl -n vault-system get secret openbao-server-root-token -o json |
  jq -er '.data.root_token | @base64d' |
  kubectl -n vault-system exec -i "$ACTIVE_POD" -c openbao -- sh -ec '
    IFS= read -r BAO_TOKEN
    test -n "$BAO_TOKEN"
    export BAO_TOKEN
    export BAO_ADDR=http://127.0.0.1:8200
    bao operator step-down
  '
check_openbao
```

Wait until a 2.6.3 member reports `is_self: true` and the former active reports
standby status. A successful step-down request alone does not confirm transfer.
If leadership has not transferred within one minute, stop. Delete the former
active using the same one-pod procedure and recovery checks as the standbys.
If leadership changed earlier, apply this rule to whichever old-image member
is currently active; never delete a member you have not verified is a standby.

## Verify Servers and Agents

Run `check_openbao` and confirm all three members are unsealed on 2.6.3, with
one active member and the original cluster ID. Compare every container image
with the desired StatefulSet template:

```bash
kubectl -n vault-system get pods \
  -l app.kubernetes.io/name=openbao,component=server \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{range .spec.containers[*]}{"  "}{.name}{": "}{.image}{"\n"}{end}{end}'
```

Updating the injector affects new pods only. Roll out existing injected
workloads through their normal deployment procedure, one service at a time,
and verify each replacement agent's image, authentication, rendered secrets,
and application readiness. A single-replica service can be interrupted during
this step. Never restart all consumers together just to update their agents.

Invoke an existing function with its existing API key, then create and delete
a separate test function with a secret to check API-to-ESS access. Repeat the
normal stack apply to confirm it is idempotent. An unchanged image does not
require another server rotation.

Do not use a Helm rollback as an OpenBao data-downgrade procedure. Downgrade
and snapshot restore require a separately validated recovery plan.
