# Control Plane Operations

This section provides runbooks for operating the self-hosted NVCF control plane, including encryption key rotation, service management, and upgrades.

## Encryption Key Management

Self-hosted NVCF uses a two-tier encryption hierarchy to protect secrets stored in the Encrypted Secret Store (ESS):

| Key | Purpose |
| --- | --- |
| MEK (Master Encryption Key) | A single AES-256-GCM key stored in OpenBAO. The MEK encrypts (wraps) all NEKs. It is shared across NVCF services in the control plane. |
| NEK (Namespace Encryption Key) | Per-namespace AES-256-GCM keys stored in Cassandra (encrypted by the MEK). NEKs directly encrypt user secrets such as API keys and registry credentials. |

When a user stores a secret through the NVCF API, ESS encrypts it with the active NEK for that namespace. The NEK itself is stored in Cassandra, encrypted by the MEK. To decrypt a secret, ESS retrieves the NEK from Cassandra, decrypts it using the MEK from OpenBAO, then decrypts the secret.

### Key Rotation Runbooks

- [control-plane-runbook-mek-rotation](./runbooks/control-plane-key-rotation-mek.md). Rotate the master encryption key stored in OpenBAO.

## Basic Operations

### Service Reference

The following table lists all NVCF control plane services with their namespace, resource name,
and resource type. Use these values in the commands throughout this section.

| Namespace | Service | Resource Name | Type |
| --- | --- | --- | --- |
| `nvcf` | NVCF API | `nvcf-api` | Deployment |
| `nvcf` | Invocation Service | `invocation-service` | Deployment |
| `nvcf` | gRPC Proxy | `grpc-proxy-deployment` | Deployment |
| `nvcf` | Notary Service | `notary-service` | Deployment |
| `sis` | Spot Instance Service | `spot-instance-service` | Deployment |
| `api-keys` | API Keys Service | `api-keys` | Deployment |
| `api-keys` | Admin Issuer Proxy | `admin-token-issuer-proxy` | Deployment |
| `ess` | ESS API | `ess-api-helm-nvcf-ess-api-deployment` | Deployment |
| `nats-system` | NATS | `nats` | StatefulSet |
| `vault-system` | OpenBao | `openbao-server` | StatefulSet |
| `cassandra-system` | Cassandra | `cassandra` | StatefulSet |
| `nvca-operator` | NVCA Operator | `nvca-operator` | Deployment |
| `envoy-gateway-system` | Envoy Gateway | `envoy-gateway` | Deployment |

### Restarting a Service

Restarting a Deployment:

```bash
kubectl rollout restart deployment/<name> -n <namespace>

# Example: restart the NVCF API
kubectl rollout restart deployment/nvcf-api -n nvcf

# Verify the rollout completes
kubectl rollout status deployment/<name> -n <namespace> --timeout=120s
```

Restarting a StatefulSet:

StatefulSets using `RollingUpdate` restart one pod at a time in reverse ordinal
order. Check the update strategy before using the commands below. OpenBao uses
`OnDelete`, so `kubectl rollout restart` alone does not replace its pods. For
OpenBao 2.6.2 to 2.6.3, use the
[OpenBao patch upgrade runbook](./runbooks/openbao-patch-upgrade.md).

```bash
kubectl rollout restart statefulset/<name> -n <namespace>

# Example: restart Cassandra
kubectl rollout restart statefulset/cassandra -n cassandra-system

# Verify the rollout completes
kubectl rollout status statefulset/<name> -n <namespace> --timeout=300s
```

<Note>
For OpenBao, verify every member's seal state after each replacement, not just
Kubernetes readiness. For example:

```bash
kubectl exec -n vault-system openbao-server-0 -c openbao -- sh -c \
  'BAO_ADDR=http://127.0.0.1:8200 bao status'
```

</Note>

Restarting all Deployments in a namespace:

```bash
kubectl rollout restart deployment -n <namespace>

# Example: restart all services in the nvcf namespace
kubectl rollout restart deployment -n nvcf
```

### Checking Service Health

List pods and their status:

```bash
# All pods in a namespace
kubectl get pods -n <namespace>

# All NVCF control plane pods at a glance
for ns in nvcf sis api-keys ess nats-system vault-system cassandra-system; do
  echo "=== $ns ==="
  kubectl get pods -n $ns
done
```

