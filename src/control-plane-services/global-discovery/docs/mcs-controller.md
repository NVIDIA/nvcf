<!--
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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

# gds-mcs-controller

gds-mcs-controller implements the Kubernetes Multi-Cluster Services API
(KEP-1645, `multicluster.x-k8s.io/v1beta1`) on top of the Global Discovery
Service. One controller runs in every member cluster. It exports the
cluster's ServiceExports to GDS and materialises the ServiceImports of the
whole clusterset, with their derived Services and EndpointSlices, from
GDS. The design is section 8 of
`docs-gds/global-discovery-service-design.md`; flows are in 4.3 and 4.4.

Code: `cmd/gds-mcs-controller` and `internal/mcs/*`. Image:
`//src/control-plane-services/global-discovery:controller_image`.

## Components

All run on one controller-runtime manager. Everything that talks to GDS
as a writer or applies imports is leader-only (`--leader-elect`, Lease
`gds-mcs-controller.gds.nvidia.com` in `--operator-namespace`).

| Component | Package | Role |
| :---- | :---- | :---- |
| Identity | `internal/mcs/identity` | Resolves the clusterset and cluster ids, region, zones and provider at startup; optionally writes ClusterProperty objects |
| Lease | `internal/mcs/lease` | `RegisterCluster`, then `HeartbeatCluster` every TTL/3 with 10 percent jitter |
| ServiceExport reconciler | `internal/mcs/export` | Writes exports and endpoint sets, writes ServiceExport conditions |
| Resyncer | `internal/mcs/export` | Garbage-collects GDS records and resends every export |
| Auto-export | `internal/mcs/export` | Label-driven ServiceExports (Service half) and pool ServiceExports (pool half) |
| Importer | `internal/mcs/importer` | `WatchServiceImports` stream and the import controller |

## Identity

- `--cluster-identity-source=auto` (default): `--clusterset-id` and
  `--cluster-id` win; an id without its flag comes from the ClusterProperty
  `clusterset.k8s.io` or `cluster.clusterset.k8s.io` (`about.k8s.io/v1alpha1`,
  KEP-2149). A missing ClusterProperty CRD counts as no property.
- `flags`: both flags are required; ClusterProperty objects are not read.
- `clusterproperty`: both properties are required; flags, when set, must
  agree with them.
- Both ids are DNS labels. The cluster id must equal the LLM stack
  `clusterId` of the cluster.
- Region: `--region`, else the most common `topology.kubernetes.io/region`
  node label. Zones: every `topology.kubernetes.io/zone` node label.
  Provider: the scheme of the nodes' `spec.providerID` (`aws`, ...).
- `--write-cluster-properties` creates the two ClusterProperty objects for
  other MCS tooling. An existing property with another value stops the
  controller instead of being overwritten.

## Cluster lease

The lease registers the cluster with `--lease-ttl` (default 30 s, accepted
range 10 s to 5 m) and heartbeats every TTL/3. A heartbeat refused with
`ABORTED LEASE_EPOCH_MISMATCH` (another registration took the lease) or
`NOT_FOUND CLUSTER_NOT_REGISTERED` (GDS lost or deleted the cluster)
registers again and triggers a full resync of the exports. Other failures
are retried with backoff; while they last, GDS lets the lease expire after
TTL plus its skew allowance, which hides the cluster's endpoints from every
import until the heartbeats resume.

`--deregister-on-shutdown` (default false) deregisters the cluster when the
controller stops, which deletes its exports and pools in GDS. Leave it off
for normal restarts and upgrades; the lease covers a short absence.

## Exports

### ServiceExport reconciler

For every ServiceExport in a watched namespace, created by a chart (KEP
behaviour) or by auto-export:

1. Adds the finalizer `gds.nvidia.com/service-export`.
2. Reads the Service of the same name. No Service: `Valid=False`, reason
   `NoService`. Type ExternalName: `Valid=False`, reason
   `InvalidServiceType`. In both cases the GDS record is deleted once.
