# Troubleshooting

Known errors → diagnostic command → remediation. Keep in sync with the structured `phase_failed` events the CLI emits (REQ-15) — every entry below should map to an `errCategory` and a `remediation` array.

## Pre-flight

| Symptom | Cause | Remediation |
|---|---|---|
| `kubectl not on PATH` | Operator's `$PATH` doesn't include kubectl | Install kubectl 1.28+: https://kubernetes.io/docs/tasks/tools/install-kubectl/ |
| `helmfile not on PATH` | Same | https://github.com/helmfile/helmfile#installation |
| `helm not on PATH` | Same | https://helm.sh/docs/intro/install/ |
| `Gateway API CRDs not installed` | envoy-gateway prereq missing | `helm install eg oci://docker.io/envoyproxy/gateway-helm --namespace=envoy-gateway-system --create-namespace` |
| `default StorageClass not available` | Cluster has no default SC | Annotate one: `kubectl patch sc <name> -p '{"metadata":{"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}'` |
| `GPU operator not installed` (compute-plane) | nvidia GPU operator missing | Install per https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/install-gpu-operator.html, OR use `fake-gpu-operator` for dev |
| `GPU node labels missing` (compute-plane) | Nodes need `nvidia.com/gpu.family` etc. | Set `--k3s-node-label nvidia.com/gpu.family=hopper@agent:N` for k3d |

## Auth

| Symptom | Cause | Remediation |
|---|---|---|
| `admin token required` (init not run) | No `~/.nvcf-cli.state` and `--token` not passed | `nvcf-cli init` (interactive) or `--token=$JWT --non-interactive` (CI) |
| `Signed JWT rejected: Another algorithm expected, or no matching key(s) found` | Stale session.json from a different control plane | `rm ~/.nvcf-cli.state` then `nvcf-cli init` again |
| `403 missing requested authorities` (function invoke) | Admin token doesn't have `invoke_function` scope | `nvcf-cli api-key generate --description=…` then re-run invoke |

## Install / `up`

| Symptom | Cause | Remediation |
|---|---|---|
| Helm `timed out waiting for cassandra` | Cassandra StatefulSet PVC stuck `Pending` | Confirm default StorageClass; check `kubectl get pv,pvc -n cassandra-system` |
| `Invalid GPU 'NCP.GPU.H100' specified` (function deploy) | Wrong field — `gpu` is the family, not the SKU | Use `"gpu": "H100"`, `"instanceType": "NCP.GPU.H100_1x"` |
| `region must be provided` (cluster register / install --compute-plane) | `--region` empty + `CLUSTER_REGION` env empty | Pass `--region=us-west-1` (or any non-empty value) |
| `requiredEnv "NCA_ID" is not set` (helmfile error during compute-plane apply) | Older nvcf-cli before commit `6aa5704`; ExtraEnv not propagating NCA_ID | Upgrade `nvcf-cli` to a release after `6aa5704` |
| `nvca-operator: ImagePullBackOff` for `nvca-operator:psat` | Image not in cluster registry; multi-cluster build is local | `k3d image import nvcr.io/.../nvca-operator:psat -c <cluster>` |

## Compute-plane runtime

| Symptom | Cause | Remediation |
|---|---|---|
| NVCA agent CrashLoopBackOff with `HTTP 401` on `/v1/nvca/clusters/.../register` | Stale `out/<cluster>-register-values.yaml` from prior register; cluster ID doesn't match ICMS | Delete the stale file; re-run `nvcf-cli self-hosted up` (idempotent register writes a fresh file) |
| `No cluster found with valid JWKS for cluster ID: <id>` (ICMS log) | JWKS in ICMS doesn't match what compute-plane K8s is currently signing | `nvcf-cli cluster rotate --cluster-id=<id>` |
| Function pod `ImagePullBackOff` in `nvcf-backend` | Image-credential-helper config missing or wrong | `kubectl describe pod -n nvcf-backend <pod>`; check the pull secret config |
| Function deploy stuck `DEPLOYING` for >5 min | NATS stream init lag (cold-cluster) — wait, or check NATS auth-callout health | `kubectl logs -n nats-system -l app.kubernetes.io/name=nats-auth-callout-service` |

## Tasks

| Symptom | Cause | Remediation |
|---|---|---|
| `task create` returns `Missing <TYPE> registry credential for hostname`, or keeps using a previous credential value, shortly after `registry-credential add/update/delete` while `registry-credential list`/`get` already show the new value | Expected propagation delay: NVCT caches the account's registry credentials with `nvct.nvcf.cache-ttl` (default `PT5M`, about 5 min) and applies the change when that cached copy refreshes | Wait up to about 5 min and retry, or `kubectl -n nvcf rollout restart deployment/nvct-api` to apply immediately |
