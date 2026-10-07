# Diagnose a failed self-hosted NVCF install

User says "my install failed" or "X isn't working". Don't guess — query the cluster.

## 1. Get the verdict

```sh
nvcf-cli self-hosted status --json | jq -c .
```

The first event is the snapshot. Parse `.verdict`:

- **`healthy`** — nothing's wrong. Ask the user what they're actually seeing; their problem may be at a different layer (function deploy, network, etc.).
- **`degraded`** — at least one component not ready. The Components list will identify which. Most common: `cassandra` PodInitializing, `openbao` not unsealed, `sis` ImagePullBackOff.
- **`failed`** — NVCFBackend `Health=Failed`. Read the cluster's NVCFBackend status condition message.
- **`unknown`** — ICMS unreachable. The control plane itself may be down; check `kubectl get pods -n sis` directly.

## 2. Drill into the failing component

For each non-ready component, the snapshot includes a `message` (e.g. `cassandra-0 PodInitializing 6m23s`). Ask the user to run:

```sh
kubectl describe pod -n <ns> <pod>
kubectl logs -n <ns> <pod> --all-containers --tail=100
```

Surface the events + last log lines. Don't propose `kubectl delete pod` unless the user confirms — Cassandra restart loses any in-progress migration.

## 3. Common patterns

| Symptom | Likely cause | Remediation |
|---|---|---|
| `cassandra-0 PodInitializing` for >5 min | StorageClass missing or PV stuck `Pending` | `kubectl get pv,pvc -A` — confirm a default StorageClass exists. Local dev: `kubectl patch storageclass local-path -p '{"metadata":{"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}'` |
| `helm install ...: timed out waiting for cassandra to become available` | Same as above | Same |
| `Gateway API CRDs not installed` (preflight) | Multi-cluster `nvcf-gateway-routes` chart needs envoy-gateway prerequisite | Install envoy-gateway: `helm install eg oci://docker.io/envoyproxy/gateway-helm --namespace=envoy-gateway-system --create-namespace` |
| `No cluster found with valid JWKS for cluster ID: <id>` (NVCA agent) | Compute plane's PSAT was issued by a K8s server whose JWKS doesn't match what's stored in ICMS | Re-register: `nvcf-cli cluster rotate --cluster-id=<id>` from a context that can reach the compute plane's K8s API |
| `Signed JWT rejected: Another algorithm expected, or no matching key(s) found` (init / cluster register) | Stale session.json from a different control plane | `rm ~/.nvcf-cli.state` then `nvcf-cli init` again |
| `nvca-operator` ImagePullBackOff for `nvca-operator:psat` | Image not in cluster (multi-cluster topology builds it locally) | `k3d image import nvcr.io/.../nvca-operator:psat -c <cluster>` (or whatever your image push path is) |
| `requiredEnv "NCA_ID" is not set` (helmfile error) | Older nvcf-cli; the env propagation fix landed in M+5 follow-ups | Upgrade `nvcf-cli` to a version after `6aa5704` |

## 4. When the user wants to start over

If they confirm a teardown:

- Single-cluster: `cd nvcf-self-managed-stack && make destroy` (or `helm uninstall` per release in reverse order).
- Multi-cluster: `helm uninstall nvca-operator -n nvca-operator` on the compute plane FIRST; then control plane; then `nvcf-cli cluster delete --cluster-id=<id>` to remove the ICMS row.

Always confirm before any teardown — Cassandra StatefulSet PVC deletion is destructive and loses cluster_oidc rows.

## What not to do

- **Don't propose `--debug` as a fix.** It's diagnostic only.
- **Don't propose `helm uninstall`** unless the user explicitly asked to start over.
- **Don't propose `kubectl delete pvc`** ever without confirmation — it deletes the underlying PV.
- **Don't re-run `up` with different inputs** to "see what happens". Diagnose with `status` / `kubectl describe` first.