3. Builds the export spec: type `HEADLESS` for `clusterIP: None`,
   `CLUSTER_SET_IP` otherwise (NodePort and LoadBalancer Services export
   their pod endpoints); ports; session affinity and its ClientIP timeout;
   IP families; internal traffic policy; traffic distribution;
   `export_creation_timestamp` = the ServiceExport's creationTimestamp. The
   Service's `gds.nvidia.com/*` labels and annotations are forwarded as
   exported labels and annotations together with `spec.exportedLabels` and
   `spec.exportedAnnotations`, which win on the same key. The controller's
   own markers (`gds.nvidia.com/managed-by`, `gds.nvidia.com/clusterset`,
   `gds.nvidia.com/pool-id`) are not forwarded.
4. `PutServiceExport` when the hash of the spec changed since the last
   successful write.
5. Builds the endpoint set from the Service's EndpointSlices
   (`kubernetes.io/service-name=<name>`), skipping slices labelled
   `endpointslice.kubernetes.io/managed-by=gds-mcs-controller.gds.nvidia.com`
   (imports): per endpoint the addresses, the slice address type, `ready`
   (unset means ready), `serving` (unset follows ready), `terminating`,
   hostname, node name, zone, the slice ports, and the target reference as
   attributes `targetRef.kind`, `targetRef.name`, `targetRef.uid`. The set is
   sorted and de-duplicated, so its hash is stable. Above 1000 endpoints the
   ready ones are kept first and an `EndpointsTruncated` Event is emitted.
6. `ReplaceEndpoints` when the hash changed.
7. Writes the status: `Valid` locally; `Ready` and `Conflict` copied from the
   cluster status GDS reports for this cluster in the import (delivered by
   the importer). Before GDS reports, `Ready=Unknown`, reason `Pending`. A
   write GDS rejects as invalid sets `Ready=False`, reason `Failed`, and an
   `ExportFailed` Event.

Generations: each export has a spec and an endpoint generation,
`max(previous + 1, unix milliseconds)` (`client.Generation`), monotonic
across restarts and GDS replicas. A write refused with
`FAILED_PRECONDITION STALE_GENERATION` is retried once with a generation
above the `current_generation` GDS returned.

Writes need a registered cluster: while the lease is not registered the
export waits (`Ready=Unknown`, retried every 2 s), and a write refused with
`CLUSTER_NOT_REGISTERED` wakes the lease.

Deleting a ServiceExport deletes the GDS record (`DeleteServiceExport`)
before the finalizer is removed, so while GDS is unreachable a deletion
waits. Removing the finalizer by hand skips that; the next resync deletes
the record.

### Resync and garbage collection

At startup (once the cluster is registered and the caches are synced),
after every reconnect of the GDS client and after every re-registration,
the leader:

1. forgets which hashes it wrote (generations are kept);
2. lists this cluster's exports in GDS (`ListServiceExports`) and deletes
   every record without a ServiceExport in a watched namespace;
3. reconciles every ServiceExport, so each spec and endpoint set is written
   again.

The controller owns every GDS export record of its cluster id. Run exactly
one controller per cluster id; records in namespaces that are no longer
watched are deleted.

### Label-driven export

A Service labelled `gds.nvidia.com/export: "true"` gets a ServiceExport of
the same name with an owner reference to the Service and the labels
`gds.nvidia.com/managed-by: gds-mcs-controller` and
`gds.nvidia.com/clusterset: <clusterset>`. Removing the label deletes that
ServiceExport; deleting the Service deletes it through the owner
reference. A ServiceExport without the managed-by label is never created,
changed or deleted by the controller.

### Pool exports

