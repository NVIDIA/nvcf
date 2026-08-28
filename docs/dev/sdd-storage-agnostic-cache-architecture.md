# SDD: Storage-Agnostic Cache Architecture

## Summary

This document separates implemented foundation from target runtime behavior.

Implemented foundation:

- The NVCA Operator chart installs a public `v1alpha1` storage catalog and packages its JSON Schema.
- NVCA has a strict catalog loader and semantic validator. Tests call them; runtime reconciliation does not.
- Current public NVCA still uses legacy StorageClass-presence checks and feature flags for backend selection.
- Weka, OCI File Storage (FSS), and OCI Lustre model-cache transitions remain `disabled`.

Target:

1. Deployment tooling renders exactly one selected provider as `StorageClass/nvcf-sc`.
2. The public NVCA catalog declares qualified access modes and regular/Helm cache transitions.
3. NVCA resolves the live class and catalog entry, then persists a shared cache binding before storage side effects.
4. A provider transition remains disabled until its complete functional contract passes.

Deployment tooling owns CSI-specific StorageClass parameters. NVCA owns model-cache transitions. A provider name,
provisioner, or access mode alone never enables a workflow.

Editing the catalog ConfigMap does not enable a provider today. Runtime work is tracked in
[NVIDIA/nvcf#1326](https://github.com/NVIDIA/nvcf/issues/1326); this SDD defines the stable `nvcf-sc` target contract.

## Table of contents

- [Summary](#summary)
- [Scope](#scope)
- [Terms](#terms)
- [Deployment configuration](#deployment-configuration)
- [Storage catalog](#storage-catalog)
- [Target runtime design](#target-runtime-design)
- [Model-cache contract](#model-cache-contract)
- [Failure, migration, and rollback](#failure-migration-and-rollback)
- [Provider qualification](#provider-qualification)
- [Test plan](#test-plan)
- [Security and observability](#security-and-observability)
- [Implementation order](#implementation-order)
- [Public source references](#public-source-references)

## Scope

This design covers regular and Helm/MiniService model cache across managed and self-managed deployments, including
NVMesh, Weka, OCI FSS, OCI Lustre, future CSI providers, functional qualification, migration, and rollback.

It excludes container cache; function, internal, and database storage; `nvcf-function-storage-sc`; CSI driver
installation/lifecycle; and performance qualification, which follows functional acceptance.

## Terms

| Term | Meaning |
|---|---|
| Provider | Product or integration ID, such as `ociFss` or `weka` |
| Provisioner | Exact CSI string in `StorageClass.provisioner` |
| Access mode | Kubernetes PVC mode declared only after qualification for an exact provider configuration |
| Workflow | `regularModelCache` or `helmModelCache` |
| Transition | Named NVCA implementation that moves a cache from writer state to reusable reader state |
| Qualification record | Versioned evidence for the exact tested storage and cluster configuration |
| Sharing domain | Stable authorization and encryption scope, such as an NCA ID |
| Cache key | Tuple of workflow, sharing domain, and cache handle |
| Cache binding | Immutable provider and transition choice shared by all requests for one cache key |
| Request selection | Per-request mode and optional cache-binding reference persisted before side effects |
| Data identity | Provider-backed object containing one populated cache handle |
| Writer | Workload holding the binding Lease while it populates a data identity |
| Reader view | Provider-specific, namespace-local, read-only access to the same data identity |

`ReadOnlyMany` (ROX) is a PVC access mode. Mounting a `ReadWriteMany` (RWX) PVC with `readOnly: true` is not evidence
that the driver provisions or binds a ROX PVC. Neither condition alone proves backend-enforced write denial or
cross-namespace data identity.

`ReadWriteOnce` (RWO) limits a volume to one node, not one Pod or writer. Kubernetes access modes describe attachment
and mount intent; NVCA must separately serialize writers and test reader write denial.

## Deployment configuration

Managed input selects a provider through `spec.nvcfStorage.provider`, resolves
`spec.storage.drivers.<provider>.storageClasses.nvcf-sc`, and renders `StorageClass/nvcf-sc`.

The selected provider supplies its exact provisioner, parameters, mount options, binding mode, expansion setting,
and topology. Provider values must match the installed CSI driver.

The class name is always `nvcf-sc`, its reclaim policy is always `Retain`, and exactly one installed provider is
primary even when multiple CSI drivers coexist. Rendering rejects an empty provisioner, invalid class fields, unknown
provider fields, and provider input for `reclaimPolicy`.

When no provider is selected, managed output remains compatible with NVMesh. Self-managed deployments may use
different tooling, but NVCA sees the same live `StorageClass/nvcf-sc` contract.

NVMesh supports volume expansion, so its `nvcf-sc` definition and regression tests must preserve
`allowVolumeExpansion: true`. Expansion is deployment behavior, not a field in the NVCA catalog.

## Storage catalog

The chart installs ConfigMap `nvcf-storage-capabilities` in its release namespace. Data key
`storage-provider-capabilities.yaml` contains a `storage.nvcf.nvidia.com/v1alpha1` `StorageCapabilityCatalog`. Source
and release charts contain byte-identical catalog and schema files; rendering fails when the catalog file is absent.

### Minimal shape

```yaml
drivers:
  <exact-csi-provisioner>:
    provider: <provider-id>
    accessModes:
      - <qualified-kubernetes-access-mode>
    transitions:
      regularModelCache: <implemented-transition-or-disabled>
      helmModelCache: <implemented-transition-or-disabled>
```

This is a shape illustration, not a valid provider entry. Actual entries must use exact provisioner strings,
Kubernetes access-mode names, and registered transition names.

The exact provisioner is the lookup key. `provider` is a label, `accessModes` cites externally qualified PVC modes, and
each workflow names an implemented transition or `disabled`.

Transition code, not the flat access-mode list, defines state A to state B:

| Transition | Provisioner | Writer to reader contract |
|---|---|---|
| `disabled` | Any | No durable transition |
| `nvmesh` | `nvmesh-csi.excelero.com` | RWO writer to ROX reader views |

Adding a transition requires dispatcher code, schema and semantic validation, a declared writer-to-reader mode pair,
and workflow tests. Access modes only show which required modes were qualified.

`disabled` is the complete workflow-disabled state; a driver may still list qualified access modes. There is no
separate qualification field. Enabling a transition is a release decision allowed only after implementation and exact
workflow qualification; the schema cannot prove that evidence exists.

The catalog intentionally does not contain a general CSI capability matrix. StorageClass rendering and provider
documentation own expansion, snapshots, clones, topology, and other CSI settings.

Access-mode rules:

- Record only Kubernetes access modes exercised by functional evidence for the exact provider configuration.
- Do not infer ROX from a read-only Pod mount of an RWX claim.
- Do not infer RWX or ROX from CSI driver documentation alone.
- Do not infer cross-namespace sharing or backend write denial from an access mode.

NVMesh uses transition `nvmesh` for both regular and Helm model cache. Samba is not an NVMesh transition. Weka, OCI
FSS, and OCI Lustre transitions remain `disabled` until their exact workflow qualification passes.

The target catalog does not register a generic `sharedfs` transition. Current NVCA retains a legacy, presence-selected
`sharedfs` route. Separate dynamic PVCs may expose different data, so class or driver presence cannot qualify it.

### Validation contract

CI validates structure and required fields with the packaged JSON Schema; Helm only checks that the file exists. When
called, the Go loader independently performs strict decoding and semantic validation, including ID, access-mode,
transition, and provisioner-transition checks. The test plan covers every rejection case.

Catalog entries are configuration metadata, not credentials. The security requirements below govern their content.

### Current runtime gap

The loader has no runtime call site, and NVCA has no complete provider-neutral cache binding. Current requests persist
only a coarse backend value. Both gaps must be closed before the catalog can control runtime behavior. Until then, a
`disabled` catalog entry does not disable a legacy path selected by StorageClass presence.

For Helm caching, current public NVCA first requires `CachingSupport` and `HelmModelCaching`. It then checks the legacy
`nvcf-sc-30` marker, the `nvcf-miniservice-sc` sharedfs sentinel, and a gated Samba fallback with a usable backing
class; otherwise it selects the per-Pod ephemeral cache. These are compatibility paths, not target provider selection.
The presence-only sharedfs branch cannot prove a cross-namespace backend identity. The target reads only `nvcf-sc`,
finds the exact provisioner entry, and selects its recorded transition.

## Target runtime design

### Selection algorithm

For each request, NVCA must:

1. Evaluate the applicable feature gates and workflow. Persist `none` or `ephemeral` on the request when selected; do
   not create a durable binding.
2. For durable caching, derive `(workflow, sharingDomain, cacheHandle)` and read its binding from the model-cache
   control namespace.
3. If it is `Active`, use it. If it is `Retiring`, retry or record a supported request fallback; never rebind it.
4. If no binding exists, read `StorageClass/nvcf-sc`, require `Retain`, and strictly load the catalog.
5. Find the exact provisioner entry and require a non-disabled transition with its required access modes.
6. Create the binding by deterministic name and optimistic concurrency. A losing creator reads the winning binding.
7. Conditionally add the request namespace, name, and UID while the binding is `Active`. Then persist its name, UID,
   and a request finalizer before any storage side effect.
8. Re-read the binding, confirm the reference and request finalizer are present, and execute only its transition.

Unknown provisioners, invalid catalogs, non-`Retain` classes, and disabled transitions cannot select durable storage.
The only generic fallbacks are `none` and, where the workflow supports it, `ephemeral`; NVCA never switches to a
second durable provider.

### Release qualification

Provisioner matching selects code; it does not prove that a deployment was qualified. Before a transition is enabled,
dated evidence must identify the CSI provisioner and images, backend version/configuration, `nvcf-sc` digest,
Kubernetes version, eligible node/OS/architecture matrix, catalog/schema version, and evidence reference.

Some backend fields are not discoverable through Kubernetes or CSI. The release or deployment qualification process,
not NVCA, owns this record and verifies those fields. NVCA snapshots only live Kubernetes data it can read: the
StorageClass name, UID, provisioner, configuration digest, and catalog digest. That snapshot detects drift for a cache
binding; it does not prove the backend product or version.

Setting a transition to a non-disabled value is therefore an operator or release assertion that the qualification
record applies to the cluster. NVCA validates the catalog and live StorageClass, but cannot verify non-observable
fields.

### Cache binding and realized state

Recomputing per request could mix providers for one cache key. The binding is one durable Kubernetes object in the
model-cache control namespace; every namespace-local request for that key references it.

| Group | Required binding data |
|---|---|
| Identity | Version, workflow, sharing-domain digest, and cache-handle digest |
| Decision | Provider, provisioner, transition, required access modes, and catalog payload digest |
| StorageClass snapshot | Name, UID, `Retain`, and configuration digest |
| Resource intent | Deterministic names for NVCA-created PVCs, static PVs, Jobs, and Leases |
| Lifecycle | `Active` or `Retiring`, request namespace/name/UID references, and finalizer |
| Realized state | Bound PV and provider data identity plus population state as they become known |

The target API is a namespaced `ModelCacheBinding` in the model-cache control namespace. Its immutable `spec` contains
identity, decision, StorageClass snapshot, and shared resource intent. Its `status` contains lifecycle, request
references, realized state, and conditions; a finalizer protects cleanup. `StorageRequest.status.modelCache` gains an
immutable request selection containing mode, binding name/UID, and deterministic namespace-local reader intent, plus a
request finalizer. The UID is the API-assigned `ModelCacheBinding.metadata.uid`; it is not part of binding `spec`. API
validation rejects binding-spec or request-selection mutation after persistence.

The versioned StorageClass digest is SHA-256 over canonical JSON containing provisioner, sorted parameters, reclaim
policy, binding mode, mount options, and allowed topologies; list order is preserved. It excludes object metadata and
volume expansion. The catalog digest is SHA-256 over the exact ConfigMap payload. The request records mode `none`,
`ephemeral`, or `durable` and the binding reference when durable.

Binding rules:

- Persist the binding and request reference before the first storage side effect.
- All requests for one cache key converge through optimistic concurrency.
- A Lease keyed by the binding serializes at most one active writer.
- Retries and agent restarts reuse the binding.
- Creation uses the recorded deterministic name and Get-before-Create. A retry adopts an object only when its binding
  UID and immutable spec match the intent; a mismatch is terminal.
- Record provider data identity only after it exists; do not change it afterward.
- Catalog, feature-gate, and StorageClass updates do not mutate an existing binding.
- Before any resource exists, input drift fails without side effects.
- After a resource exists, reconcile only the binding's resources; never switch providers or delete data because of
  drift.
- Do not include Secret contents.

Cleanup is a state transition, not a list-then-delete check. The reconciler marks the binding `Retiring`, which blocks
new references, while its finalizer prevents deletion. A stale reference is one whose request is absent, has a different
UID, or does not point back to the binding. Request deletion first removes reader resources, then removes the binding
reference, then releases the request finalizer. Provider cleanup starts only at zero references; the binding finalizer
is released only after owned Kubernetes resources are gone. Deleting retained backend data, if required, is an explicit
transition operation, not a response to configuration drift.

## Model-cache contract

PVCs are namespace-scoped. One PVC cannot be mounted by Pods in different namespaces. A same-namespace multi-Pod
read-only test is primitive evidence only.

A regular or Helm model-cache transition can be enabled only if it provides:

- at most one active writer per cache key, serialized by the binding Lease;
- one provider-backed data identity per cache key;
- no source-sized clone or extra data copy;
- namespace-local reader objects that resolve to the same data identity through a documented provider mechanism;
- no duplicate PVs with one CSI `volumeHandle`;
- `readOnly: true` on both the PVC volume source and each matching `volumeMount`;
- a read-only flag on the filesystem mount observed inside each reader container;
- failed write attempts from every reader Pod;
- a durable populated marker that survives writer cleanup and NVCA restart;
- idempotent creation, observation, retry, and deletion;
- cleanup that cannot delete data while another namespace references it.

The current webhook sets `PersistentVolumeClaimVolumeSource.readOnly: true` but sets both model-cache `volumeMount`
values to `false`. Runtime implementation must set and test both mount values as `true` before external qualification.

Kubernetes and generic CSI do not provide a cross-namespace PVC-sharing primitive. Each transition owns its
provider-specific reader-view mapping. A registered transition must document a driver-supported alias or rebind method
that produces namespace-local PVC/PV pairs with distinct CSI `volumeHandle` values resolving to the same backend data.
The generic planner must not synthesize those PVs or assume two PVCs from one class expose the same data. A provider
remains disabled if it cannot meet this contract without a source-sized copy.

Cross-workflow and cross-domain reuse are not assumed. Either requires its own qualified transition and authorization
contract.

## Failure, migration, and rollback

After runtime enforcement is wired, durable selection fails closed for a missing or invalid catalog, a missing or
non-`Retain` `nvcf-sc`, an unknown provisioner or transition, a disabled workflow, or drift before the first side
effect.
Stale resources from the presence-selected sharedfs path are not adopted.

Failure before a binding or request fallback is persisted creates no storage objects. Failure afterward follows the
recorded transition's cleanup rules. `none` or `ephemeral` is used only when recorded on the request.

Legacy requests may have no backend or only a coarse backend value. Runtime migration must enforce:

- requests with existing resources are not rebound to another provider;
- requests without storage side effects may establish or reference a binding;
- ambiguous shared-filesystem resources are not adopted from class presence or labels alone;
- NVMesh retained-primary-PV, sharedfs writer-PVC, and Samba backing-PVC markers are inspected explicitly;
- a migrated request remains stable across retry and restart.

The runtime change must derive the exact legacy conversion from existing resources and cover it with tests.

New unencrypted cache requests must reject the legacy `Agent.ModelCache.StorageClassName` override unless it is empty
or `nvcf-sc`. NVMesh encryption is the exception: selection still uses `nvcf-sc`, while the `nvmesh` transition records
and validates its deterministic per-NCA derived StorageClass before creating the encrypted writer PVC. The NCA sharing
domain is part of the cache key, and the derived class cannot select a provider.

Provider-defining StorageClass fields, including the provisioner and parameters, cannot be changed in place. A
provider change is a maintenance operation:

1. Stop new cache PVC creation.
2. Mark every binding for the current `nvcf-sc` UID `Retiring` so it rejects new references.
3. Drain requests, reach zero references, complete cleanup, and inventory retained data.
4. Verify no active binding references the old UID, then delete and recreate `nvcf-sc`.
5. Verify the new UID, provisioner, configuration digest, provisioning, and mounts.
6. Run functional qualification for the exact deployed configuration.
7. Resume new requests only after catalog and runtime support are deployed.

Rollback repeats the process with the prior definition. `Retain` prevents automatic deletion; it does not migrate data.

## Provider qualification

1. Verify CSI controller health and node-plugin readiness on every eligible CPU and GPU node.
2. Render and verify the exact `nvcf-sc` definition.
3. Run dynamic provisioning and basic mount tests.
4. Prove each cataloged access mode for the exact deployed configuration.
5. Add the catalog entry with both transitions `disabled`.
6. Implement an explicit transition, including its provider-specific reader-view mapping.
7. Run the full workflow suite across namespaces and eligible pools.
8. Record CSI, backend, StorageClass, Kubernetes, node, and dated evidence details.
9. Set only a passing workflow to its implemented transition.
10. Deploy catalog and runtime changes together, then verify new bindings.

A disabled entry does not enable cache traffic.

## Test plan

### Catalog and deployment

- Missing namespace, ConfigMap, key, payload, and chart file.
- Malformed YAML, unknown fields, missing or invalid access modes, and missing transitions.
- Whitespace-only IDs, duplicate modes, unknown transitions, and provider-specific transition misuse.
- `nvmesh` requires the NVMesh provisioner plus RWO and ROX; external providers remain `disabled`.
- Source/release catalog, schema, and template parity.
- Deployment-tooling tests for exact Weka, OCI FSS, OCI Lustre, and generic provider rendering.
- Fixed `nvcf-sc` name and `Retain` policy.
- Deployment-tooling tests for exact parameters, mount options, binding mode, expansion, and topology.
- Deployment tooling preserves NVMesh expansion; the catalog selects `nvmesh` for both workflows.
- Malformed deployment input fails rendering.
- `nvcf-function-storage-sc` remains unchanged.

### Binding and migration

- Feature gates select the request fallback without creating a durable binding.
- The NVMesh provisioner entry selects `nvmesh` for both workflows.
- StorageClass and catalog digests are deterministic, sensitive to included fields, and captured with UIDs.
- Unknown provisioner, disabled transition, and non-`Retain` class.
- Concurrent namespaces in one sharing domain converge on one binding; other workflows or domains use different keys.
- Retry, leader change, and agent restart reuse the binding and Lease.
- Drift before the first side effect fails without resources; drift afterward never switches providers.
- A crash after object creation adopts only an exact deterministic intent and binding UID; a mismatch fails closed.
- `Active` to `Retiring` blocks new references; cleanup waits for zero references and honors the finalizer.
- Request deletion removes reader resources and the binding reference before releasing its finalizer.
- Legacy empty-backend, NVMesh PV, sharedfs PVC, and Samba PVC marker migration.
- Legacy class-override rejection and NVMesh encrypted derived-class validation.
- Provider replacement retires every binding for the old StorageClass UID before new requests resume.
- Stale shared-filesystem guard and cleanup.

### Workflow

- The Lease permits one active writer, including when competing writer Pods share a node.
- Readers in multiple namespaces mount the same provider data identity.
- CPU and GPU readers resolve the same provider data identity.
- Full-file and tree checksum equality.
- Denied create, append, rename, chmod, truncate, and delete.
- Both volume source and volume mount are read-only.
- A same-provider baseline mount is writable by the test UID/GID; the reader reports `ro` in `/proc/self/mountinfo`.
- Each cataloged PVC access mode is created and mounted explicitly.
- An RWX claim with a read-only Pod mount is not reported as ROX evidence.
- Writer and reader restart.
- Reader recreation and rescheduling to another eligible node.
- Agent restart at every lifecycle phase.
- Cancellation and injected provision, mount, writer, and cleanup failures.
- Idempotent retries with no duplicate writer or leaked resources.
- Cleanup with active and inactive readers.
- Upgrade and rollback with existing bindings.
- No source-sized clone, extra copy, or duplicate CSI handle.

Performance testing is a separate follow-up after this functional suite passes.

## Security and observability

Security requirements:

- Store no credentials, Secret contents, tokens, or private endpoints; use CSI-supported Secret references.
- Grant least-privilege read access to the StorageClass and catalog.
- Set read-only intent at every Kubernetes layer and verify write denial.
- Reject unknown fields and transitions.
- Qualification evidence records the exact deployed configuration; published evidence redacts credentials and private
  deployment values while preserving the configuration digest.

Observability requirements:

- Log and trace binding creation, reuse, fallback, drift, transition, and cleanup.
- Count binding outcomes with bounded labels and publish persistence, population, reader, and cleanup conditions.
- Do not use cache handles, PVC names, backend IDs, or binding digests as metric labels.

## Implementation order

Keep external transitions disabled. Add the binding API and strict-loader call site, migrate legacy state, fix read-only
mounts, implement provider-specific no-copy transitions, remove presence selection, then qualify each workflow.

## Public source references

- [Storage catalog](https://github.com/NVIDIA/nvcf/blob/main/src/compute-plane-services/nvca/deployments/nvca-operator/files/nvcf-storage-capabilities-v1alpha1.yaml)
- [Catalog JSON Schema](https://github.com/NVIDIA/nvcf/blob/main/src/compute-plane-services/nvca/deployments/nvca-operator/files/nvcf-storage-capabilities-v1alpha1.schema.json)
- [Catalog loader and validator](https://github.com/NVIDIA/nvcf/blob/main/src/compute-plane-services/nvca/pkg/storage/storage_capabilities.go)
- [Current Helm cache selection](https://github.com/NVIDIA/nvcf/blob/main/src/compute-plane-services/nvca/pkg/storage/cachebackend.go)
- [Current model-cache webhook](https://github.com/NVIDIA/nvcf/blob/main/src/compute-plane-services/nvca/pkg/webhook/helm_storage_webhook.go)
- [Kubernetes persistent-volume access modes](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#access-modes)
- [Runtime integration issue](https://github.com/NVIDIA/nvcf/issues/1326)
