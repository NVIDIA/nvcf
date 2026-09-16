# Control Plane Operations

This section provides runbooks for operating the self-hosted NVCF control plane, including encryption key rotation, service management, and upgrades.

## Encryption Key Management

Self-hosted NVCF uses a two-tier encryption hierarchy to protect secrets stored in the Encrypted Secret Store (ESS):

| Key | Purpose |
| --- | --- |
| **MEK** (Master Encryption Key) | A single AES-256-GCM key stored in OpenBAO. The MEK encrypts (wraps) all NEKs. It is shared across NVCF services in the control plane. |
| **NEK** (Namespace Encryption Key) | Per-namespace AES-256-GCM keys stored in Cassandra (encrypted by the MEK). NEKs directly encrypt user secrets such as API keys and registry credentials. |

When a user stores a secret through the NVCF API, ESS encrypts it with the active NEK for that namespace. The NEK itself is stored in Cassandra, encrypted by the MEK. To decrypt a secret, ESS retrieves the NEK from Cassandra, decrypts it using the MEK from OpenBAO, then decrypts the secret.

### Key Rotation Runbooks

- [control-plane-runbook-mek-rotation](runbooks/control-plane-key-rotation-mek.md) — Rotate the master encryption key stored in OpenBAO.

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

**Restarting a Deployment:**

```bash
kubectl rollout restart deployment/<name> -n <namespace>

# Example: restart the NVCF API
kubectl rollout restart deployment/nvcf-api -n nvcf

# Verify the rollout completes
kubectl rollout status deployment/<name> -n <namespace> --timeout=120s
```

**Restarting a StatefulSet:**

StatefulSets perform a rolling restart, terminating and recreating one pod at a time in
reverse ordinal order (highest first). For clustered services like NATS, OpenBao, and
Cassandra, this preserves quorum as long as a majority of replicas remain available.

```bash
kubectl rollout restart statefulset/<name> -n <namespace>

# Example: restart Cassandra
kubectl rollout restart statefulset/cassandra -n cassandra-system

# Verify the rollout completes
kubectl rollout status statefulset/<name> -n <namespace> --timeout=300s
```

<Note>
For OpenBao, verify the seal status after the rollout completes. Each pod must unseal
before it can serve requests:

```bash
kubectl exec -n vault-system openbao-server-0 -- bao status
```

</Note>

**Restarting all Deployments in a namespace:**

```bash
kubectl rollout restart deployment -n <namespace>

# Example: restart all services in the nvcf namespace
kubectl rollout restart deployment -n nvcf
```

### Checking Service Health

**List pods and their status:**

```bash
# All pods in a namespace
kubectl get pods -n <namespace>

# All NVCF control plane pods at a glance
for ns in nvcf sis api-keys ess nats-system vault-system cassandra-system; do
  echo "=== $ns ==="
  kubectl get pods -n $ns
done
```

**Check logs for a service:**

```bash
# Recent logs
kubectl logs -n <namespace> -l app.kubernetes.io/name=<name> --tail=50

# Follow logs in real time
kubectl logs -n <namespace> -l app.kubernetes.io/name=<name> -f
```

**Describe a pod for events and conditions:**

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

<Warning>
**Upgrades are not officially supported during the Early Access period.** The self-hosted
NVCF stack does not yet have a validated upgrade path. Even a full `helmfile sync` may
introduce breaking changes between releases — there is no guarantee of backward
compatibility for configuration, database schemas, or inter-service APIs at this stage.

</Warning>

The guidance below is provided for **advanced users** who need to apply targeted fixes or
hotfixes to individual services. It is not a substitute for a validated upgrade procedure.

<Warning>
**Spot upgrades carry additional risk.** Beyond the general Early Access limitations above,
spot-upgrading an individual Helm chart bypasses the Helmfile's version coordination and
automatic database migrations. Proceed only when you understand the compatibility
implications for the specific version you are upgrading to.

</Warning>

### When to Spot Upgrade

| Use a spot upgrade when | Use a full Helmfile upgrade when |
| --- | --- |
| Applying a patch release to a single service (e.g., `1.2.6` to `1.2.7`) | Upgrading the entire stack to a new minor or major version |
| Applying a targeted hotfix provided by NVIDIA support | The new version includes Cassandra schema migrations |
| You need to roll out a configuration change that requires a new chart version | Multiple services need to be upgraded together for compatibility |

### Pre-Upgrade Checklist

Before upgrading any chart:

1. **Note the current chart version and app version:**

   ```bash
   helm list -n <namespace>
   ```

2. **Back up the current Helm values:**

   ```bash
   helm get values <release> -n <namespace> -o yaml > <release>-values-backup.yaml
   ```

3. **Review release notes** for the target version. Check for breaking changes, required
   value changes, or new dependencies.

4. **Verify the cluster is healthy** before starting — all pods running, no pending operations.

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

**Example — upgrading the NVCF API chart:**

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
same, but the rollout behavior differs:

- **Rolling update:** StatefulSets restart pods one at a time in reverse ordinal order,
  waiting for each pod to become ready before proceeding to the next.
- **Quorum preserved:** For 3-replica clusters, at most one pod is unavailable at a time,
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

**Service-specific notes:**

| **Cassandra** | After upgrading, check if the new version requires a schema migration. If a `cassandra-migrations` job exists in the chart, it will run automatically as a Helm post-upgrade hook. Verify it completes: `kubectl get jobs -n cassandra-system`. |
| --- | --- |
| **OpenBao** | After each pod restarts, verify it unseals successfully: `kubectl exec -n vault-system openbao-server-0 -- bao status`. If auto-unseal is configured, this happens automatically. Otherwise, you must unseal each pod manually. |
| **NATS** | The NATS cluster maintains message availability during rolling updates as long as a majority of nodes remain online. Monitor the cluster state: `kubectl logs -n nats-system nats-0 --tail=10`. |

### Rolling Back

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

## Observability

For observability configuration and reference architecture, see [self-hosted-observability](./observability.md).