With `--auto-export-pools` (default true) the leader watches the pools of
its own cluster (`client.PoolCache`, list plus watch plus a re-list every
30 s). For every pool registered through the Pool API (pools with a lease;
label-only pools already have their exports) it keeps a ServiceExport per
entry of `services[]`, labelled `gds.nvidia.com/pool-id: <pool id>` and
managed-by. The exports stay while the pool exists in any state but GONE:
REGISTERED, ACTIVE, DRAINING, and also UNHEALTHY, since removing the export
would keep the pool from ever becoming healthy. A draining pool therefore
keeps its import and derived VIP in every cluster until traffic moved. When
the pool is GONE or deleted, or no longer lists a Service, the controller
deletes the exports it labelled; if the Service is also labelled for
export, only the pool label is removed. When two pools list the same
Service, the export carries the smaller pool id. Nothing is removed before the first pool list or while the cache
is stale.

## Imports

The leader holds one `WatchServiceImports` stream with endpoints (with a
namespace filter when exactly one namespace is watched). `SNAPSHOT_START`
clears the local map of imports, `UPSERT` and `DELETE` update it, and
`SNAPSHOT_END` garbage-collects. Every change is applied by the import
controller with server-side apply, field manager `gds-mcs-controller`:

| Object | Name | Content |
| :---- | :---- | :---- |
| ServiceImport | `<name>` | `spec.type` (ClusterSetIP or Headless), merged ports, session affinity and config, IP families, `spec.ips` = the derived Service's ClusterIPs (at most two, none for Headless); exported labels and annotations; labels `gds.nvidia.com/managed-by`, `gds.nvidia.com/clusterset`; annotation `multicluster.kubernetes.io/derived-service`; `status.clusters` = the participating clusters; conditions `Ready` and `Conflict` (the import-level conflict) |
| Derived Service | `derived-<hash>` (`api.DerivedServiceName`) | No selector; type ClusterIP; `clusterIP: None` for Headless; merged ports; session affinity; IP families (SingleStack for one, PreferDualStack for two); labels managed-by, clusterset and `multicluster.kubernetes.io/service-name`; owner reference to the ServiceImport |
| EndpointSlices | `<derived>-<cluster>-<i>` | One set per source cluster, the local one included, at most `--endpoints-per-slice` endpoints each, grouped by address type and port set; ports filtered to the port names of the merged import; node names kept for local endpoints only; labels `kubernetes.io/service-name=<derived>`, `multicluster.kubernetes.io/service-name=<name>`, `multicluster.kubernetes.io/source-cluster=<cluster>`, `endpointslice.kubernetes.io/managed-by=gds-mcs-controller.gds.nvidia.com`, managed-by and clusterset; owner reference to the ServiceImport |

Details:

- The status is written with a merge patch of the whole status; the spec
  and metadata by server-side apply with forced ownership.
- A ServiceImport without the managed-by label belongs to another MCS
  implementation and is skipped, unless `--adopt-unmanaged-imports`.
- A Service named like the derived Service but not managed by the
  controller blocks the import (the reconcile fails and retries).
- A change between ClusterSetIP and Headless deletes and re-creates the
  derived Service, since `clusterIP` is immutable.
- Slices of a source cluster that left the import, or beyond the needed
  count, are deleted.
- A `DELETE` event deletes the ServiceImport, the derived Service and the
  slices explicitly; owner references would cascade too, but the controller
  does not depend on the garbage collector.
- After `SNAPSHOT_END` every import that has managed objects in the cluster
  (ServiceImports, derived Services or slices) but none in GDS is deleted the
  same way. This covers imports deleted while the controller was down or
  disconnected. Before `SNAPSHOT_END` nothing is deleted for a missing
  import, so a reconnect never flaps imports.
- Deleting a managed object by hand re-creates it.
- This cluster's status of every import (`clusters[]` entry of the cluster
  id) goes to the ServiceExport reconciler, which copies `Ready` and
  `Conflict` onto the ServiceExport.

### Pending namespaces

Namespace sameness means an import lands in the namespace of the exported
Service. When that namespace does not exist (or is terminating) the import
is held pending, counted by `nvcf_gds_mcs_controller_imports_pending_namespace`,
and applied when the Namespace informer sees the namespace created. A
re-created namespace gets all its imports again. `--create-namespaces`
creates a missing namespace instead (labelled managed-by and clusterset);
the controller never deletes namespaces.