Check logs for a service:

```bash
# Recent logs
kubectl logs -n <namespace> -l app.kubernetes.io/name=<name> --tail=50

# Follow logs in real time
kubectl logs -n <namespace> -l app.kubernetes.io/name=<name> -f
```

Describe a pod for events and conditions:

```bash
kubectl describe pod -n <namespace> -l app.kubernetes.io/name=<name>
```

### Scaling a Service

To temporarily take a service offline (for example, during maintenance), scale it to zero,
perform the work, then scale it back:

```bash
# Scale down
kubectl scale deployment/<name> -n <namespace> --replicas=0

# ... perform maintenance ...

# Scale back up
kubectl scale deployment/<name> -n <namespace> --replicas=1

# Verify
kubectl rollout status deployment/<name> -n <namespace>
```

<Warning>
Scaling infrastructure StatefulSets (Cassandra, NATS, OpenBao) to zero will cause a full
outage. Only do this if you understand the implications for data availability and quorum.

</Warning>

## Upgrading Services

For a stack upgrade, follow the [staged 1.0.2-to-1.0.4 procedure](../overview/release-notes/1.0.2-to-1.0.4-upgrade.md).
It covers the docs edition 1.0.2 combination and the required hostname-policy,
NVCA and control-plane ordering. Other source versions must complete their
intermediate migration steps first.

The commands below are for targeted fixes to individual services. A spot
upgrade bypasses the stack's coordinated versions and migrations. Review the
specific chart's compatibility and rollback limits before using this path.

### When to Spot Upgrade

| Use a spot upgrade when | Use a full Helmfile upgrade when |
| --- | --- |
| Applying a patch release to a single service (e.g., `1.2.6` to `1.2.7`) | Upgrading the entire stack to a new minor or major version |
| Applying a targeted hotfix provided by NVIDIA support | The new version includes Cassandra schema migrations |
| You need to roll out a configuration change that requires a new chart version | Multiple services need to be upgraded together for compatibility |

### Pre-Upgrade Checklist

Before upgrading any chart:

1. Note the current chart version and app version:

   ```bash
   helm list -n <namespace>
   ```

2. Back up the current Helm values:

   ```bash
   helm get values <release> -n <namespace> -o yaml > <release>-values-backup.yaml
   ```

3. Review release notes for the target version. Check for breaking changes, required
   value changes, or new dependencies.

4. Verify the cluster is healthy before starting. all pods running, no pending operations.

### Spot Upgrading a Helm Chart

The following commands work for any Deployment-based service. Replace the placeholders with
values from the [Service Reference] table above.

```bash
# 1. Upgrade the chart
helm upgrade <release> \
  oci://<registry>/<repository>/<chart> \
  --version <new-version> \
  --namespace <namespace> \
  --wait --timeout 5m \
  -f <release>-values.yaml

# 2. Verify the rollout
kubectl rollout status deployment/<name> -n <namespace> --timeout=120s

# 3. Confirm the new chart version
helm list -n <namespace>
```

Example. upgrading the NVCF API chart:

```bash
helm upgrade api \
  oci://${REGISTRY}/${REPOSITORY}/helm-nvcf-api \
  --version 2.1.0 \
  --namespace nvcf \
  --wait --timeout 5m \
  -f nvcf-api-values.yaml

kubectl rollout status deployment/api -n nvcf --timeout=120s
```

<Info>
Always pass your values file (`-f values.yaml`) during upgrade. If you omit it, Helm
resets all values to chart defaults, which can break your deployment. If you no longer
have the original values file, back up the current values first with `helm get values`.

</Info>

### Upgrading StatefulSet-Based Services

Cassandra, NATS, and OpenBao are deployed as StatefulSets. The `helm upgrade` command is the
same, but rollout depends on the update strategy. The following rolling-update
description does not apply to OpenBao's `OnDelete` strategy:

- Rolling update: StatefulSets restart pods one at a time in reverse ordinal order,
  waiting for each pod to become ready before proceeding to the next.
- Quorum preserved: For 3-replica clusters, at most one pod is unavailable at a time,
  maintaining quorum throughout the upgrade.

