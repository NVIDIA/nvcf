# SDD: Storage-Agnostic Model Cache Architecture

## Summary

New model-cache requests select storage from two cluster objects:

- `StorageClass/nvcf-sc` identifies the installed CSI provisioner.
- ConfigMap `nvcf-storage-capabilities` maps that exact provisioner to an NVCA transition.

NVCA records the selection on the `ICMSRequest`, creates an immutable `ModelCacheBinding`, and adds the exact request
UID to the binding before it creates cache storage. Runtime code then uses the recorded binding. It does not select a
different durable provider after a retry, restart, feature-gate change, or catalog change.

Only the catalog-registered durable transition `nvmesh` is executable. Weka, OCI File Storage (FSS), and OCI Lustre
remain disabled for new annotated requests in both model-cache workflows. Their catalog entries do not enable cache
traffic.

This change also sets Kubernetes read-only intent on the PVC volume source and every model-cache mount. Automated
tests prove the generated Kubernetes objects. They do not prove backend write denial, multi-Pod mounts, data identity,
restart behavior, or performance. Those require functional qualification on the target storage system.

## Table of contents

- [Scope](#scope)
- [Current support](#current-support)
- [Cluster configuration](#cluster-configuration)
- [Runtime architecture](#runtime-architecture)
- [Regular model cache](#regular-model-cache)
- [Helm model cache](#helm-model-cache)
- [Encryption](#encryption)
- [Failure and cleanup rules](#failure-and-cleanup-rules)
- [Compatibility and limitations](#compatibility-and-limitations)
- [Test contract](#test-contract)
- [Provider enablement](#provider-enablement)
- [Rollout and rollback](#rollout-and-rollback)
- [Public source references](#public-source-references)

## Scope

This design covers regular container model cache and Helm/MiniService model cache. It includes provider selection,
binding ownership, NVMesh execution, read-only Kubernetes intent, cleanup guards, and the contract for adding another
CSI provider.

It excludes container cache, function storage, internal storage, database storage, CSI driver installation, and
performance qualification.

## Current support

| Provisioner | Catalog access modes | Regular transition | Helm transition |
|---|---|---|---|
| `nvmesh-csi.excelero.com` | RWO, ROX | `nvmesh` | `nvmesh` |
| `csi.weka.io` | RWX, ROX | `disabled` | `disabled` |
| `fss.csi.oraclecloud.com` | RWX | `disabled` | `disabled` |
| `lustre.csi.oraclecloud.com` | None recorded | `disabled` | `disabled` |

RWO means `ReadWriteOnce`, ROX means `ReadOnlyMany`, and RWX means `ReadWriteMany`.

Catalog access modes record PVC modes exercised for an exact provider configuration. They do not prove an NVCF
model-cache transition. In particular, a read-only Pod mount of an RWX claim is not ROX evidence.

## Cluster configuration

### StorageClass contract

Deployment tooling owns provider-specific CSI parameters. NVCA requires:

- the class name is `nvcf-sc`;
- `provisioner` is non-empty;
- `reclaimPolicy` is `Retain`;
- exactly one provider supplies the class.

The StorageClass digest covers provisioner, sorted parameters, reclaim policy, volume binding mode, mount options, and
allowed topologies. It excludes metadata and `allowVolumeExpansion`. Expansion remains a deployment setting. NVMesh
deployment configuration may keep `allowVolumeExpansion: true` without changing NVCA selection.

### Capability catalog

The operator chart installs `nvcf-storage-capabilities`. The operator mirrors it into each NVCA agent namespace. Data
key `storage-provider-capabilities.yaml` contains the public catalog.

Minimal shape:

```yaml
apiVersion: storage.nvcf.nvidia.com/v1alpha1
kind: StorageCapabilityCatalog
drivers:
  <exact-csi-provisioner>:
    provider: <provider-id>
    accessModes:
      - <qualified-pvc-access-mode>
    transitions:
      regularModelCache: <registered-transition-or-disabled>
      helmModelCache: <registered-transition-or-disabled>
```

The provisioner is the lookup key. `provider` is an identifier for logs and persisted state. A transition names
executable NVCA code. `disabled` is the only disabled value.

The parser rejects unknown fields, unknown access modes, duplicate modes, unknown transitions, and transition/provider
mismatches. Tests parse the shipped catalog and validate the shipped JSON Schema.

The catalog is not a general CSI capability matrix. It does not record expansion, snapshots, clones, performance, or
topology support.

### Selection outcomes

For a cache request, NVCA evaluates `CachingSupport`, the Helm-only `HelmModelCaching` sub-gate, `nvcf-sc`, and the
catalog once, before it creates the `ICMSRequest`.

| Condition | Regular model cache | Helm model cache |
|---|---|---|
| Caching gate is off | `none` | `none` |
| `nvcf-sc` is absent | `none` | `ephemeral` |
| Selected workflow transition is `disabled` | `none` | `ephemeral` |
| Transition is `nvmesh` | `durable` | `durable` |
| Catalog is invalid or provisioner is unknown | Request creation fails | Request creation fails |
| `nvcf-sc` is not `Retain` | Request creation fails | Request creation fails |

NVCA never falls through to a second durable provider.

## Runtime architecture

```text
ICMS cache request
  -> persisted selection annotation
  -> persisted NVCA request finalizer
  -> Active ModelCacheBinding with exact request UID reference
  -> binding UID labels on cache resources
  -> provider transition
  -> read-only workload volume and mounts
```

### Persisted request selection

Annotation `nvca.nvcf.nvidia.io/model-cache-storage-selection` records:

- workflow and mode;
- StorageClass name, UID, and configuration digest;
- catalog payload digest;
- provider, provisioner, transition, and required access modes;
- the NVMesh encryption decision;
- binding name and API-assigned UID after the binding is committed.

The annotation is strict JSON. Runtime validation rejects unknown fields, partial durable state, workflow changes, an
incomplete binding reference, and unsupported transitions.

Immediately before first binding creation, NVCA revalidates the live StorageClass and exact catalog payload. A changed
UID, provisioner, `Retain` policy, StorageClass digest, catalog digest, provider, or transition fails before a storage
side effect. Transient API errors requeue.

After binding creation, the binding is authoritative. NVCA does not reselect from a later catalog revision. If a
binding-owned unencrypted writer PVC is missing and must be dynamically provisioned again, the live `nvcf-sc` must
still match the binding's StorageClass snapshot. Existing bound objects and static reader objects do not require that
live lookup.

### ModelCacheBinding

`ModelCacheBinding` is a namespaced `v2beta1` API object in `nvca-modelcache-init`.

| Area | Recorded data |
|---|---|
| Identity | Version, workflow, sharing-domain digest, cache-handle digest |
| Decision | Provider, provisioner, transition, required access modes, catalog digest, encryption decision |
| StorageClass | Name, UID, `Retain`, configuration digest |
| Resource intent | Writer namespace, deterministic PVC and Job names, optional Lease, encrypted class and Secret names |
| Lifecycle | `Active` or `Retiring`, exact request namespace/name/UID references, finalizer |

The API server rejects `spec` mutation. It also rejects a transition from `Retiring` back to `Active` and a change to a
recorded provider data identity.

Regular failure cleanup changes an `Active` binding to `Retiring` only when the exact request is its sole reference.
`Retiring` blocks new references and normal runtime. That same regular request may resume interrupted cleanup.
General zero-reference retirement, data garbage collection, binding deletion, `status.realized`, and conditions are not
implemented. A zero-reference binding stays `Active`, and legacy idle garbage collection skips binding-owned cache data.

Binding creation order:

1. Persist the general NVCA finalizer on the `ICMSRequest` and stop that reconcile.
2. Build the deterministic binding intent.
3. If no binding exists, revalidate live selection inputs and create it.
4. Initialize it as `Active` and add the exact request UID reference.
5. Persist the binding name and UID in the request selection and stop that reconcile.
6. Re-read the binding, require exact immutable intent, `Active`, finalizer, UID, and request reference.
7. Start provider side effects.

Before this request persists a binding reference, NVCA may reuse an exact Active binding and add the request reference,
or recover an exact newly created binding whose status is completely empty. After the request records the binding name
and UID, normal runtime retries require the exact UID, finalizer, immutable spec, Active phase, and request reference.
Partially initialized status, Retiring runtime use, new joins to Retiring bindings, and immutable collisions fail
closed. The exact sole regular request may resume cleanup against its Retiring binding. A stale same-name request
reference is replaced only after the old request UID is absent. If request deletion starts while the reference is being
committed, NVCA removes the newly added reference.

The current binding name hashes only the cache handle because existing NVMesh resource names are handle-scoped. The
same handle in another workflow or sharing domain collides and fails closed. A future resource-naming migration is
required before those domains can use independent bindings for the same handle.

## Regular model cache

The regular path runs in the Pod instance namespace.

1. Create `rw-pvc-<handle>` with RWO and `writer-job-<handle>`.
2. Wait for population and volume detachment.
3. Set the retained PV to ROX and bind `ro-pvc-<handle>` to that PV.
4. Mount the reader PVC with `PersistentVolumeClaimVolumeSource.readOnly: true`.
5. Set every matching init-container and container `volumeMount.readOnly: true`.

The regular path has no Kubernetes Lease. Its mutex serializes setup only within one NVCA process. Deterministic names
detect collisions; they do not elect one writer across the cluster. The binding UID labels the writer PVC, Job, Job Pod
template, retained PV, and reader PVC. Existing same-name PVCs and Jobs require the exact binding UID and immutable
intent. Other same-name objects are not adopted.

Before any destructive failure cleanup, NVCA atomically changes the binding from `Active` to `Retiring` if the exact
request is its sole reference. A concurrent reference prevents retirement and cleanup. The same request can resume
interrupted cleanup while the binding remains `Retiring`. Cleanup validates exact resource names, immutable PVC and Job
intent, claim ownership, and binding UID before each destructive change. Legacy periodic reader-PVC garbage collection
skips binding-owned PVCs.

## Helm model cache

The Helm writer runs in `nvca-modelcache-init`.

1. A Lease named from the cache handle elects one writer request.
2. The writer populates `rw-pvc-<handle>` through `writer-job-<handle>`.
3. NVCA retains the primary PV as the cache data identity.
4. Each StorageRequest creates its namespace-local ROX PVC and secondary PV for the same NVMesh data identity.
5. The webhook sets the PVC volume source and every mount that references the model-cache volume read-only.

The binding UID labels the StorageRequest, writer PVC, Job and Pod template, pull Secrets, Lease, primary PV, secondary
PV, and reader PVC. The secondary PV and reader PVC also carry
`nvca.nvcf.nvidia.io/model-cache-request-uid`, which rejects a stale same-name reader from an earlier ICMSRequest
generation. Shared writer objects do not carry a request UID label because multiple requests can share them. The
StorageRequest records the source UID in annotation `nvca.nvcf.nvidia.io/icms-request-uid`. Existing writer objects and
Leases with another or missing binding UID fail closed. An existing StorageRequest must match the exact persisted
selection and source ICMSRequest UID. Same-binding PVC, Job, Lease, and pull Secret adoption also requires immutable
intent to match before any object is created.

Annotated cleanup validates the Active binding and exact per-request reader inventory before deletion. While the exact
binding reference is present, only a Lease holder containing the exact request UID may delete shared writer artifacts.
Before each shared-writer deletion, cleanup revalidates the Lease UID, resourceVersion, binding UID, and holder. After
that reference is released, the StorageRequest identity is a tombstone only if the exact ICMSRequest is deleting or
absent. The tombstone authorizes deletion of that request's reader PV/PVC only; shared writer cleanup is skipped. A live
unreferenced request, a same-name request with another UID, or a request-read error stops cleanup. Legacy idle GC skips
binding-owned primary PVs and their encrypted StorageClasses.

## Encryption

Encryption is part of the durable selection and binding decision. A later feature-gate change does not change it.

NVMesh encryption uses existing sharing-domain-scoped resources:

- regular: StorageClass `<domain-hash>-sc` and Secret `<domain-hash>`;
- Helm: StorageClass `sc-<domain-hash>` and Secret `scsec-<domain-hash>`.

The derived StorageClass must match the expected NVMesh provisioner, `Retain`, binding mode, expansion setting, and
parameters. The Secret must contain a non-empty `dmcryptKey`. Existing conflicting objects fail closed, and existing
key material is preserved.

These objects are shared by multiple bindings in one domain, so they do not carry one binding UID. The binding records
their deterministic names, never Secret contents.

## Failure and cleanup rules

| Event | Result for a new persisted request |
|---|---|
| Feature gate changes after selection | Recorded mode remains authoritative |
| StorageClass or catalog drifts before binding creation | Terminal failure, no binding or cache object |
| Catalog changes after binding creation | Existing binding remains authoritative |
| Missing writer requires dynamic provisioning after class replacement | Terminal failure before PVC creation |
| Runtime binding is missing, replaced, `Retiring`, or lacks the exact request reference | Terminal failure; the exact sole regular request may resume cleanup against `Retiring`, and Helm cleanup has the validated tombstone exception |
| Object has missing or foreign ownership | Never adopt or delete that object; runtime fails terminal and cleanup refuses that target |
| Missing unencrypted writer before initialization | Recreate only after live `nvcf-sc` matches the binding snapshot |
| An artifact that should already exist in the persisted runtime phase is missing or invalid | Terminal failure |
| Deterministic selection, binding, or ownership data does not match | Terminal failure before the conflicting object is used or changed |
| Cleanup target is already absent | Idempotent success |
| Durable cache execution reports failure | Terminal failure, no uncached fallback |
| Transient Kubernetes API error | Requeue without changing selection or phase |
| Forbidden, Unauthorized, Invalid, or Gone API response | Surface the API error without converting deterministic state to a terminal cache failure |
| Annotation-free legacy request fails caching | Existing legacy fallback behavior remains |

On request deletion, NVCA waits until instances are terminated, removes the exact request UID reference from the
binding, and then removes its general request finalizer. Reference removal is idempotent.

Request references are live references, not tombstones. `ModelCacheBinding` has no tombstone field. Helm
StorageRequest cleanup may run after reference release and then uses the persisted request identity described above.
Zero references do not trigger `Retiring`, binding deletion, or cache-data deletion.

Annotated Helm cleanup deletes validated snapshots with UID and resourceVersion preconditions. Binding-scoped regular
Job and PVC deletes use UID and resourceVersion preconditions.

This change does not delete a zero-reference binding or its retained cache data. Operator uninstall explicitly removes
binding finalizers before deleting the model-cache control namespace.

Configuration drift never authorizes data deletion.

## Compatibility and limitations

Requests with no storage-selection annotation are legacy requests. They keep existing feature-gated regular behavior
and existing Helm backend state. When no Helm StorageRequest exists, the compatibility selector may still inspect the
legacy NVMesh marker, shared-filesystem marker, or Samba configuration. New annotated requests do not use those
presence checks.

There is no automatic conversion of legacy cache objects into bindings. In particular, NVCA does not adopt an
unlabeled legacy PVC, PV, Job, Secret, or Lease into a new binding.

Current limitations:

- only NVMesh has executable regular and Helm transitions;
- regular writer serialization is not a cluster-wide Lease;
- binding names cannot separate the same handle across workflows or sharing domains;
- realized state and binding conditions are not populated;
- only regular sole-request failure cleanup marks a binding `Retiring`;
- no general retirement or provider-data garbage-collection controller exists;
- no provider replacement controller exists;
- binding-specific metrics are not implemented;
- backend functional qualification is outside the unit test suite.

Do not replace `nvcf-sc` while Active bindings may need to provision a writer. Stop new requests and drain the affected
cache state first. General drain, zero-reference retirement, and retained-data garbage collection remain future work.

## Test contract

### Automated tests in this change

- strict catalog and annotation parsing;
- exact provisioner lookup and workflow transition selection;
- `Retain`, UID, StorageClass digest, and catalog digest checks;
- missing class, unknown provider, disabled transition, and feature-gate outcomes;
- binding API schema, immutable spec, status subresource, and generated clients;
- binding create, retry adoption, collision, request-reference add/release, and drift handling;
- finalizer-before-side-effect ordering;
- regular and Helm binding UID propagation, Helm request UID propagation, and stale-generation rejection;
- sole-reference retirement, concurrent-join refusal, and interrupted regular cleanup retry;
- immutable regular and Helm writer-object adoption, drift refusal, and create-race validation;
- transient and non-transient Kubernetes API error classification;
- Helm tombstone authorization and refusal, including reader-only cleanup after reference release;
- Helm reader PV/PVC and regular Job/PVC UID and resourceVersion delete preconditions;
- binding-owned primary-PV and encrypted-StorageClass idle-GC exclusion with legacy idle GC preserved;
- encrypted StorageClass and Secret conflict detection;
- read-only PVC volume sources and every model-cache mount path;
- annotation-free compatibility behavior;
- operator catalog mirroring, RBAC, CRD installation, and uninstall cleanup.

These tests use fake clients, unit tests, and Kubernetes `envtest`. They prove control-plane decisions and generated
objects only.

### Required provider qualification

A workflow stays `disabled` until an exact provider configuration passes all of these tests:

1. Provision and mount every cataloged PVC access mode.
2. Populate one cache and prove every reader resolves the same provider data identity.
3. Mount from multiple Pods and from every required namespace pattern.
4. Run readers on eligible CPU and GPU nodes.
5. Compare full-file and tree checksums.
6. Verify `ro` in `/proc/self/mountinfo` inside each reader.
7. Deny create, append, rename, chmod, truncate, and delete from each reader.
8. Prove a same-provider baseline mount is writable by the test UID/GID.
9. Restart writer and readers, restart NVCA, and reschedule readers to another eligible node.
10. Inject provisioning, mount, writer, cancellation, and cleanup failures.
11. Retry every lifecycle step and verify no duplicate writer, leaked object, or data loss.
12. Prove cleanup cannot remove data while another reader uses it.
13. Prove the transition performs no source-sized clone or extra copy.

Record the CSI provisioner and images, backend version and configuration, StorageClass digest, Kubernetes version,
eligible node matrix, catalog version, date, and evidence reference. Performance testing follows functional acceptance.

## Provider enablement

1. Install the CSI driver and render exact `StorageClass/nvcf-sc` parameters through deployment tooling.
2. Add or update the public catalog entry with both workflows `disabled`.
3. Record only access modes proven on the exact configuration.
4. Implement a named regular or Helm transition. Do not infer a transition from access modes.
5. Add unit, failure, retry, ownership, cleanup, and read-only object tests.
6. Run the complete functional qualification above.
7. Enable only the passing workflow in the catalog.
8. Deploy transition code and the enabling catalog together.
9. Verify new requests persist the expected provisioner, transition, digests, and binding.

For Weka or OCI, the missing item is not StorageClass templating. It is a tested, provider-specific no-copy mapping
from one populated data identity to namespace-local read-only readers, plus its cleanup implementation.

## Rollout and rollback

Rollout order:

1. Install the `ModelCacheBinding` CRD and RBAC.
2. Install and mirror the catalog.
3. Deploy NVCA with persisted selection and binding support.
4. Verify one new NVMesh regular request and one new NVMesh Helm request.
5. Verify selection annotations, Active bindings, exact UID labels, read-only object intent, and cleanup refusal tests.
6. Keep every external transition disabled until qualification passes.

Existing annotation-free requests continue through compatibility code. New annotated requests must not be downgraded
to an NVCA version that does not understand bindings while they are active. Drain those requests before rollback. The
`Retain` policy preserves backend data, but it does not migrate or make an older controller binding-aware.

## Public source references

- [Storage catalog](https://github.com/NVIDIA/nvcf/blob/main/src/compute-plane-services/nvca/deployments/nvca-operator/files/nvcf-storage-capabilities-v1alpha1.yaml)
- [Catalog JSON Schema](https://github.com/NVIDIA/nvcf/blob/main/src/compute-plane-services/nvca/deployments/nvca-operator/files/nvcf-storage-capabilities-v1alpha1.schema.json)
- [Catalog selection](https://github.com/NVIDIA/nvcf/blob/main/src/compute-plane-services/nvca/pkg/storage/storage_capabilities.go)
- [Persisted selection](https://github.com/NVIDIA/nvcf/blob/main/src/compute-plane-services/nvca/pkg/storage/modelcache_selection.go)
- [Binding API](https://github.com/NVIDIA/nvcf/blob/main/src/compute-plane-services/nvca/pkg/apis/nvca/v2beta1/modelcachebinding_types.go)
- [Binding lifecycle](https://github.com/NVIDIA/nvcf/blob/main/src/compute-plane-services/nvca/pkg/nvca/modelcache_binding.go)
- [Kubernetes persistent-volume access modes](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#access-modes)
- [Runtime integration issue](https://github.com/NVIDIA/nvcf/issues/1326)