## Labels, annotations and finalizer

| Key | On | Value |
| :---- | :---- | :---- |
| `gds.nvidia.com/export` | Service | `"true"` enables label-driven export |
| `gds.nvidia.com/role`, `gds.nvidia.com/pool` and the `gds.nvidia.com/*` annotations | Service | Forwarded to GDS as exported labels and annotations |
| `gds.nvidia.com/managed-by` | ServiceExports, ServiceImports, derived Services, slices, namespaces the controller creates | `gds-mcs-controller` |
| `gds.nvidia.com/clusterset` | same | the clusterset |
| `gds.nvidia.com/pool-id` | pool ServiceExports | the pool id |
| `gds.nvidia.com/service-export` | ServiceExport finalizer | |
| `endpointslice.kubernetes.io/managed-by` | imported slices | `gds-mcs-controller.gds.nvidia.com` |
| `multicluster.kubernetes.io/service-name`, `multicluster.kubernetes.io/source-cluster` | imported slices | import name, source cluster |
| `multicluster.kubernetes.io/derived-service` | ServiceImport annotation | derived Service name |

## Conditions

| Object | Type | Status and reason |
| :---- | :---- | :---- |
| ServiceExport | `Valid` | True/`Valid`; False/`NoService`; False/`InvalidServiceType` |
| ServiceExport | `Ready` | Copied from GDS (True/`Ready`; False/`ClusterLeaseExpired`); Unknown/`Pending` before GDS reports or while the cluster is not registered; False/`Failed` when GDS rejects the export or it is invalid |
| ServiceExport | `Conflict` | Copied from GDS: False/`NoConflicts`, True with the mcs-api reasons (`PortConflict`, `TypeConflict`, ...; several joined by commas) or `IPFamilyConflict`; Unknown/`ClusterLeaseExpired` |
| ServiceImport | `Ready` | True/`Ready` with the count of participating clusters |
| ServiceImport | `Conflict` | The import-level conflict: False/`NoConflicts` or True with its reason |

## Reachability prerequisite

A ClusterSetIP import works only when the pod IPs of every source cluster
are routable from the importing cluster: on EKS, VPC peering (or a transit
gateway) between the cluster VPCs, non-overlapping pod CIDRs (the VPC CNI
uses VPC addresses), and security groups that allow the importing
cluster's pod CIDR to the exported ports. kube-proxy DNATs the derived
ClusterIP to remote pod IPs on the client's node; there is no extra hop.
The same holds for headless imports and for router seeds. The controller
does not detect routability. Where pod IPs are not routable, front the
workload with a LoadBalancer and publish its address as a pool frontend URL
instead of relying on the import.

Health propagation across clusters takes roughly 3 to 6 s (EndpointSlice
change, `ReplaceEndpoints`, replication, refresh, watch, apply), so
clients of a derived VIP keep retrying another endpoint on failure.

## DNS: CoreDNS multicluster plugin (optional)