```bash
helm upgrade <release> \
  oci://<registry>/<repository>/<chart> \
  --version <new-version> \
  --namespace <namespace> \
  --wait --timeout 10m \
  -f <release>-values.yaml

kubectl rollout status statefulset/<name> -n <namespace> --timeout=300s
```

Service-specific notes:

| Cassandra | After upgrading, check if the new version requires a schema migration. If a `cassandra-migrations` job exists in the chart, it will run automatically as a Helm post-upgrade hook. Verify it completes: `kubectl get jobs -n cassandra-system`. |
| --- | --- |
| OpenBao | Helm success does not replace existing `OnDelete` pods. Use the [OpenBao patch upgrade runbook](./runbooks/openbao-patch-upgrade.md) for 2.6.2 to 2.6.3. Replace standbys first, transfer leadership, and verify each member is unsealed before proceeding. |
| NATS | The NATS cluster maintains message availability during rolling updates as long as a majority of nodes remain online. Monitor the cluster state: `kubectl logs -n nats-system nats-0 --tail=10`. |

### Rolling Back

The commands below are not a data-downgrade procedure for OpenBao. Use a
separately validated recovery plan for stateful data services.

If an upgrade causes issues, roll back to the previous Helm revision:

```bash
# List revision history
helm history <release> -n <namespace>

# Roll back to the previous revision
helm rollback <release> -n <namespace>

# Verify
kubectl rollout status deployment/<name> -n <namespace> --timeout=120s

# Or for StatefulSets
kubectl rollout status statefulset/<name> -n <namespace> --timeout=300s
```

<Note>
`helm rollback` reverts both the chart version and the values. If you made intentional
value changes alongside the version upgrade, you will need to re-apply them after the
rollback.

</Note>

## Disabling Admin Issuer Proxy

Admin Issuer Proxy is a bootstrap helper. `POST /v1/admin/keys` returns a 12-hour admin JWT, signed by OpenBao as the NVCF API's in-cluster issuer, carrying function, secret, registry-credential, cluster-group listing and cluster-management scopes.

It does not authenticate callers. the stack relies on the gateway in front of it for that.

It exists so a new install with no identity provider can mint its first admin token, and `nvcf-cli init` uses it.

An install whose callers already get tokens from their own identity provider has no use for it, and can remove it.

<Warning>
`nvcf-cli init` and `nvcf-cli refresh` both call `POST /v1/admin/keys` through the same `generateAdminTokenViaAPI` function. Once the proxy is removed, both fail:

```text
$ nvcf-cli init
Error: failed to generate admin token: API error 404: 
Usage:
  nvcf-cli init [flags]
...
```

It is not a graceful failure. The message is bare because the Gateway returns an empty body for an unrouted host, and Cobra then prints usage as though the command were mistyped, which points the operator at their own typing rather than at the missing component. Neither command has a flag to supply a token you already hold, so there is no fallback inside the CLI.

What to do instead: obtain the admin token from your own identity provider and supply it as `NVCF_TOKEN`, the same credential `init` would have written. The CLI resolves a token in the order environment variable, then config file, then the state file `init` writes, so setting it by either of the first two makes `init` unnecessary rather than merely working around it:

```bash
export NVCF_TOKEN="<token from your identity provider>"
# or, to persist it, add to ~/.nvcf-cli.yaml:
#   token: "<token from your identity provider>"
```

Every other `nvcf-cli` command works unchanged from there. Operators following [the CLI guide](../overview/cli.md) or [the end-to-end installation example](./csp-end-to-end-example-installation.md) should skip the `nvcf-cli init` step on a stack where this proxy is disabled.

</Warning>

### Configuration

```yaml
# environments/<environment-name>.yaml
adminIssuerProxy:
  enabled: false
```

This value controls two things that must stay in step:

| What | Effect |
| --- | --- |
| The Helm release | Not installed, so no Deployment, Service, ServiceAccount, HTTPRoute, ReferenceGrant or PodDisruptionBudget |
| The OpenBao migration | `10_setup_admin-issuer.sh` is skipped, so the signing role, policy and auth role are never created |

