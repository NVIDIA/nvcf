<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# GDS MCS Controller Helm Chart

This directory contains the Helm chart for `gds-mcs-controller`, the
Multi-Cluster Services API (KEP-1645) controller of the Global Discovery
Service (GDS). One release runs in every member cluster of a clusterset. It
exports the cluster's ServiceExports to GDS and materialises the clusterset's
ServiceImports, derived Services and EndpointSlices from GDS. The controller
source and its full documentation are in
`src/control-plane-services/global-discovery` (`docs/mcs-controller.md`).
The server has its own chart, `deploy/helm/global-discovery`.

## Overview

The chart installs:

- the `gds-mcs-controller` Deployment: two replicas with leader election
  (only the leader writes to GDS and applies imports), readiness on
  `/readyz` and liveness on `/healthz` of the health port, a soft
  anti-affinity that spreads the replicas over nodes, and the
  pylon-operator security context (non-root, read-only root filesystem, no
  capabilities)
- a ServiceAccount with its API token mounted
- a ClusterRole and ClusterRoleBinding with the rules of the controller's
  generated `config/rbac/role.yaml`, trimmed by the values (see
  [RBAC](#rbac))
- a Role and RoleBinding for the leader election Lease
  `gds-mcs-controller.gds.nvidia.com` in the release namespace
- the ServiceExport and ServiceImport CRDs (`multicluster.x-k8s.io/v1beta1`)
  and the ClusterProperty CRD (`about.k8s.io/v1alpha1`) (see [CRDs](#crds))
- a ClusterIP Service for `/metrics`, a PodDisruptionBudget with more than
  one replica and, optionally, a Prometheus Operator ServiceMonitor

## Prerequisites

- Kubernetes 1.27 or later and Helm 3.x.
- The `nvcf-gds-mcs-controller` image (Bazel target
  `//src/control-plane-services/global-discovery:controller_image`) in a
  registry the cluster can pull from. The chart sets no image repository.
- A GDS deployment reachable from the cluster on its gRPC port (chart
  `deploy/helm/global-discovery`), and its CA when it serves TLS with a
  private CA.
- A clusterset id shared by every cluster and a cluster id per cluster, both
  DNS labels. The cluster id must equal the LLM stack `clusterId` of the
  cluster.
- For ClusterSetIP imports to carry traffic: pod IPs of every source cluster
  routable from this cluster (module `docs/mcs-controller.md`,
  "Reachability prerequisite").

## Install

A values file for one cluster:

```yaml
image:
  repository: <your-registry>/<your-org>/nvcf-gds-mcs-controller
gds:
  urls:
    - https://gds.prod.internal
    - https://gds.us-east-1.prod.internal
  tlsCaConfigMap: gds-ca
clusterset:
  id: prod
cluster:
  id: use1-a
  region: us-east-1
```

```bash
kubectl create namespace gds-system
kubectl -n gds-system create configmap gds-ca --from-file=ca.crt=path/to/gds-ca.crt
helm install gds-mcs-controller deploy/helm/global-discovery-mcs-controller/global-discovery-mcs-controller \
  --namespace gds-system \
  --values path/to/use1-a.yaml \
  --set fullnameOverride=gds-mcs-controller \
  --wait
```

`helm --wait` returns when both replicas are ready: connected to GDS and, on
the leader, the cluster lease registered. Then export a Service:

```bash
kubectl -n <namespace> label service <name> gds.nvidia.com/export=true
kubectl get serviceexports,serviceimports -A
```

Repeat in every member cluster with its own `cluster.id`. The release notes
print these commands.

For development against a plaintext gds-server, set
`gds.allowInsecureHttp: true` with `http://` URLs.
`tests/e2e/global-discovery-k3d` installs the chart that way in two k3d
clusters.

## Values

Rendering fails when a required value is empty or a combination is invalid.
`values.schema.json` rejects unknown keys and checks ids, URLs, durations,
ports and label keys.

| Key | Default | Description |
| --- | --- | --- |
| `image.repository` | `""` | Controller image repository. Required. |
| `image.tag` | `""` | Image tag. Defaults to the chart `appVersion`. A `sha256:` value is used as a digest. |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy. |
| `imagePullSecrets` | `[]` | Pull Secrets, as `{name: <Secret>}` entries. |
| `replicaCount` | `2` | Replicas. More than one needs `leaderElection.enabled`. |
| `gds.urls` | `[]` | `--gds-url`, one per entry, in failover order. Required. One scheme for all. |
| `gds.tlsCaConfigMap` | `""` | ConfigMap with key `ca.crt`, mounted at `/etc/gds/ca`; `--gds-tls-ca-file`. Empty trusts the system roots. |
| `gds.allowInsecureHttp` | `false` | `--gds-allow-insecure-http`. Required for `http://` URLs; development only. |
| `clusterset.id` | `""` | `--clusterset-id`. Required with `identity.source: flags`. |
| `cluster.id` | `""` | `--cluster-id`. Required with `identity.source: flags`. |
| `cluster.region` | `""` | `--region`. Empty uses the `topology.kubernetes.io/region` node label. |
| `cluster.labels` | `{}` | `--cluster-labels`, sorted by key. At most 64. |
| `identity.source` | `flags` | `--cluster-identity-source`: `flags`, `auto` (values win, else ClusterProperty) or `clusterproperty`. The controller's own default is `auto`; the chart defaults to `flags` so missing ids fail the render. |
| `identity.writeClusterProperties` | `false` | `--write-cluster-properties`. Grants ClusterProperty create, update and patch. |
| `lease.ttl` | `30s` | `--lease-ttl`, 10 s to 5 min. Heartbeats every TTL/3. |
| `deregisterOnShutdown` | `false` | `--deregister-on-shutdown`. Deletes every export of the cluster in GDS on stop; leave off. |
| `imports.createNamespaces` | `false` | `--create-namespaces`. Grants namespace create. |
| `imports.adoptUnmanaged` | `false` | `--adopt-unmanaged-imports`. |
| `pools.autoExport` | `true` | `--auto-export-pools`: ServiceExports for the `services[]` of API-registered pools of this cluster. |
| `watchNamespaces` | `[]` | `--watch-namespaces`. Empty means all namespaces. |
| `endpointsPerSlice` | `100` | `--endpoints-per-slice`, 1 to 1000. |
| `maxConcurrentReconciles` | `4` | `--max-concurrent-reconciles`. |
| `leaderElection.enabled` | `true` | `--leader-elect`, rendered as `true` or `false` (the controller default is true). Off needs `replicaCount: 1` and switches the rollout to Recreate. |
| `crds.install` | `true` | Render the three CRDs. See [CRDs](#crds). |
| `otel.enabled` | `false` | `--otel-enabled`. |
| `otel.endpoint` | `""` | `OTEL_EXPORTER_OTLP_ENDPOINT`. |
| `otel.resourceAttributes` | `{}` | Extra `OTEL_RESOURCE_ATTRIBUTES` entries; the pod and namespace are always set. |
| `logLevel` | `info` | `--zap-log-level`: `debug`, `info` or `error`. |
| `extraArgs` | `[]` | Extra controller arguments, appended last, for example `--zap-encoder=console`. |
| `extraEnv` | `[]` | Extra container environment variables. |
| `serviceAccount.create`, `name`, `annotations` | `true`, `""`, `{}` | The ServiceAccount; the name defaults to the release full name. |
| `metrics.port` | `8080` | `--metrics-bind-address` and the metrics Service port. |
| `metrics.serviceMonitor.enabled` | `false` | Render a ServiceMonitor for `/metrics`. |
| `metrics.serviceMonitor.interval`, `scrapeTimeout`, `labels` | `30s`, `10s`, `{}` | ServiceMonitor settings. |
| `healthProbe.port` | `8081` | `--health-probe-bind-address`. |
| `probes.readiness`, `probes.liveness` | see `values.yaml` | Probe timing. |
| `podDisruptionBudget.enabled` | `true` | Render the PDB; skipped with one replica. |
| `podDisruptionBudget.minAvailable`, `maxUnavailable` | `1`, `""` | Set exactly one. |
| `terminationGracePeriodSeconds` | `30` | Time to release the Lease (and deregister, when enabled). |
| `resources` | 50m CPU, 64Mi request; 256Mi limit | Container resources. |
| `podAnnotations`, `podLabels`, `priorityClassName`, `nodeSelector`, `tolerations` | empty | Pod settings. |
| `affinity` | `{}` | Pod affinity. Empty renders a preferred anti-affinity over `kubernetes.io/hostname`. |
| `podSecurityContext`, `securityContext` | non-root uid 1000, read-only root, drop `ALL`, `RuntimeDefault` seccomp | Security contexts. |

The chart always passes `POD_NAMESPACE` from the downward API, which
defaults `--operator-namespace`, the namespace of the leader election Lease.
Boolean flags are always rendered with their value, so a value never
depends on a controller default.

## RBAC

The controller's ClusterRole is generated from its `+kubebuilder:rbac`
markers into `config/rbac/role.yaml` of the module (`make codegen-update`).
The chart's `templates/clusterrole.yaml` keeps those rules and trims two of
them by value:

| Rule | Granted when |
| --- | --- |
| `namespaces`: get, list, watch | always |
| `namespaces`: create | `imports.createNamespaces` |
| `clusterproperties` (`about.k8s.io`): get, list, watch | always |
| `clusterproperties`: create, update, patch | `identity.writeClusterProperties` |
| `serviceexports`: get, list, watch, update, patch, create, delete | always |
| `serviceexports/status`, `serviceexports/finalizers`, `serviceimports`, `serviceimports/status`, `services`, `endpointslices`, `nodes`, `events` | always, as in `role.yaml` |

`serviceexports` create and delete stay because label-driven export
(`gds.nvidia.com/export=true`) always runs; the controller has no flag to
turn it off. `scripts/check-render.sh` renders the chart with both options on
and fails unless the ClusterRole grants exactly what `role.yaml` grants, so
a new RBAC marker in the controller fails `make test` until the template
follows.

Leader election uses a namespaced Role in the release namespace:
`coordination.k8s.io` leases (get, list, watch, create, update, patch,
delete) and events (create, patch). It is rendered only with
`leaderElection.enabled`.

## CRDs

`templates/crds/` holds the CRDs the controller needs:

- `multicluster.x-k8s.io_serviceexports.yaml` and
  `multicluster.x-k8s.io_serviceimports.yaml` from `sigs.k8s.io/mcs-api`, the
  version the global-discovery module requires in its `go.mod`;
- `about.k8s.io_clusterproperties.yaml`, the module's
  `config/crd/bases` copy of the KEP-2149 ClusterProperty CRD.

The module is the single source of truth: its `config/crd` package embeds the
same files for the envtest suite. `scripts/sync-crds.sh` (`make sync-crds`)
copies them byte for byte and adds only the `crds.install` gate, the chart
labels and `helm.sh/resource-policy: keep`. `make check-crds` fails when the
copies drifted; `make test` runs the same comparison and, without Go,
compares the mcs-api version against `go.mod` only. After changing the
mcs-api version or the ClusterProperty CRD in the module, run
`make sync-crds` and commit the result.

The CRDs are chart templates rather than files in the Helm `crds/`
directory, so `crds.install` can turn them off and `helm upgrade` updates
them (Helm never upgrades `crds/` files). The keep policy leaves them, and
every ServiceExport and ServiceImport, in place when the release is
uninstalled or `crds.install` turns false.

Ownership: one release per cluster should own the CRDs. Set
`crds.install: false` when another MCS implementation or a platform
installs `multicluster.x-k8s.io` CRDs; Helm refuses to install over CRDs it
does not own. To take over CRDs created by `kubectl apply`, label and
annotate them for the release first:

```bash
for crd in serviceexports.multicluster.x-k8s.io serviceimports.multicluster.x-k8s.io clusterproperties.about.k8s.io; do
  kubectl label crd "$crd" app.kubernetes.io/managed-by=Helm --overwrite
  kubectl annotate crd "$crd" meta.helm.sh/release-name=gds-mcs-controller \
    meta.helm.sh/release-namespace=gds-system --overwrite
done
```

## Upgrades and disruptions

- A rolling update adds one new replica, waits for it to be ready, then
  stops an old one (`maxUnavailable: 0`, `maxSurge: 1`). A stopping leader
  releases its Lease, so another replica takes over within seconds; it
  re-registers the cluster lease and resyncs every export.
- Leave `deregisterOnShutdown` off. The cluster lease (`lease.ttl` plus the
  server's skew) covers a short absence; deregistering would withdraw the
  cluster's exports from every other cluster during each restart.
- Imports survive restarts: the new leader receives a snapshot from GDS and
  applies only what changed. Managed objects are deleted only when GDS no
  longer lists them after a complete snapshot.
- CRD changes arrive with the chart. A newer mcs-api CRD version is applied
  by `helm upgrade`; the controller and the CRDs move together.
- Changing `cluster.id` or `clusterset.id` registers a new cluster in GDS.
  The old cluster's exports stay until its lease expires and the server's
  retention removes them; deregister it with `gds-admin delete-cluster` to
  remove them at once.
- With `leaderElection.enabled: false` and one replica, the rollout is
  Recreate: the old pod stops before the new one starts.

## Development

From the repository root:

```bash
make -C deploy/helm/global-discovery-mcs-controller lint        # helm lint with the CI values
make -C deploy/helm/global-discovery-mcs-controller template    # render to bin/manifest.yaml
make -C deploy/helm/global-discovery-mcs-controller validate    # kubeconform, when installed
make -C deploy/helm/global-discovery-mcs-controller test        # scripts/check-render.sh
make -C deploy/helm/global-discovery-mcs-controller sync-crds   # copy the CRDs from the module
make -C deploy/helm/global-discovery-mcs-controller check-crds  # fail when they drifted (needs Go)
tools/ci/check-helm-charts                                      # every chart with CI values
```

The CI values are in
`tools/ci/helm-validate-values/global-discovery-mcs-controller.yaml`.
`scripts/check-render.sh` also checks that every rendered flag is defined
in the controller's `internal/mcs/config`.

When the controller gains a flag, add a value, render it in
`templates/_helpers.tpl`, assert it in `scripts/check-render.sh` and add a
row to the table above.