Clients compute the derived Service name and dial
`derived-<hash>.<namespace>.svc.cluster.local`; no extra DNS is needed. For
`<service>.<namespace>.svc.clusterset.local` names, add the CoreDNS
[multicluster](https://github.com/coredns/multicluster) plugin. It answers
from ServiceImports (`spec.ips` for ClusterSetIP) and from the slices
labelled `multicluster.kubernetes.io/service-name` (Headless), both of
which the controller writes. Stock CoreDNS images (EKS, k3s) do not
include the plugin: build CoreDNS with `multicluster:github.com/coredns/multicluster`
added to `plugin.cfg`, before `kubernetes`.

Corefile stanza, inside the existing `.:53` server block:

```text
multicluster clusterset.local {
    fallthrough
}
```

CoreDNS also needs to list and watch the MCS objects:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: coredns-multicluster
rules:
  - apiGroups: ["multicluster.x-k8s.io"]
    resources: ["serviceimports"]
    verbs: ["list", "watch"]
  - apiGroups: ["discovery.k8s.io"]
    resources: ["endpointslices"]
    verbs: ["list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: coredns-multicluster
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: coredns-multicluster
subjects:
  - kind: ServiceAccount
    name: coredns
    namespace: kube-system
```

## Flags

| Flag | Default | Meaning |
| :---- | :---- | :---- |
| `--gds-url` | `$GDS_URLS` | GDS URLs in failover order; repeatable or comma-separated; required |
| `--gds-tls-ca-file` | system roots | CA bundle of the GDS certificates |
| `--gds-allow-insecure-http` | false | Permit `http://` URLs |
| `--clusterset-id`, `--cluster-id` | ClusterProperty | KEP-1645 identifiers, DNS labels |
| `--cluster-identity-source` | `auto` | `auto`, `flags` or `clusterproperty` |
| `--region` | node label | Region of the registration |
| `--cluster-labels` | none | `k=v,...` labels of the registration |
| `--lease-ttl` | `30s` | Cluster lease TTL (10 s to 5 m); heartbeat every TTL/3 |
| `--watch-namespaces` | all | Namespaces exported from and imported into |
| `--create-namespaces` | false | Create missing import namespaces |
| `--adopt-unmanaged-imports` | false | Take over ServiceImports of other implementations |
| `--auto-export-pools` | true | Pool half of auto-export |
| `--write-cluster-properties` | false | Write the ClusterProperty objects |
| `--deregister-on-shutdown` | false | Deregister the cluster on stop (deletes its exports in GDS) |
| `--endpoints-per-slice` | `100` | Endpoints per imported EndpointSlice (1 to 1000) |
| `--max-concurrent-reconciles` | `4` | Workers per controller |
| `--metrics-bind-address` | `:8080` | `/metrics`; `0` disables |
| `--health-probe-bind-address` | `:8081` | `/healthz`, `/readyz` |
| `--leader-elect` | true | Leader election; required for more than one replica |
| `--operator-namespace` | `$POD_NAMESPACE` | Namespace of the leader election Lease |
| `--otel-enabled` | false | OTLP tracing configured by the `OTEL_*` environment |
| `--zap-*` | | controller-runtime logging flags, for example `--zap-log-level=debug` |

Readiness (`/readyz`): the GDS client connected at least once and, on the
leader, the cluster is registered. Liveness (`/healthz`) is a ping.

## RBAC

`config/rbac/role.yaml` is the ClusterRole generated from the
`+kubebuilder:rbac` markers (`make codegen-update`):

| Resource | Verbs | Why |
| :---- | :---- | :---- |
| `serviceexports` | get, list, watch, update, patch; create, delete | Reconciler; auto-export creates and deletes |
| `serviceexports/status`, `serviceexports/finalizers` | get, update, patch; update | Conditions, finalizer |
| `serviceimports`, `serviceimports/status` | full; get, update, patch | Imports |
| `services`, `endpointslices` | full | Export sources, derived Services, imported slices |
| `namespaces` | get, list, watch; create | Pending imports; `--create-namespaces` |
| `nodes` | get, list, watch | Region and zones |
| `clusterproperties` (`about.k8s.io`) | get, list, watch; create, update, patch | Identity; `--write-cluster-properties` |
| `events` (core and `events.k8s.io`) | create, patch | Events on ServiceExports |

The chart drops `namespaces` create and the ClusterProperty writes when
their flags are off, and adds a Role for `coordination.k8s.io` leases (get,
list, watch, create, update, patch, delete) in the controller's namespace
for leader election.

## Metrics

`nvcf_gds_mcs_controller_*`, counters pre-initialised:

| Metric | Type | Labels | Meaning |
| :---- | :---- | :---- | :---- |
| `exports` | gauge | `state` (exported, pending, invalid, failed) | ServiceExports by state |
| `reconcile_duration_seconds` | histogram | `controller` (serviceexport, autoexport, serviceimport, poolexport) | Reconcile duration |
| `reconciles_total` | counter | `controller`, `result` (success, error) | Reconciles |
| `export_writes_total` | counter | `op` (put_export, delete_export), `result` (success, stale_generation, not_registered, error) | Export writes |
| `endpoint_replacements_total` | counter | `result` | `ReplaceEndpoints` calls |
| `exported_endpoints` | gauge | | Endpoints of all exports as last written |
| `imports` | gauge | | Imports in the controller's view |
| `import_events_total` | counter | `type` (snapshot_start, upsert, delete, snapshot_end, bookmark) | Watch events |
| `imports_pending_namespace` | gauge | | Imports waiting for their namespace |
| `stream_reconnects_total` | counter | | GDS client reconnects |
| `resyncs_total` | counter | `reason` (startup, reconnect, reregister, snapshot) | Full resyncs |
| `gc_deletions_total` | counter | `kind` (gds_export, service_import, service, endpoint_slice) | Objects deleted because their source is gone |
| `heartbeat_failures_total` | counter | `reason` (epoch_mismatch, not_registered, error) | Failed heartbeats |
| `registrations_total` | counter | `result` | `RegisterCluster` calls |
| `cluster_registered` | gauge | | 1 while this replica holds the lease |
| `pool_exports` | gauge | | ServiceExports kept for pools |

The GDS client adds `nvcf_gds_client_*` (`pkg/client/README.md`) and
controller-runtime its `controller_runtime_*` and `workqueue_*` metrics.

## Logs and traces

Logs are controller-runtime zap, JSON by default (`--zap-devel` switches
to the console encoder), with
`clusterset` and `cluster` on every component logger and the controller
name, namespace and name on reconcile logs. With `--otel-enabled` every GDS
call gets an otelgrpc client span with W3C propagation, inside the
controller spans `gds-mcs-controller.register_cluster`,
`heartbeat_cluster`, `put_service_export`, `replace_endpoints`,
`delete_service_export` and `apply_service_import`, which carry
`gds.clusterset`, `nvcf.cluster.id`, `k8s.namespace.name` and
`gds.service.name`, and `error=true` on failure.

## Run and test

```bash
# Unit tests (fake Kubernetes client and pkg/fakegds), part of make test:
GOWORK=off go test -race ./internal/mcs/... ./cmd/gds-mcs-controller/...
# envtest suite against kube-apiserver and etcd:
make envtest-assets   # downloads them with setup-envtest into _output/envtest
make test-envtest
# Out of cluster against the current kubeconfig and a local gds-server:
kubectl apply -f "$(GOWORK=off go list -m -f '{{.Dir}}' sigs.k8s.io/mcs-api)/config/crd"
kubectl apply -f config/crd/bases
GOWORK=off go run ./cmd/gds-mcs-controller --gds-url=http://localhost:9090 \
  --gds-allow-insecure-http --clusterset-id=dev --cluster-id=a --region=local \
  --leader-elect=false --metrics-bind-address=:18080 --health-probe-bind-address=:18081
```

`config/crd` embeds the three CRDs
(`multicluster.x-k8s.io_serviceexports.yaml` and
`multicluster.x-k8s.io_serviceimports.yaml` from sigs.k8s.io/mcs-api v0.5.2,
and `bases/about.k8s.io_clusterproperties.yaml`); the envtest suite installs
them from there. The ClusterProperty CRD carries the
`api-approved.kubernetes.io` annotation that the protected `about.k8s.io`
group requires.

The envtest suite covers: an export creating `PutServiceExport` and
`ReplaceEndpoints`; scaling replacing the endpoint set with a higher
generation; this cluster's conditions on the ServiceExport; deleting a
ServiceExport calling `DeleteServiceExport` and removing the finalizer; an
import creating the ServiceImport, the derived Service with `spec.ips`, and
slices with their labels and owner references; a `DELETE` cascading; a
snapshot after reconnecting to a new replica collecting an orphan import; a
namespace created later receiving its pending import; label-driven export
and its removal.