The NVCF UI health list drops its Admin Issuer Proxy entry at the same time, so the UI does not report a missing component.

A related value, `adminIssuerProxy.gateway.enabled: false`, removes only the `api-keys.<domain>/v1/admin/keys` route and keeps the service reachable inside the cluster. Use it when `nvcf-cli init` is still needed from inside the cluster but the endpoint should not be on the public edge.

Setting `adminIssuerProxy.enabled: false` removes the route as well, so the two are not combined.

### Removing it from an existing install

Setting the value to `false` on a stack where the proxy is already installed does not remove anything. The release condition stops Helmfile managing the release, and the migration stops creating the OpenBao grant, but both already exist and stay as they are.

Removal is two steps, and both are required.

1. Uninstall the release. This removes everything the chart owns: the Deployment, Service, ServiceAccount, HTTPRoute and ReferenceGrant, plus the PodDisruptionBudget if `adminIssuerProxy.podDisruptionBudget.enabled` is set.

```bash
helm uninstall admin-issuer-proxy -n api-keys

# Verify: nothing left in the namespace, and no route on the shared Gateway
kubectl get pods,deploy,svc,sa,pdb -n api-keys | grep admin-token-issuer-proxy
kubectl get httproute,referencegrant -A | grep admin-token-issuer-proxy
```

1. Revoke the OpenBao grant. This is the step that is easy to miss.

OpenBao state is not owned by the Helm release, so uninstalling leaves the signing capability in place: any pod running as the `admin-issuer-proxy` service account in the `api-keys` namespace can still authenticate to OpenBao and sign admin-scoped tokens for the NVCF API issuer.

Until these three objects are deleted, removing the Deployment has reduced the exposed surface without withdrawing the capability.

Retrieve the root token as described in [Troubleshooting](./troubleshooting.md), then:

```bash
# The JWT auth role bound to the proxy's service account
kubectl exec -it openbao-server-0 -c openbao -n vault-system -- \
  env BAO_TOKEN=$BAO_ROOT_TOKEN \
  bao delete auth/jwt/role/admin-issuer-proxy

# The policy granting it signing rights
kubectl exec -it openbao-server-0 -c openbao -n vault-system -- \
  env BAO_TOKEN=$BAO_ROOT_TOKEN \
  bao delete sys/policies/acl/services-admin-issuer-proxy-jwt-sign-admin-issuer-proxy-rw

# The JWT secret role that mints the admin-scoped token
kubectl exec -it openbao-server-0 -c openbao -n vault-system -- \
  env BAO_TOKEN=$BAO_ROOT_TOKEN \
  bao delete services/nvcf-api/jwt/roles/admin-issuer-proxy
```

Verify all three are gone:

```bash
kubectl exec -it openbao-server-0 -c openbao -n vault-system -- \
  env BAO_TOKEN=$BAO_ROOT_TOKEN \
  sh -c 'bao list auth/jwt/role; bao policy list; bao list services/nvcf-api/jwt/roles' \
  | grep admin-issuer-proxy
```

No output means the grant is withdrawn.

<Note>
Re-enabling is simpler than disabling. Setting `adminIssuerProxy.enabled: true` reinstalls the release, and the next migration run recreates all three OpenBao objects, so nothing has to be restored by hand.

</Note>

### Verifying at the edge

The route's hostname may have no DNS record, which does not by itself make it unreachable. a load balancer that forwards the `Host` header passes the request to the Gateway regardless. Check the edge, not only the cluster:

```bash
curl -s -o /dev/null -w '%{http_code}\n' \
  --resolve api-keys.<your-domain>:443:<load-balancer-address> \
  https://api-keys.<your-domain>/v1/admin/keys
echo "curl exit: $?"
```

`--resolve` rather than `-H "Host: ..."`: the `Host` header is an HTTP header and does not set the TLS server name, so against a load-balancer IP the certificate would be checked against that IP and the request could fail before the Gateway ever saw it.

Read the two results together. Exit 0 with `404` means no route is attached, which is the outcome you want. Exit 0 with anything else means the route is still being served.

A non-zero curl exit means the request never reached the Gateway, so the check is inconclusive rather than a pass. the printed status will be `000`.

