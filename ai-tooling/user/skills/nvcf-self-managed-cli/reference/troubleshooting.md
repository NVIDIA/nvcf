# Troubleshooting

Known errors, the diagnostic command, and the remediation. Keep in sync with the structured events the CLI emits (REQ-15): outside pre-flight, every entry below should map to a `phase_failed` `errCategory` and a `remediation` array.

## Pre-flight

`check` reports each finding as a `check_completed` event, not as
`phase_failed`. The symptom is the event's `message`; cluster-validator rows
quote the validator's failed summary rows, and `--show-logs` prints the full
transcript.

| Symptom | Cause | Remediation |
|---|---|---|
| `kubectl not found on PATH` | Operator's `$PATH` doesn't include kubectl. An error with `--pre`; other scopes warn, adding `; only an install needs it` | Install kubectl 1.28+: https://kubernetes.io/docs/tasks/tools/install-kubectl/ |
| `helmfile not found on PATH` | Same | https://github.com/helmfile/helmfile#installation |
| `helm not found on PATH` | Same | https://helm.sh/docs/intro/install/ |
| `cluster-validator not run: cluster_validator_image is not set; ...`, warning | No validator image is configured, so the cluster was not validated | Set `cluster_validator_image` in the nvcf-cli config, `NVCF_CLI_CLUSTER_VALIDATOR_IMAGE` or `--cluster-validator-image` |
| `cluster-validator did not complete, so the cluster was not validated: ...`, error | The validator could not run or ended before its verdict: RBAC denied, image pull failure, its own timeout | Fix the cause the message names, or pass `--skip-cluster-validation` |
| `cluster-validator Job succeeded; its transcript could not be read (...)`, passed | The validator passed, but its log could not be read: the kubeconfig user lacks `get` on `pods/log` in `default`, or the API server cannot reach the kubelet. The checks it ran and warnings it reported are not shown: an image that predates validator roles runs the compute-plane checks for either role | Grant `get` on `pods/log` in `default`, or read the transcript with the `kubectl logs` command in the row |
| `cluster-validator reported failures: Gateway API CRDs: Not Installed` | Envoy Gateway prereq missing | `helm install eg oci://docker.io/envoyproxy/gateway-helm --namespace=envoy-gateway-system --create-namespace` |
| `cluster-validator reported failures: Default StorageClass: Not Found` | Cluster has no default SC, and the stack does not name one | Annotate one: `kubectl patch sc <name> -p '{"metadata":{"annotations":{"storageclass.kubernetes.io/is-default-class":"true"}}}'`, or set `global.storageClass` in the stack's environment file (or `NVCF_STORAGE_CLASS`) to the class the stack uses |
| `cluster-validator reported failures: Tier-1 Deployments: Not Ready` (control-plane) | An NVCF Deployment, Envoy proxy or the Envoy Gateway controller is not fully Ready, or its rollout is stuck. After install it also fails when an NVCF Service selects no pod that is serving or starting while a workload of that release is Ready under other labels, or without a label the Service selects, as an upgrade that changed the pod labels or the Service's selector leaves it: the Service has no endpoint. A Completed hook pod or a terminating pod does not count as selected | Run with `--show-logs`: each finding names the Deployment, or the Service, its selector and the pod labels. For a Service, read `helm -n <namespace> history <release>`, then roll the release back or recreate the workload so its pods carry the labels the Service selects |
| `cluster-validator reported failures: HA Guarantee: ...` (control-plane) | A workload falls short of what the HA mode requires: a replica count, a PodDisruptionBudget or, under `enforced`, its placement, including a quorum whose Ready members crowd one zone beyond the zone spread's `maxSkew` 1, or quorum members on an older revision that lack the placement their StatefulSet's template now renders. An environment or secrets file override that weakens the stack's HA values reads as a degraded guarantee. Without a mode, only replicas all on one node despite a required hostname anti-affinity term fail it | Run with `--show-logs`: each finding names the workload, what the mode requires and what was found. Restore the HA values the mode sets, or pass the `--mode` the install used. Move members behind the update revision one at a time: under `updateStrategy: OnDelete` delete one, standbys first, and wait for it to be Ready before the next; under a held partition, lower it to 0 |
| `cluster-validator reported failures: HA Topology: ...` (control-plane) | Under the HA mode the cluster has fewer than 3 schedulable nodes for the quorum services, or, under `enforced`, fewer than 3 zones, too little node capacity, a StorageClass that binds volumes immediately, or quorum members whose node-local volumes pin them where that placement cannot hold, such as two on one node or one on a node it cannot run on (cordoned, NotReady, removed or outside its placement). Before install a node with a `NoSchedule` or `NoExecute` taint does not count; the finding names it | Add schedulable nodes, label each with `topology.kubernetes.io/zone`, or use a StorageClass with `volumeBindingMode: WaitForFirstConsumer` whose volumes can attach on another node. Move a member pinned by a node-local volume only by its service's own member-replacement procedure. Under `preferred` these zone, capacity, binding and pinned-volume findings are warnings |
| `... (degraded: HA Guarantee, HA Topology)`, warning | An HA row warned: under `preferred`, missing or soft placement, co-located replicas, a quorum crowding one zone, or a zone or capacity shortfall, which leave the control plane exposed to a node or zone loss; without a mode, replicas all on one node under a preferred hostname anti-affinity term or a hostname spread; in either mode, a Deployment that does not roll out with `maxSurge` 1 and `maxUnavailable` 0, a PodDisruptionBudget on `grpc-proxy` or `invocation-service` that blocks node drains, quorum members behind their StatefulSet's update revision, or a StorageClass that provisions node-local volumes, so a quorum member whose node fails cannot be recreated on another node | Run with `--show-logs` for the findings. Placement, zone and capacity findings fail under `enforced` |
| `... the cluster-validator image does not support --mode, so the HA invariants of mode <mode> were not checked ...`, warning or error | The validator image predates the HA rows and ignored `NVCF_HA_MODE` set to `preferred` or `enforced`. It is an error when the mode came from `--mode`. Mode `none` asks for no HA check, so it is not graded | Use a validator image from an NVCA release that supports it |
| `cluster-validator reported failures: GPU Resources: Not Available` (compute-plane) | No node advertises an allocatable GPU: no GPU nodes, or the NVIDIA GPU Operator is missing or not working | Install per https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/install-gpu-operator.html, OR use `fake-gpu-operator` for dev. `GPU Operator: Not Installed` alone is a warning |
| `stale-namespaces`: `<ns> (stuck Terminating: ...)`, error, exit `2` | The namespace has been deleting for over 2 minutes, or its conditions report a deletion failure. Usually an object inside it still holds a finalizer | Run the commands in the row in order: read `kubectl get ns <ns> -o jsonpath='{.status.conditions}'`, list what is left with the `api-resources` command, and resolve those objects. Force-clear the namespace finalizers only as a last resort |
| `stale-namespaces`: `<ns> (Terminating: deleting for <age>)`, warning | A normal deletion is still draining volumes and finalizers | Wait and rerun `check`; `--wait` polls this row. It becomes `stuck Terminating` after 2 minutes |
| `stale-namespaces`: `<ns> (Helm release mid-operation: <release> <status>)`, warning | An interrupted install, upgrade, rollback or teardown left the release `pending-*` or `uninstalling`. The next `up` fails on it | If no install or teardown is running, read `helm history <release> -n <ns>`, then finish it with `helm rollback` or `helm uninstall` |
| `stale-namespaces`: `<ns> (no Helm release)`, warning | The namespace runs no workload and has no Helm release, but still holds volume claims or workload objects, as `down` leaves them. A reinstall reattaches the old volumes | Inspect only: `kubectl api-resources --verbs=list --namespaced -o name \| xargs -n1 kubectl get -n <ns> --show-kind --ignore-not-found`. An Argo CD or upstream install of a gated component also has no Helm release |
| `stale-namespaces` reports `no Helm release` on a healthy install | Helm stores releases with `HELM_DRIVER=sql`, and `check` reads `HELM_DRIVER` from its own environment only, not from the helmfile environment | Export `HELM_DRIVER=sql` before `nvcf-cli self-hosted check`. With it, only Terminating namespaces are checked |
| `stale-namespaces` does not scan an add-on or gated namespace | Namespaces behind a stack `condition:` are probed only when that gate is on. Gates come from the stack the install used, found as for the [settings read from the stack](flags.md#settings-read-from-the-stack): its `environments/base.yaml` and, when `--env` or `HELMFILE_ENV` names the environment or `--pre` checks the plane before its install, `environments/<env>.yaml`. Otherwise the stack defaults decide, which leave `kai-scheduler`, `grove-system`, `dynamo-system` and `nvcf-ui` off | Pass the `--env` the install used, and the stack flag the install used, if any |

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