<Note>
If curl exits with a certificate error (exit 60), the edge is presenting a certificate that does not cover this hostname. expected when the route's hostname has no certificate of its own. Re-run with `-k` to get past the TLS check and read the routing result.

Only do this when diagnosing that specific failure: `-k` disables certificate verification and says nothing about whether the edge's TLS is sound.

</Note>

## Worker NATS Authentication

Every worker connects to NATS to receive its function's requests. `nats.workerAuth` sets how it authenticates:

| Mode | How a worker authenticates | What it can reach |
| --- | --- | --- |
| `nkey` (default) | The NVCF API gives every worker the control plane's shared NATS NKey, as `NKEY_SEED` | Everything in the `APP` account, with no expiry |
| `token` | Each worker uses its own worker token. The NATS auth callout sends it to the NVCF API, which checks it | Its own function version's subjects, in `APP` |

Use `token` when workers run outside the control-plane cluster, as in a multi-cluster install. In `nkey` mode, the shared key leaves the cluster on every worker launch.

<Warning>
Token mode needs an NVCF API that tells workers their account (`NVCF_NATS_ACCOUNT`), and a worker-utils image that reads it. An older worker-utils asks for the account `Worker`, which is refused, so its workers can't connect to NATS.

</Warning>

### Configuration

```yaml
# environments/<environment-name>.yaml
nats:
  workerAuth: token
```

This value changes three releases:

| Release | Effect in `token` mode |
| --- | --- |
| `nats` | The auth callout configuration gives the `APP` account a `webhook` plugin that sends worker tokens to the NVCF API's webhook URL. NKey logins and NVCA's login are unchanged |
| `api` | The API's secrets leave out `worker-nkey-seed`, so the API stops sending `NKEY_SEED` to workers. The API keeps its own NKey for its own connection |
| `nats-auth-callout-service` | Restarts whenever the mode or the webhook URL changes, because it reads its plugin configuration only at start-up |

The auth callout sends worker tokens to the NVCF API's in-cluster Service, `http://api.nvcf.svc.cluster.local:8080/v2/nvcf/webhook/nats-auth`. If the API runs elsewhere, set the URL:

```yaml
# environments/<environment-name>.yaml
nats:
  workerAuth: token
  authCallout:
    workerTokenAuth:
      webhookUrl: http://<api-service>.<api-namespace>.svc.cluster.local:8080/v2/nvcf/webhook/nats-auth
```

Any value other than `nkey` or `token` fails the deployment with `nats.workerAuth must be nkey or token`. In `token` mode, an empty webhook URL fails it too.

### Switching an existing install

1. Set `nats.workerAuth: token` and sync the stack.
2. New workers authenticate with their worker tokens. Workers that were already running keep the shared NKey and stay connected until they're replaced, for example by redeploying their functions.
3. Once no worker started before the switch is running, rotate the shared NATS NKey, because those workers held it.

Switching back to `nkey` restores the shared key for new workers and restarts the auth callout the same way.

### Verifying

The auth callout logs each successful login, with the account and the user it assigned. Worker logins in token mode show the account `APP` and a user named after the function version:

```bash
kubectl logs -n nats-system -l app.kubernetes.io/instance=nats-auth-callout-service \
  --tail=-1 --prefix | grep 'Authentication successful' | grep 'worker-'
```

If new workers don't appear there, look for refused logins:

```bash
kubectl logs -n nats-system -l app.kubernetes.io/instance=nats-auth-callout-service \
  --tail=-1 --prefix | grep -E 'Authentication (failed|denied)'
```

| Log shows | Cause |
| --- | --- |
| `plugin webhook not found for account Worker` | The worker asked for the account `Worker`, not `APP`. Its worker-utils doesn't read `NVCF_NATS_ACCOUNT`, or the NVCF API doesn't send `APP` |
| `Authentication denied`, with `status_code` 401 or 403 | The NVCF API refused the worker's token or account. Check the API's logs |
| `webhook request failed` | The auth callout can't reach the NVCF API at the webhook URL |

If the auth callout logged nothing for a worker, its login didn't reach the auth callout. Check the worker's own logs for its NATS connection error.

## Observability

For observability configuration and reference architecture, see [self-hosted-observability](../observability/observability.md).
